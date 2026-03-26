package budget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

const unknownAgent = "unknown"

// Action is the budget enforcement decision returned by CheckBudget.
type Action string

// Budget enforcement actions.
const (
	ActionAllow     Action = "allow"
	ActionReject    Action = "reject"
	ActionDowngrade Action = "downgrade"
	ActionAlert     Action = "alert"
	ActionKill      Action = "kill"
)

// Clock lets tests control time.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

// Now returns current UTC time.
func (realClock) Now() time.Time {
	return time.Now().UTC()
}

// Decision is a detailed budget enforcement result.
type Decision struct {
	Action   Action
	Code     string
	Message  string
	Agent    string
	Period   config.BudgetPeriod
	LimitUSD float64
	SpentUSD float64
	Over     bool
}

//nolint:govet // keep policy fields grouped by semantic role.
type agentPolicy struct {
	limitUSD              float64
	downgradeThresholdPct float64
	period                config.BudgetPeriod
	actionOnExceed        config.BudgetAction
	downgradeChain        []string
	alertThresholdsPct    []float64
}

//nolint:govet // keep state fields grouped by update/read patterns.
type agentState struct {
	spentUSD        float64
	lastAlertedPct  float64
	periodStartedAt time.Time
	periodResetsAt  time.Time
	requestTimes    []time.Time
	triggeredAlerts map[float64]bool
	killed          bool
}

// Snapshot captures current tracked state for tests and diagnostics.
type Snapshot struct {
	PeriodStartedAt time.Time
	PeriodResetsAt  time.Time
	SpentUSD        float64
	LastAlertedPct  float64
	Killed          bool
}

// BudgetManager tracks agent spend and applies budget enforcement rules.
//
//nolint:revive,govet // Name required by spec; field grouping aids maintainability.
type BudgetManager struct {
	clock        Clock
	logger       *slog.Logger
	defaultState agentPolicy

	mu          sync.RWMutex
	agentPolicy map[string]agentPolicy
	state       map[string]*agentState
	apiKeyMap   []config.APIKeyMapEntry
	runaway     config.RunawayConfig
	emergency   bool
}

// NewManager creates a budget manager from gate configuration.
func NewManager(gate config.GateConfig, logger *slog.Logger) *BudgetManager {
	return NewManagerWithClock(gate, logger, realClock{})
}

// NewManagerWithClock creates a budget manager with an explicit clock.
func NewManagerWithClock(gate config.GateConfig, logger *slog.Logger, clock Clock) *BudgetManager {
	if clock == nil {
		clock = realClock{}
	}

	manager := &BudgetManager{
		clock:       clock,
		logger:      logger,
		agentPolicy: make(map[string]agentPolicy),
		state:       make(map[string]*agentState),
		apiKeyMap:   append([]config.APIKeyMapEntry(nil), gate.APIKeyMap...),
		runaway:     gate.Runaway,
		defaultState: agentPolicy{
			period:                gate.DefaultBudget.Period,
			actionOnExceed:        gate.DefaultBudget.ActionOnExceed,
			limitUSD:              gate.DefaultBudget.LimitUSD,
			downgradeThresholdPct: gate.DowngradeThresholdPct,
			downgradeChain:        append([]string(nil), gate.DefaultDowngradeChain...),
			alertThresholdsPct:    append([]float64(nil), gate.AlertThresholdsPct...),
		},
	}

	for _, entry := range gate.Agents {
		policy := manager.defaultState
		policy.period = entry.Period
		policy.actionOnExceed = entry.ActionOnExceed
		policy.limitUSD = entry.LimitUSD
		if len(entry.DowngradeChain) > 0 {
			policy.downgradeChain = append([]string(nil), entry.DowngradeChain...)
		}
		manager.agentPolicy[strings.ToLower(strings.TrimSpace(entry.Name))] = policy
	}

	return manager
}

// IdentifyAgent returns the calling agent by header, API key mapping, then "unknown".
func (m *BudgetManager) IdentifyAgent(request *http.Request) string {
	if request == nil {
		return unknownAgent
	}

	if headerAgent := strings.TrimSpace(request.Header.Get("X-Oberwatch-Agent")); headerAgent != "" {
		return headerAgent
	}

	apiKey := extractAPIKey(request)
	if apiKey != "" {
		for _, mapping := range m.apiKeyMap {
			prefix := strings.TrimSpace(mapping.APIKeyPrefix)
			if prefix == "" {
				continue
			}
			if strings.HasPrefix(apiKey, prefix) {
				agent := strings.TrimSpace(mapping.Agent)
				if agent != "" {
					return agent
				}
			}
		}
	}

	return unknownAgent
}

// CheckBudget evaluates enforcement action for the agent.
func (m *BudgetManager) CheckBudget(agent string, estimatedCostUSD float64) Action {
	decision := m.CheckBudgetDetailed(agent, estimatedCostUSD)
	return decision.Action
}

// CheckBudgetDetailed evaluates enforcement action and returns full context.
func (m *BudgetManager) CheckBudgetDetailed(agent string, estimatedCostUSD float64) Decision {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	policy := m.policyForAgentLocked(normalizedAgent)
	state := m.stateForAgentLocked(normalizedAgent, policy, now)
	m.maybeResetPeriodLocked(state, policy, now)

	if m.emergency {
		return Decision{
			Action:   ActionKill,
			Code:     "emergency_stop",
			Message:  "Emergency stop is active",
			Agent:    normalizedAgent,
			Period:   policy.period,
			LimitUSD: policy.limitUSD,
			SpentUSD: state.spentUSD,
		}
	}

	if state.killed {
		return Decision{
			Action:   ActionKill,
			Code:     "agent_killed",
			Message:  fmt.Sprintf("Agent '%s' is disabled", normalizedAgent),
			Agent:    normalizedAgent,
			Period:   policy.period,
			LimitUSD: policy.limitUSD,
			SpentUSD: state.spentUSD,
		}
	}

	if m.registerRequestAndDetectRunawayLocked(state, now) {
		state.killed = true
		return Decision{
			Action:   ActionKill,
			Code:     "agent_killed",
			Message:  fmt.Sprintf("Agent '%s' was disabled due to runaway request volume", normalizedAgent),
			Agent:    normalizedAgent,
			Period:   policy.period,
			LimitUSD: policy.limitUSD,
			SpentUSD: state.spentUSD,
		}
	}

	if estimatedCostUSD < 0 {
		estimatedCostUSD = 0
	}

	projectedSpend := state.spentUSD + estimatedCostUSD
	if policy.limitUSD > 0 && projectedSpend > policy.limitUSD {
		return m.overLimitDecision(normalizedAgent, policy, state, projectedSpend)
	}

	if shouldDowngradeForThreshold(policy, projectedSpend) {
		return Decision{
			Action:   ActionDowngrade,
			Agent:    normalizedAgent,
			Period:   policy.period,
			LimitUSD: policy.limitUSD,
			SpentUSD: state.spentUSD,
		}
	}

	if shouldAlertThreshold(policy, state.spentUSD, projectedSpend) {
		return Decision{
			Action:   ActionAlert,
			Agent:    normalizedAgent,
			Period:   policy.period,
			LimitUSD: policy.limitUSD,
			SpentUSD: state.spentUSD,
		}
	}

	return Decision{
		Action:   ActionAllow,
		Agent:    normalizedAgent,
		Period:   policy.period,
		LimitUSD: policy.limitUSD,
		SpentUSD: state.spentUSD,
	}
}

// RecordSpend updates the running spend total for the agent.
func (m *BudgetManager) RecordSpend(agent string, costUSD float64) {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	if costUSD < 0 {
		costUSD = 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	policy := m.policyForAgentLocked(normalizedAgent)
	state := m.stateForAgentLocked(normalizedAgent, policy, now)
	m.maybeResetPeriodLocked(state, policy, now)

	before := percentageUsed(policy.limitUSD, state.spentUSD)
	state.spentUSD += costUSD
	after := percentageUsed(policy.limitUSD, state.spentUSD)

	for _, threshold := range policy.alertThresholdsPct {
		if threshold <= before || threshold > after {
			continue
		}
		if state.triggeredAlerts[threshold] {
			continue
		}
		state.triggeredAlerts[threshold] = true
		state.lastAlertedPct = threshold
		if m.logger != nil {
			m.logger.Warn(
				"budget threshold reached",
				"agent",
				normalizedAgent,
				"threshold_pct",
				threshold,
				"spent_usd",
				state.spentUSD,
				"limit_usd",
				policy.limitUSD,
			)
		}
	}
}

// Snapshot returns current budget state for an agent.
func (m *BudgetManager) Snapshot(agent string) Snapshot {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	policy := m.policyForAgentLocked(normalizedAgent)
	state := m.stateForAgentLocked(normalizedAgent, policy, now)
	m.maybeResetPeriodLocked(state, policy, now)

	return Snapshot{
		SpentUSD:        state.spentUSD,
		PeriodStartedAt: state.periodStartedAt,
		PeriodResetsAt:  state.periodResetsAt,
		Killed:          state.killed,
		LastAlertedPct:  state.lastAlertedPct,
	}
}

// KillAgent disables an agent immediately.
func (m *BudgetManager) KillAgent(agent string) {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	policy := m.policyForAgentLocked(normalizedAgent)
	state := m.stateForAgentLocked(normalizedAgent, policy, now)
	state.killed = true
}

// EnableAgent re-enables a killed agent.
func (m *BudgetManager) EnableAgent(agent string) {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	policy := m.policyForAgentLocked(normalizedAgent)
	state := m.stateForAgentLocked(normalizedAgent, policy, now)
	state.killed = false
}

// SetEmergencyStop enables or disables the global kill switch.
func (m *BudgetManager) SetEmergencyStop(enabled bool) {
	m.mu.Lock()
	m.emergency = enabled
	m.mu.Unlock()
}

// EmergencyStop reports whether the global kill switch is active.
func (m *BudgetManager) EmergencyStop() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.emergency
}

// RewriteModelForDowngrade rewrites the request body model to the next model in chain.
func (m *BudgetManager) RewriteModelForDowngrade(agent string, requestBody []byte) ([]byte, string, string, bool, error) {
	normalizedAgent := normalizeAgent(agent)
	if len(bytes.TrimSpace(requestBody)) == 0 {
		return requestBody, "", "", false, nil
	}

	m.mu.RLock()
	policy := m.policyForAgentLocked(normalizedAgent)
	m.mu.RUnlock()

	if len(policy.downgradeChain) < 2 {
		return requestBody, "", "", false, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		return nil, "", "", false, fmt.Errorf("decode request body for downgrade: %w", err)
	}

	currentRaw, ok := payload["model"]
	if !ok {
		return requestBody, "", "", false, nil
	}

	currentModel, ok := currentRaw.(string)
	if !ok {
		return requestBody, "", "", false, nil
	}

	nextModel := nextInDowngradeChain(policy.downgradeChain, currentModel)
	if nextModel == "" {
		return requestBody, currentModel, "", false, nil
	}

	payload["model"] = nextModel
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil, "", "", false, fmt.Errorf("encode rewritten downgrade request body: %w", err)
	}

	return rewritten, currentModel, nextModel, true, nil
}

func (m *BudgetManager) overLimitDecision(agent string, policy agentPolicy, state *agentState, projected float64) Decision {
	message := fmt.Sprintf(
		"Agent '%s' has exceeded its %s budget of $%.2f (spent: $%.2f)",
		agent,
		policy.period,
		policy.limitUSD,
		projected,
	)

	decision := Decision{
		Agent:    agent,
		Period:   policy.period,
		LimitUSD: policy.limitUSD,
		SpentUSD: state.spentUSD,
		Over:     true,
		Code:     "budget_exceeded",
		Message:  message,
	}

	switch policy.actionOnExceed {
	case config.BudgetActionReject:
		decision.Action = ActionReject
	case config.BudgetActionDowngrade:
		decision.Action = ActionDowngrade
	case config.BudgetActionAlert:
		decision.Action = ActionAlert
	case config.BudgetActionKill:
		state.killed = true
		decision.Action = ActionKill
		decision.Code = "agent_killed"
		decision.Message = fmt.Sprintf("Agent '%s' is disabled after budget exceed", agent)
	default:
		decision.Action = ActionReject
	}

	return decision
}

func (m *BudgetManager) registerRequestAndDetectRunawayLocked(state *agentState, now time.Time) bool {
	if !m.runaway.Enabled || m.runaway.MaxRequests < 1 || m.runaway.WindowSeconds < 1 {
		return false
	}

	windowStart := now.Add(-time.Duration(m.runaway.WindowSeconds) * time.Second)
	kept := state.requestTimes[:0]
	for _, ts := range state.requestTimes {
		if ts.After(windowStart) || ts.Equal(windowStart) {
			kept = append(kept, ts)
		}
	}
	state.requestTimes = kept
	state.requestTimes = append(state.requestTimes, now)

	return len(state.requestTimes) > m.runaway.MaxRequests
}

func (m *BudgetManager) policyForAgentLocked(agent string) agentPolicy {
	if policy, ok := m.agentPolicy[agent]; ok {
		return policy
	}
	return m.defaultState
}

func (m *BudgetManager) stateForAgentLocked(agent string, policy agentPolicy, now time.Time) *agentState {
	state, ok := m.state[agent]
	if ok {
		return state
	}
	state = &agentState{
		periodStartedAt: now,
		periodResetsAt:  nextPeriodReset(now, policy.period),
		triggeredAlerts: make(map[float64]bool),
	}
	m.state[agent] = state
	return state
}

func (m *BudgetManager) maybeResetPeriodLocked(state *agentState, policy agentPolicy, now time.Time) {
	if now.Before(state.periodResetsAt) {
		return
	}
	state.spentUSD = 0
	state.requestTimes = state.requestTimes[:0]
	state.periodStartedAt = now
	state.periodResetsAt = nextPeriodReset(now, policy.period)
	state.triggeredAlerts = make(map[float64]bool)
	state.lastAlertedPct = 0
}

func nextPeriodReset(start time.Time, period config.BudgetPeriod) time.Time {
	switch period {
	case config.BudgetPeriodHourly:
		return start.Add(1 * time.Hour)
	case config.BudgetPeriodWeekly:
		return start.Add(7 * 24 * time.Hour)
	case config.BudgetPeriodMonthly:
		return start.AddDate(0, 1, 0)
	case config.BudgetPeriodDaily:
		fallthrough
	default:
		return start.Add(24 * time.Hour)
	}
}

func percentageUsed(limit float64, spent float64) float64 {
	if limit <= 0 {
		return 0
	}
	if spent <= 0 {
		return 0
	}
	return (spent / limit) * 100
}

func shouldDowngradeForThreshold(policy agentPolicy, projectedSpend float64) bool {
	if policy.limitUSD <= 0 || len(policy.downgradeChain) == 0 {
		return false
	}
	threshold := policy.downgradeThresholdPct
	if threshold <= 0 {
		return false
	}
	return percentageUsed(policy.limitUSD, projectedSpend) >= threshold
}

func shouldAlertThreshold(policy agentPolicy, currentSpend float64, projectedSpend float64) bool {
	if policy.limitUSD <= 0 || len(policy.alertThresholdsPct) == 0 {
		return false
	}

	before := percentageUsed(policy.limitUSD, currentSpend)
	after := percentageUsed(policy.limitUSD, projectedSpend)
	for _, threshold := range policy.alertThresholdsPct {
		if threshold > before && threshold <= after {
			return true
		}
	}
	return false
}

func nextInDowngradeChain(chain []string, currentModel string) string {
	normalizedCurrent := strings.ToLower(strings.TrimSpace(currentModel))
	for i := 0; i < len(chain); i++ {
		if strings.ToLower(strings.TrimSpace(chain[i])) != normalizedCurrent {
			continue
		}
		nextIndex := i + 1
		if nextIndex >= len(chain) {
			return ""
		}
		return chain[nextIndex]
	}
	return ""
}

func normalizeAgent(agent string) string {
	trimmed := strings.TrimSpace(agent)
	if trimmed == "" {
		return unknownAgent
	}
	return trimmed
}

func extractAPIKey(request *http.Request) string {
	if request == nil {
		return ""
	}

	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if authorization != "" {
		parts := strings.Fields(authorization)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}

	return strings.TrimSpace(request.Header.Get("x-api-key"))
}
