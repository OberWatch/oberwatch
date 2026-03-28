package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

const unknownAgent = "unknown"

const (
	disableReasonNone           = ""
	disableReasonBudgetExceeded = "budget_exceeded"
	disableReasonManualKill     = "manual_kill"
	disableReasonRunaway        = "runaway_detected"
)

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
	lastSeenAt      time.Time
	requestTimes    []time.Time
	triggeredAlerts map[float64]bool
	killed          bool
	disableReason   string
	dirty           bool
}

// Snapshot captures current tracked state for tests and diagnostics.
type Snapshot struct {
	PeriodStartedAt time.Time
	PeriodResetsAt  time.Time
	SpentUSD        float64
	LastAlertedPct  float64
	Killed          bool
}

// BudgetView is an API-friendly view of one agent budget state.
//
//nolint:revive,govet // Name/field grouping are intentional for API clarity.
type BudgetView struct {
	Agent           string
	Status          string
	Period          config.BudgetPeriod
	ActionOnExceed  config.BudgetAction
	PeriodStartedAt time.Time
	PeriodResetsAt  time.Time
	LimitUSD        float64
	SpentUSD        float64
	RemainingUSD    float64
	PercentageUsed  float64
}

// BudgetUpdate applies mutable budget policy fields for one agent.
//
//nolint:revive,govet // Name/field grouping are intentional for API clarity.
type BudgetUpdate struct {
	Period                config.BudgetPeriod
	ActionOnExceed        config.BudgetAction
	DowngradeThresholdPct float64
	LimitUSD              float64
	DowngradeChain        []string
	AlertThresholdsPct    []float64
}

// Dispatcher routes budget-generated alert events.
type Dispatcher interface {
	Dispatch(context.Context, alert.Alert)
}

// BudgetManager tracks agent spend and applies budget enforcement rules.
//
//nolint:revive,govet // Name required by spec; field grouping aids maintainability.
type BudgetManager struct {
	clock        Clock
	logger       *slog.Logger
	dispatcher   Dispatcher
	store        storage.Store
	defaultState agentPolicy

	mu            sync.RWMutex
	agentPolicy   map[string]agentPolicy
	state         map[string]*agentState
	apiKeyMap     []config.APIKeyMapEntry
	runaway       config.RunawayConfig
	emergency     bool
	knownAgents   map[string]struct{}
	flushInterval time.Duration
	flushStop     chan struct{}
	flushWG       sync.WaitGroup
}

// NewManager creates a budget manager from gate configuration.
func NewManager(gate config.GateConfig, logger *slog.Logger) *BudgetManager {
	manager, _ := newManager(gate, logger, nil, realClock{}, nil, false)
	return manager
}

// NewManagerWithClock creates a budget manager with an explicit clock.
func NewManagerWithClock(gate config.GateConfig, logger *slog.Logger, clock Clock) *BudgetManager {
	manager, _ := newManager(gate, logger, nil, clock, nil, false)
	return manager
}

// NewManagerWithClockAndDispatcher creates a budget manager with explicit clock and alert dispatcher.
func NewManagerWithClockAndDispatcher(gate config.GateConfig, logger *slog.Logger, clock Clock, dispatcher Dispatcher) *BudgetManager {
	manager, _ := newManager(gate, logger, nil, clock, dispatcher, false)
	return manager
}

// NewPersistentManager creates a budget manager backed by SQLite agents.
func NewPersistentManager(gate config.GateConfig, logger *slog.Logger, store storage.Store) (*BudgetManager, error) {
	return newManager(gate, logger, store, realClock{}, nil, true)
}

// NewPersistentManagerWithClockAndDispatcher creates a persistent budget manager with explicit clock and dispatcher.
func NewPersistentManagerWithClockAndDispatcher(
	gate config.GateConfig,
	logger *slog.Logger,
	store storage.Store,
	clock Clock,
	dispatcher Dispatcher,
) (*BudgetManager, error) {
	return newManager(gate, logger, store, clock, dispatcher, true)
}

// SeedConfiguredAgents writes TOML-configured agents into the persistent store.
func SeedConfiguredAgents(ctx context.Context, gate config.GateConfig, store storage.Store, clock Clock) error {
	if store == nil {
		return nil
	}
	if clock == nil {
		clock = realClock{}
	}

	defaultPolicy := agentPolicy{
		period:                gate.DefaultBudget.Period,
		actionOnExceed:        gate.DefaultBudget.ActionOnExceed,
		limitUSD:              gate.DefaultBudget.LimitUSD,
		downgradeThresholdPct: gate.DowngradeThresholdPct,
		downgradeChain:        append([]string(nil), gate.DefaultDowngradeChain...),
		alertThresholdsPct:    append([]float64(nil), gate.AlertThresholdsPct...),
	}

	for _, entry := range gate.Agents {
		normalized := normalizeAgent(entry.Name)
		policy := clonePolicy(defaultPolicy)
		policy.period = entry.Period
		policy.actionOnExceed = entry.ActionOnExceed
		policy.limitUSD = entry.LimitUSD
		if len(entry.DowngradeChain) > 0 {
			policy.downgradeChain = append([]string(nil), entry.DowngradeChain...)
		}

		record, found, err := store.GetAgent(ctx, normalized)
		if err != nil {
			return fmt.Errorf("load persisted agent %q: %w", normalized, err)
		}
		if !found {
			now := clock.Now().UTC()
			record = storage.AgentRecord{
				Name:            normalized,
				Status:          "active",
				PeriodStartedAt: now,
				PeriodResetsAt:  nextPeriodReset(now, policy.period),
				FirstSeenAt:     now,
				LastSeenAt:      now,
			}
		}

		record.Name = normalized
		record.BudgetLimitUSD = policy.limitUSD
		record.BudgetPeriod = policy.period
		record.ActionOnExceed = policy.actionOnExceed
		record.DowngradeChain = append([]string(nil), policy.downgradeChain...)
		record.DowngradeThresholdPct = policy.downgradeThresholdPct
		record.AlertThresholdsPct = append([]float64(nil), policy.alertThresholdsPct...)
		if record.PeriodStartedAt.IsZero() {
			record.PeriodStartedAt = clock.Now().UTC()
		}
		if record.PeriodResetsAt.IsZero() {
			record.PeriodResetsAt = nextPeriodReset(record.PeriodStartedAt, policy.period)
		}

		if err := store.UpsertAgent(ctx, record); err != nil {
			return fmt.Errorf("seed configured agent %q: %w", normalized, err)
		}
	}

	return nil
}

func newManager(
	gate config.GateConfig,
	logger *slog.Logger,
	store storage.Store,
	clock Clock,
	dispatcher Dispatcher,
	persistent bool,
) (*BudgetManager, error) {
	if clock == nil {
		clock = realClock{}
	}

	manager := &BudgetManager{
		clock:       clock,
		logger:      logger,
		dispatcher:  dispatcher,
		store:       store,
		agentPolicy: make(map[string]agentPolicy),
		state:       make(map[string]*agentState),
		apiKeyMap:   append([]config.APIKeyMapEntry(nil), gate.APIKeyMap...),
		knownAgents: make(map[string]struct{}),
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
		normalized := normalizeAgent(entry.Name)
		manager.agentPolicy[normalized] = policy
		manager.knownAgents[normalized] = struct{}{}
	}

	if persistent && store != nil {
		if err := manager.loadPersistedAgents(context.Background()); err != nil {
			return nil, err
		}
		manager.flushInterval = 30 * time.Second
		manager.startFlushLoop()
	}

	return manager, nil
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
	queuedAlerts := make([]alert.Alert, 0, 2)
	flushAgent := ""

	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		m.dispatchAlerts(queuedAlerts)
		m.flushAgentIfNeeded(flushAgent)
	}()

	policy := m.policyForAgentLocked(normalizedAgent)
	state, created := m.stateForAgentLocked(normalizedAgent, policy, now)
	state.lastSeenAt = now
	m.maybeResetPeriodLocked(state, policy, now)
	if created {
		flushAgent = normalizedAgent
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
		state.disableReason = disableReasonRunaway
		state.dirty = true
		queuedAlerts = append(
			queuedAlerts,
			alert.NewRunawayDetectedAlert(normalizedAgent, len(state.requestTimes), m.runaway.WindowSeconds),
			alert.NewAgentKilledAlert(normalizedAgent, "runaway_detected"),
		)
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
		decision := m.overLimitDecision(normalizedAgent, policy, state, projectedSpend)
		queuedAlerts = append(
			queuedAlerts,
			alert.NewBudgetExceededAlert(normalizedAgent, projectedSpend, policy.limitUSD, string(decision.Action)),
		)
		if decision.Action == ActionKill {
			queuedAlerts = append(queuedAlerts, alert.NewAgentKilledAlert(normalizedAgent, "budget_exceeded"))
		}
		return decision
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
	queuedAlerts := make([]alert.Alert, 0, 2)
	flushAgent := ""

	if costUSD < 0 {
		costUSD = 0
	}

	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		m.dispatchAlerts(queuedAlerts)
		m.flushAgentIfNeeded(flushAgent)
	}()

	policy := m.policyForAgentLocked(normalizedAgent)
	state, created := m.stateForAgentLocked(normalizedAgent, policy, now)
	state.lastSeenAt = now
	m.maybeResetPeriodLocked(state, policy, now)

	before := percentageUsed(policy.limitUSD, state.spentUSD)
	state.spentUSD += costUSD
	state.dirty = true
	after := percentageUsed(policy.limitUSD, state.spentUSD)
	if created {
		flushAgent = normalizedAgent
	}

	for _, threshold := range policy.alertThresholdsPct {
		if threshold <= before || threshold > after {
			continue
		}
		if state.triggeredAlerts[threshold] {
			continue
		}
		state.triggeredAlerts[threshold] = true
		state.lastAlertedPct = threshold
		queuedAlerts = append(queuedAlerts, alert.NewBudgetThresholdAlert(
			normalizedAgent,
			threshold,
			state.spentUSD,
			policy.limitUSD,
			string(policy.actionOnExceed),
			state.periodStartedAt,
		))
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
	state, _ := m.stateForAgentLocked(normalizedAgent, policy, now)
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
	queuedAlerts := make([]alert.Alert, 0, 1)

	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		m.dispatchAlerts(queuedAlerts)
		m.flushAgentIfNeeded(normalizedAgent)
	}()

	policy := m.policyForAgentLocked(normalizedAgent)
	state, _ := m.stateForAgentLocked(normalizedAgent, policy, now)
	state.killed = true
	state.disableReason = disableReasonManualKill
	state.lastSeenAt = now
	state.dirty = true
	queuedAlerts = append(queuedAlerts, alert.NewAgentKilledAlert(normalizedAgent, "manual_kill"))
}

// EnableAgent re-enables a killed agent.
func (m *BudgetManager) EnableAgent(agent string) {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		m.flushAgentIfNeeded(normalizedAgent)
	}()

	policy := m.policyForAgentLocked(normalizedAgent)
	state, _ := m.stateForAgentLocked(normalizedAgent, policy, now)
	state.killed = false
	state.disableReason = disableReasonNone
	state.lastSeenAt = now
	state.dirty = true
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
		state.disableReason = disableReasonBudgetExceeded
		state.dirty = true
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

func (m *BudgetManager) stateForAgentLocked(agent string, policy agentPolicy, now time.Time) (*agentState, bool) {
	state, ok := m.state[agent]
	if ok {
		return state, false
	}
	state = &agentState{
		periodStartedAt: now,
		periodResetsAt:  nextPeriodReset(now, policy.period),
		lastSeenAt:      now,
		triggeredAlerts: make(map[float64]bool),
		dirty:           true,
	}
	m.state[agent] = state
	m.knownAgents[agent] = struct{}{}
	return state, true
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
	if state.disableReason == disableReasonBudgetExceeded || legacyBudgetKill(policy, state) {
		state.killed = false
		state.disableReason = disableReasonNone
	}
	state.dirty = true
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

func (m *BudgetManager) dispatchAlerts(events []alert.Alert) {
	if m.dispatcher == nil || len(events) == 0 {
		return
	}
	ctx := context.Background()
	for _, event := range events {
		m.dispatcher.Dispatch(ctx, event)
	}
}

func (m *BudgetManager) loadPersistedAgents(ctx context.Context) error {
	if m.store == nil {
		return nil
	}

	records, err := m.store.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("load persisted agents: %w", err)
	}

	now := m.clock.Now().UTC()
	for _, record := range records {
		agent := normalizeAgent(record.Name)
		m.agentPolicy[agent] = agentPolicy{
			limitUSD:              record.BudgetLimitUSD,
			downgradeThresholdPct: record.DowngradeThresholdPct,
			period:                record.BudgetPeriod,
			actionOnExceed:        record.ActionOnExceed,
			downgradeChain:        append([]string(nil), record.DowngradeChain...),
			alertThresholdsPct:    append([]float64(nil), record.AlertThresholdsPct...),
		}
		m.state[agent] = &agentState{
			spentUSD:        record.BudgetSpentUSD,
			periodStartedAt: firstNonZeroTime(record.PeriodStartedAt, now),
			periodResetsAt:  firstNonZeroTime(record.PeriodResetsAt, nextPeriodReset(firstNonZeroTime(record.PeriodStartedAt, now), record.BudgetPeriod)),
			lastSeenAt:      firstNonZeroTime(record.LastSeenAt, now),
			triggeredAlerts: triggeredAlerts(record.AlertThresholdsPct, record.BudgetLimitUSD, record.BudgetSpentUSD),
			killed:          persistedAgentDisabled(record.Status),
			disableReason:   persistedDisableReason(record.Status, record.BudgetLimitUSD, record.BudgetSpentUSD, record.ActionOnExceed),
			dirty:           false,
		}
		m.knownAgents[agent] = struct{}{}
	}

	return nil
}

func (m *BudgetManager) startFlushLoop() {
	if m.store == nil || m.flushInterval <= 0 {
		return
	}
	m.flushStop = make(chan struct{})
	m.flushWG.Add(1)
	go func() {
		defer m.flushWG.Done()
		ticker := time.NewTicker(m.flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := m.Flush(context.Background()); err != nil && m.logger != nil {
					m.logger.Warn("flush agent budgets failed", "error", err)
				}
			case <-m.flushStop:
				return
			}
		}
	}()
}

// Close stops background persistence and flushes pending spend state.
func (m *BudgetManager) Close() error {
	if m.flushStop != nil {
		close(m.flushStop)
		m.flushWG.Wait()
	}
	return m.Flush(context.Background())
}

// Flush persists dirty agent spend/config state to the backing store.
func (m *BudgetManager) Flush(ctx context.Context) error {
	if m.store == nil {
		return nil
	}

	records := m.snapshotDirtyAgentRecords()
	for _, record := range records {
		if err := m.store.UpsertAgent(ctx, record); err != nil {
			return fmt.Errorf("flush agent %q: %w", record.Name, err)
		}
	}
	return nil
}

// RenameAgent changes the runtime and persisted name for one tracked agent.
func (m *BudgetManager) RenameAgent(ctx context.Context, oldName string, newName string) error {
	oldName = normalizeAgent(oldName)
	newName = normalizeAgent(newName)
	if oldName == newName {
		return nil
	}

	m.mu.Lock()
	if _, exists := m.knownAgents[newName]; exists {
		m.mu.Unlock()
		return storage.ErrAgentExists
	}

	policy := m.policyForAgentLocked(oldName)
	state, _ := m.stateForAgentLocked(oldName, policy, m.clock.Now().UTC())
	delete(m.agentPolicy, oldName)
	delete(m.state, oldName)
	delete(m.knownAgents, oldName)
	m.agentPolicy[newName] = clonePolicy(policy)
	m.state[newName] = cloneState(state)
	m.knownAgents[newName] = struct{}{}
	m.state[newName].dirty = true
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.RenameAgent(ctx, oldName, newName); err != nil {
			m.mu.Lock()
			delete(m.agentPolicy, newName)
			delete(m.state, newName)
			delete(m.knownAgents, newName)
			m.agentPolicy[oldName] = policy
			m.state[oldName] = state
			m.knownAgents[oldName] = struct{}{}
			m.mu.Unlock()
			return err
		}
	}

	return m.Flush(ctx)
}

func (m *BudgetManager) flushAgentIfNeeded(agent string) {
	if m.store == nil || strings.TrimSpace(agent) == "" {
		return
	}
	if err := m.Flush(context.Background()); err != nil && m.logger != nil {
		m.logger.Warn("flush agent state failed", "agent", agent, "error", err)
	}
}

func (m *BudgetManager) snapshotDirtyAgentRecords() []storage.AgentRecord {
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	records := make([]storage.AgentRecord, 0, len(m.state))
	for agent := range m.knownAgents {
		policy := m.policyForAgentLocked(agent)
		state, _ := m.stateForAgentLocked(agent, policy, now)
		m.maybeResetPeriodLocked(state, policy, now)
		if !state.dirty {
			continue
		}
		records = append(records, m.agentRecordLocked(agent, policy, state, now))
		state.dirty = false
	}
	return records
}

func (m *BudgetManager) agentRecordLocked(agent string, policy agentPolicy, state *agentState, now time.Time) storage.AgentRecord {
	status := persistedStatusForState(state)
	firstSeenAt := state.periodStartedAt
	if firstSeenAt.IsZero() {
		firstSeenAt = now
	}
	lastSeenAt := state.lastSeenAt
	if lastSeenAt.IsZero() {
		lastSeenAt = now
	}
	return storage.AgentRecord{
		Name:                  agent,
		Status:                status,
		BudgetLimitUSD:        policy.limitUSD,
		BudgetSpentUSD:        state.spentUSD,
		BudgetPeriod:          policy.period,
		ActionOnExceed:        policy.actionOnExceed,
		DowngradeChain:        append([]string(nil), policy.downgradeChain...),
		DowngradeThresholdPct: policy.downgradeThresholdPct,
		AlertThresholdsPct:    append([]float64(nil), policy.alertThresholdsPct...),
		PeriodStartedAt:       state.periodStartedAt,
		PeriodResetsAt:        state.periodResetsAt,
		FirstSeenAt:           firstSeenAt,
		LastSeenAt:            lastSeenAt,
	}
}

func clonePolicy(policy agentPolicy) agentPolicy {
	return agentPolicy{
		limitUSD:              policy.limitUSD,
		downgradeThresholdPct: policy.downgradeThresholdPct,
		period:                policy.period,
		actionOnExceed:        policy.actionOnExceed,
		downgradeChain:        append([]string(nil), policy.downgradeChain...),
		alertThresholdsPct:    append([]float64(nil), policy.alertThresholdsPct...),
	}
}

func cloneState(state *agentState) *agentState {
	if state == nil {
		return &agentState{triggeredAlerts: make(map[float64]bool)}
	}
	triggered := make(map[float64]bool, len(state.triggeredAlerts))
	for threshold, fired := range state.triggeredAlerts {
		triggered[threshold] = fired
	}
	return &agentState{
		spentUSD:        state.spentUSD,
		lastAlertedPct:  state.lastAlertedPct,
		periodStartedAt: state.periodStartedAt,
		periodResetsAt:  state.periodResetsAt,
		lastSeenAt:      state.lastSeenAt,
		requestTimes:    append([]time.Time(nil), state.requestTimes...),
		triggeredAlerts: triggered,
		killed:          state.killed,
		disableReason:   state.disableReason,
		dirty:           state.dirty,
	}
}

func persistedAgentDisabled(status string) bool {
	normalized := strings.TrimSpace(strings.ToLower(status))
	return normalized != "" && normalized != "active"
}

func persistedDisableReason(
	status string,
	limitUSD float64,
	spentUSD float64,
	actionOnExceed config.BudgetAction,
) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case disableReasonBudgetExceeded:
		return disableReasonBudgetExceeded
	case disableReasonManualKill:
		return disableReasonManualKill
	case disableReasonRunaway:
		return disableReasonRunaway
	case "killed":
		if limitUSD > 0 && spentUSD >= limitUSD && actionOnExceed == config.BudgetActionKill {
			return disableReasonBudgetExceeded
		}
		return disableReasonManualKill
	default:
		return disableReasonNone
	}
}

func persistedStatusForState(state *agentState) string {
	if state == nil || !state.killed {
		return "active"
	}
	if strings.TrimSpace(state.disableReason) != "" {
		return state.disableReason
	}
	return "killed"
}

func legacyBudgetKill(policy agentPolicy, state *agentState) bool {
	return state.killed &&
		state.disableReason == disableReasonNone &&
		policy.actionOnExceed == config.BudgetActionKill &&
		policy.limitUSD > 0 &&
		state.spentUSD >= policy.limitUSD
}

func firstNonZeroTime(value time.Time, fallback time.Time) time.Time {
	if !value.IsZero() {
		return value.UTC()
	}
	return fallback.UTC()
}

func triggeredAlerts(thresholds []float64, limitUSD float64, spentUSD float64) map[float64]bool {
	fired := make(map[float64]bool)
	for _, threshold := range thresholds {
		if percentageUsed(limitUSD, spentUSD) >= threshold {
			fired[threshold] = true
		}
	}
	return fired
}

// ListBudgets returns all known per-agent budget states.
func (m *BudgetManager) ListBudgets() []BudgetView {
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	keys := make(map[string]struct{}, len(m.knownAgents)+len(m.agentPolicy)+len(m.state))
	for key := range m.knownAgents {
		keys[key] = struct{}{}
	}
	for key := range m.agentPolicy {
		keys[key] = struct{}{}
	}
	for key := range m.state {
		keys[key] = struct{}{}
	}

	result := make([]BudgetView, 0, len(keys))
	for agent := range keys {
		policy := m.policyForAgentLocked(agent)
		state, _ := m.stateForAgentLocked(agent, policy, now)
		m.maybeResetPeriodLocked(state, policy, now)
		result = append(result, toBudgetView(agent, policy, state))
	}

	return result
}

// GetBudget returns one agent budget state.
func (m *BudgetManager) GetBudget(agent string) BudgetView {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	policy := m.policyForAgentLocked(normalizedAgent)
	state, _ := m.stateForAgentLocked(normalizedAgent, policy, now)
	m.maybeResetPeriodLocked(state, policy, now)
	return toBudgetView(normalizedAgent, policy, state)
}

// UpdateBudget mutates runtime budget policy for one agent.
func (m *BudgetManager) UpdateBudget(agent string, update BudgetUpdate) error {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	if update.LimitUSD < 0 {
		return fmt.Errorf("limit_usd must be non-negative")
	}
	if update.DowngradeThresholdPct < 0 || update.DowngradeThresholdPct > 100 {
		return fmt.Errorf("downgrade_threshold_pct must be between 0 and 100")
	}

	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		m.flushAgentIfNeeded(normalizedAgent)
	}()

	policy := m.policyForAgentLocked(normalizedAgent)
	policy.limitUSD = update.LimitUSD
	policy.period = update.Period
	policy.actionOnExceed = update.ActionOnExceed
	policy.downgradeThresholdPct = update.DowngradeThresholdPct
	policy.downgradeChain = append([]string(nil), update.DowngradeChain...)
	policy.alertThresholdsPct = append([]float64(nil), update.AlertThresholdsPct...)
	m.agentPolicy[normalizedAgent] = policy

	state, _ := m.stateForAgentLocked(normalizedAgent, policy, now)
	m.maybeResetPeriodLocked(state, policy, now)
	state.lastSeenAt = now
	state.dirty = true
	return nil
}

// ResetBudget clears spent state for one agent.
func (m *BudgetManager) ResetBudget(agent string) {
	normalizedAgent := normalizeAgent(agent)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		m.flushAgentIfNeeded(normalizedAgent)
	}()

	policy := m.policyForAgentLocked(normalizedAgent)
	state, _ := m.stateForAgentLocked(normalizedAgent, policy, now)
	state.spentUSD = 0
	state.lastAlertedPct = 0
	state.periodStartedAt = now
	state.periodResetsAt = nextPeriodReset(now, policy.period)
	state.triggeredAlerts = make(map[float64]bool)
	state.requestTimes = state.requestTimes[:0]
	if state.disableReason == disableReasonBudgetExceeded || legacyBudgetKill(policy, state) {
		state.killed = false
		state.disableReason = disableReasonNone
	}
	state.lastSeenAt = now
	state.dirty = true
}

func toBudgetView(agent string, policy agentPolicy, state *agentState) BudgetView {
	remaining := policy.limitUSD - state.spentUSD
	if remaining < 0 {
		remaining = 0
	}
	status := "active"
	if state.killed {
		status = "killed"
	}
	return BudgetView{
		Agent:           agent,
		Status:          status,
		Period:          policy.period,
		ActionOnExceed:  policy.actionOnExceed,
		PeriodStartedAt: state.periodStartedAt,
		PeriodResetsAt:  state.periodResetsAt,
		LimitUSD:        policy.limitUSD,
		SpentUSD:        state.spentUSD,
		RemainingUSD:    remaining,
		PercentageUsed:  percentageUsed(policy.limitUSD, state.spentUSD),
	}
}
