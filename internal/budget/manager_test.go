package budget

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

type mockClock struct {
	now time.Time
	mu  sync.RWMutex
}

func newMockClock(start time.Time) *mockClock {
	return &mockClock{now: start.UTC()}
}

func (c *mockClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *mockClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

func baseGateConfig() config.GateConfig {
	cfg := config.DefaultConfig().Gate
	cfg.DefaultBudget.LimitUSD = 10
	cfg.DefaultBudget.Period = config.BudgetPeriodDaily
	cfg.DefaultBudget.ActionOnExceed = config.BudgetActionReject
	cfg.DowngradeThresholdPct = 80
	cfg.DefaultDowngradeChain = []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"}
	cfg.AlertThresholdsPct = []float64{50, 80, 100}
	cfg.Runaway.Enabled = true
	cfg.Runaway.MaxRequests = 100
	cfg.Runaway.WindowSeconds = 60
	cfg.APIKeyMap = []config.APIKeyMapEntry{{APIKeyPrefix: "sk-live-", Agent: "mapped-agent"}}
	cfg.Agents = nil
	return cfg
}

func TestIdentifyAgent(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name: "header wins",
			headers: map[string]string{
				"X-Oberwatch-Agent": "email-agent",
				"Authorization":     "Bearer sk-live-anything",
			},
			want: "email-agent",
		},
		{
			name: "api key map fallback",
			headers: map[string]string{
				"Authorization": "Bearer sk-live-abc",
			},
			want: "mapped-agent",
		},
		{
			name: "unknown fallback",
			headers: map[string]string{
				"Authorization": "Bearer something-else",
			},
			want: "unknown",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequest(http.MethodPost, "http://example.test/v1/chat/completions", nil)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			got := manager.IdentifyAgent(req)
			if got != tt.want {
				t.Fatalf("IdentifyAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckBudgetActions(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table readable.
	tests := []struct {
		name          string
		setup         func(*config.GateConfig)
		initialSpend  float64
		estimatedCost float64
		wantAction    Action
		wantCode      string
	}{
		{
			name:          "under limit allows",
			initialSpend:  2,
			estimatedCost: 1,
			wantAction:    ActionAllow,
		},
		{
			name:          "threshold triggers downgrade",
			initialSpend:  8,
			estimatedCost: 0,
			wantAction:    ActionDowngrade,
		},
		{
			name:          "over limit reject",
			initialSpend:  9.5,
			estimatedCost: 1,
			wantAction:    ActionReject,
			wantCode:      "budget_exceeded",
		},
		{
			name: "over limit alert",
			setup: func(cfg *config.GateConfig) {
				cfg.DefaultBudget.ActionOnExceed = config.BudgetActionAlert
			},
			initialSpend:  9.5,
			estimatedCost: 1,
			wantAction:    ActionAlert,
			wantCode:      "budget_exceeded",
		},
		{
			name: "over limit kill",
			setup: func(cfg *config.GateConfig) {
				cfg.DefaultBudget.ActionOnExceed = config.BudgetActionKill
			},
			initialSpend:  9.5,
			estimatedCost: 1,
			wantAction:    ActionKill,
			wantCode:      "agent_killed",
		},
		{
			name: "over limit downgrade",
			setup: func(cfg *config.GateConfig) {
				cfg.DefaultBudget.ActionOnExceed = config.BudgetActionDowngrade
			},
			initialSpend:  9.5,
			estimatedCost: 1,
			wantAction:    ActionDowngrade,
			wantCode:      "budget_exceeded",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
			cfg := baseGateConfig()
			if tt.setup != nil {
				tt.setup(&cfg)
			}
			manager := NewManagerWithClock(cfg, nil, clock)
			if tt.initialSpend > 0 {
				manager.RecordSpend("agent-a", tt.initialSpend)
			}

			decision := manager.CheckBudgetDetailed("agent-a", tt.estimatedCost)
			if decision.Action != tt.wantAction {
				t.Fatalf("CheckBudgetDetailed().Action = %q, want %q", decision.Action, tt.wantAction)
			}
			if tt.wantCode != "" && decision.Code != tt.wantCode {
				t.Fatalf("CheckBudgetDetailed().Code = %q, want %q", decision.Code, tt.wantCode)
			}
		})
	}
}

func TestRunawayDetectionKillsAgent(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	cfg := baseGateConfig()
	cfg.Runaway.Enabled = true
	cfg.Runaway.MaxRequests = 2
	cfg.Runaway.WindowSeconds = 60

	manager := NewManagerWithClock(cfg, nil, clock)
	if action := manager.CheckBudget("agent-a", 0); action != ActionAllow {
		t.Fatalf("first action = %q, want %q", action, ActionAllow)
	}
	if action := manager.CheckBudget("agent-a", 0); action != ActionAllow {
		t.Fatalf("second action = %q, want %q", action, ActionAllow)
	}
	if action := manager.CheckBudget("agent-a", 0); action != ActionKill {
		t.Fatalf("third action = %q, want %q", action, ActionKill)
	}

	if action := manager.CheckBudget("agent-a", 0); action != ActionKill {
		t.Fatalf("killed agent action = %q, want %q", action, ActionKill)
	}
}

func TestPeriodReset(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	clock := newMockClock(start)
	cfg := baseGateConfig()
	cfg.DefaultBudget.Period = config.BudgetPeriodHourly
	manager := NewManagerWithClock(cfg, nil, clock)

	manager.RecordSpend("agent-a", 3)
	snapshot := manager.Snapshot("agent-a")
	if snapshot.SpentUSD != 3 {
		t.Fatalf("spent before reset = %v, want 3", snapshot.SpentUSD)
	}
	if !snapshot.PeriodResetsAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("period reset at = %v, want %v", snapshot.PeriodResetsAt, start.Add(time.Hour))
	}

	clock.Advance(61 * time.Minute)
	snapshot = manager.Snapshot("agent-a")
	if snapshot.SpentUSD != 0 {
		t.Fatalf("spent after reset = %v, want 0", snapshot.SpentUSD)
	}
}

func TestKillEnableAndEmergencyStop(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)

	manager.KillAgent("agent-a")
	if action := manager.CheckBudget("agent-a", 0); action != ActionKill {
		t.Fatalf("killed action = %q, want %q", action, ActionKill)
	}

	manager.EnableAgent("agent-a")
	if action := manager.CheckBudget("agent-a", 0); action != ActionAllow {
		t.Fatalf("enabled action = %q, want %q", action, ActionAllow)
	}

	manager.SetEmergencyStop(true)
	if !manager.EmergencyStop() {
		t.Fatal("EmergencyStop() = false, want true")
	}
	decision := manager.CheckBudgetDetailed("agent-a", 0)
	if decision.Action != ActionKill || decision.Code != "emergency_stop" {
		t.Fatalf("emergency decision = %#v, want action=kill code=emergency_stop", decision)
	}
	manager.SetEmergencyStop(false)
	if manager.EmergencyStop() {
		t.Fatal("EmergencyStop() = true, want false")
	}
}

func TestRewriteModelForDowngrade(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)

	original := []byte(`{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hello"}]}`)
	rewritten, originalModel, newModel, downgraded, err := manager.RewriteModelForDowngrade("agent-a", original)
	if err != nil {
		t.Fatalf("RewriteModelForDowngrade() error = %v", err)
	}
	if !downgraded {
		t.Fatal("downgraded = false, want true")
	}
	if originalModel != "claude-opus-4-6" || newModel != "claude-sonnet-4-6" {
		t.Fatalf("models = (%q -> %q), want (%q -> %q)", originalModel, newModel, "claude-opus-4-6", "claude-sonnet-4-6")
	}
	if !strings.Contains(string(rewritten), `"model":"claude-sonnet-4-6"`) {
		t.Fatalf("rewritten body = %s, missing downgraded model", string(rewritten))
	}

	_, _, _, downgraded, err = manager.RewriteModelForDowngrade("agent-a", []byte(`{"model":"claude-haiku-4-5"}`))
	if err != nil {
		t.Fatalf("RewriteModelForDowngrade(last) error = %v", err)
	}
	if downgraded {
		t.Fatal("downgraded for last model = true, want false")
	}
}

func TestRecordSpendThresholdAlert(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)

	manager.RecordSpend("agent-a", 4)
	if got := manager.Snapshot("agent-a").LastAlertedPct; got != 0 {
		t.Fatalf("last alerted pct after 40%% = %v, want 0", got)
	}

	manager.RecordSpend("agent-a", 1.5) // crosses 50%
	if got := manager.Snapshot("agent-a").LastAlertedPct; got != 50 {
		t.Fatalf("last alerted pct after crossing 50%% = %v, want 50", got)
	}

	manager.RecordSpend("agent-a", 3.0) // crosses 80%
	if got := manager.Snapshot("agent-a").LastAlertedPct; got != 80 {
		t.Fatalf("last alerted pct after crossing 80%% = %v, want 80", got)
	}
}

func TestConcurrentRecordSpend(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)

	const workers = 20
	const increments = 100
	const amount = 0.01

	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wg.Done()
			for i := 0; i < increments; i++ {
				manager.RecordSpend("agent-a", amount)
			}
		}()
	}
	wg.Wait()

	got := manager.Snapshot("agent-a").SpentUSD
	want := float64(workers * increments)
	want = want * amount
	if got < want-0.00001 || got > want+0.00001 {
		t.Fatalf("concurrent spent = %v, want approximately %v", got, want)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()

	if got := nextInDowngradeChain([]string{"a", "b", "c"}, "a"); got != "b" {
		t.Fatalf("nextInDowngradeChain(a) = %q, want %q", got, "b")
	}
	if got := nextInDowngradeChain([]string{"a", "b"}, "z"); got != "" {
		t.Fatalf("nextInDowngradeChain(z) = %q, want empty", got)
	}
	if got := normalizeAgent("   "); got != "unknown" {
		t.Fatalf("normalizeAgent(space) = %q, want %q", got, "unknown")
	}
	if got := percentageUsed(0, 10); got != 0 {
		t.Fatalf("percentageUsed(limit=0) = %v, want 0", got)
	}
	if got := percentageUsed(10, 5); got != 50 {
		t.Fatalf("percentageUsed(10,5) = %v, want 50", got)
	}

	start := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	if got := nextPeriodReset(start, config.BudgetPeriodWeekly); !got.Equal(start.Add(7 * 24 * time.Hour)) {
		t.Fatalf("nextPeriodReset(weekly) = %v, want %v", got, start.Add(7*24*time.Hour))
	}
	if got := nextPeriodReset(start, config.BudgetPeriodMonthly); !got.Equal(start.AddDate(0, 1, 0)) {
		t.Fatalf("nextPeriodReset(monthly) = %v, want %v", got, start.AddDate(0, 1, 0))
	}

	policy := agentPolicy{
		limitUSD:              10,
		downgradeThresholdPct: 80,
		downgradeChain:        []string{"a", "b"},
		alertThresholdsPct:    []float64{50, 80},
	}
	if !shouldDowngradeForThreshold(policy, 8) {
		t.Fatal("shouldDowngradeForThreshold() = false, want true")
	}
	if shouldDowngradeForThreshold(agentPolicy{}, 9) {
		t.Fatal("shouldDowngradeForThreshold(empty policy) = true, want false")
	}
	if !shouldAlertThreshold(policy, 4, 5.1) {
		t.Fatal("shouldAlertThreshold crossing 50% = false, want true")
	}
	if shouldAlertThreshold(policy, 5.1, 5.2) {
		t.Fatal("shouldAlertThreshold no crossing = true, want false")
	}
}

func TestNewManagerAndExtractAPIKeyFallback(t *testing.T) {
	t.Parallel()

	cfg := baseGateConfig()
	manager := NewManager(cfg, nil)
	if manager == nil {
		t.Fatal("NewManager() returned nil")
	}

	req, err := http.NewRequest(http.MethodPost, "http://example.test/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("x-api-key", "sk-live-from-header")
	if got := extractAPIKey(req); got != "sk-live-from-header" {
		t.Fatalf("extractAPIKey(x-api-key) = %q, want %q", got, "sk-live-from-header")
	}

	decision := manager.CheckBudgetDetailed("agent-real-clock", 0)
	if decision.Action != ActionAllow {
		t.Fatalf("CheckBudgetDetailed() action = %q, want %q", decision.Action, ActionAllow)
	}
}

func TestNewManagerWithClockNilAndAgentOverridePolicy(t *testing.T) {
	t.Parallel()

	cfg := baseGateConfig()
	cfg.DefaultBudget.LimitUSD = 100
	cfg.Agents = []config.AgentBudgetConfig{
		{
			Name:           "agent-override",
			LimitUSD:       1,
			Period:         config.BudgetPeriodDaily,
			ActionOnExceed: config.BudgetActionReject,
		},
	}

	manager := NewManagerWithClock(cfg, nil, nil)
	if manager == nil {
		t.Fatal("NewManagerWithClock(nil clock) returned nil")
	}

	manager.RecordSpend("agent-override", 1)
	decision := manager.CheckBudgetDetailed("agent-override", 0.1)
	if decision.Action != ActionReject {
		t.Fatalf("override action = %q, want %q", decision.Action, ActionReject)
	}

	if action := manager.CheckBudget("other-agent", 0.1); action != ActionAllow {
		t.Fatalf("default policy action = %q, want %q", action, ActionAllow)
	}
}

func TestRewriteModelForDowngrade_Branches(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)

	//nolint:govet // keep branch-focused table fields explicit.
	tests := []struct {
		name          string
		body          string
		manager       *BudgetManager
		wantErr       bool
		wantDowngrade bool
	}{
		{name: "empty body", body: "", manager: manager, wantDowngrade: false},
		{name: "invalid json", body: "{", manager: manager, wantErr: true, wantDowngrade: false},
		{name: "missing model", body: `{"messages":[]}`, manager: manager, wantDowngrade: false},
		{name: "non string model", body: `{"model":1}`, manager: manager, wantDowngrade: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, downgraded, err := tt.manager.RewriteModelForDowngrade("agent-a", []byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatal("RewriteModelForDowngrade() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("RewriteModelForDowngrade() error = %v", err)
			}
			if downgraded != tt.wantDowngrade {
				t.Fatalf("downgraded = %v, want %v", downgraded, tt.wantDowngrade)
			}
		})
	}

	oneChainCfg := baseGateConfig()
	oneChainCfg.DefaultDowngradeChain = []string{"claude-opus-4-6"}
	oneChainManager := NewManagerWithClock(oneChainCfg, nil, clock)
	_, _, _, downgraded, err := oneChainManager.RewriteModelForDowngrade("agent-a", []byte(`{"model":"claude-opus-4-6"}`))
	if err != nil {
		t.Fatalf("RewriteModelForDowngrade(single-chain) error = %v", err)
	}
	if downgraded {
		t.Fatal("downgraded with single chain entry = true, want false")
	}
}

func TestIdentifyAgent_NilRequestAndMalformedAuth(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)
	if got := manager.IdentifyAgent(nil); got != "unknown" {
		t.Fatalf("IdentifyAgent(nil) = %q, want %q", got, "unknown")
	}

	req, err := http.NewRequest(http.MethodPost, "http://example.test/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Token sk-live-abc")
	if got := manager.IdentifyAgent(req); got != "unknown" {
		t.Fatalf("IdentifyAgent(malformed auth) = %q, want %q", got, "unknown")
	}
}

func TestRecordSpend_NegativeIgnored(t *testing.T) {
	t.Parallel()

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(baseGateConfig(), nil, clock)
	manager.RecordSpend("agent-a", 1.0)
	manager.RecordSpend("agent-a", -4.0)

	if spent := manager.Snapshot("agent-a").SpentUSD; spent != 1.0 {
		t.Fatalf("spent after negative record = %v, want 1.0", spent)
	}
}
