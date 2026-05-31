package budget

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

type mockClock struct {
	now time.Time
	mu  sync.RWMutex
}

//nolint:govet // keep mutex first for concurrency-focused helper readability.
type capturingDispatcher struct {
	mu     sync.Mutex
	events []alert.Alert
}

func (d *capturingDispatcher) Dispatch(_ context.Context, event alert.Alert) {
	d.mu.Lock()
	d.events = append(d.events, event)
	d.mu.Unlock()
}

func (d *capturingDispatcher) snapshot() []alert.Alert {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]alert.Alert(nil), d.events...)
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

func TestPeriodResetReEnablesBudgetKilledAgent(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	clock := newMockClock(start)
	cfg := baseGateConfig()
	cfg.DefaultBudget.Period = config.BudgetPeriodHourly
	cfg.DefaultBudget.ActionOnExceed = config.BudgetActionKill

	manager := NewManagerWithClock(cfg, nil, clock)
	manager.RecordSpend("agent-a", 0.5)

	decision := manager.CheckBudgetDetailed("agent-a", 9.6)
	if decision.Action != ActionKill {
		t.Fatalf("CheckBudgetDetailed().Action = %q, want %q", decision.Action, ActionKill)
	}
	if got := manager.GetBudget("agent-a").Status; got != "killed" {
		t.Fatalf("GetBudget().Status = %q, want %q", got, "killed")
	}

	clock.Advance(61 * time.Minute)
	reset := manager.GetBudget("agent-a")
	if reset.Status != "active" {
		t.Fatalf("GetBudget() after period reset status = %q, want %q", reset.Status, "active")
	}
	if reset.SpentUSD != 0 {
		t.Fatalf("GetBudget() after period reset spent = %v, want 0", reset.SpentUSD)
	}
	if action := manager.CheckBudget("agent-a", 0); action != ActionAllow {
		t.Fatalf("CheckBudget() after period reset = %q, want %q", action, ActionAllow)
	}
}

func TestPeriodResetDoesNotReEnableManualKilledAgent(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	clock := newMockClock(start)
	cfg := baseGateConfig()
	cfg.DefaultBudget.Period = config.BudgetPeriodHourly

	manager := NewManagerWithClock(cfg, nil, clock)
	manager.KillAgent("agent-a")

	clock.Advance(61 * time.Minute)
	if got := manager.GetBudget("agent-a").Status; got != "killed" {
		t.Fatalf("GetBudget() after period reset status = %q, want %q", got, "killed")
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
	if decision.Action != ActionAllow {
		t.Fatalf("emergency decision = %#v, want action=allow", decision)
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

func TestSeedConfiguredAgentsAndPersistentFlush(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		recordSpend float64
	}{
		{name: "seed writes configured agent", recordSpend: 0},
		{name: "flush persists spend after mutation", recordSpend: 3.25},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := baseGateConfig()
			cfg.Agents = []config.AgentBudgetConfig{
				{
					Name:           "email-agent",
					LimitUSD:       12,
					Period:         config.BudgetPeriodWeekly,
					ActionOnExceed: config.BudgetActionDowngrade,
					DowngradeChain: []string{"claude-opus-4-6", "claude-sonnet-4-6"},
				},
			}

			store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "budget.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() {
				_ = store.Close()
			})

			if seedErr := SeedConfiguredAgents(context.Background(), cfg, store, nil); seedErr != nil {
				t.Fatalf("SeedConfiguredAgents() error = %v", seedErr)
			}

			record, found, err := store.GetAgent(context.Background(), "email-agent")
			if err != nil {
				t.Fatalf("GetAgent() error = %v", err)
			}
			if !found {
				t.Fatal("GetAgent() found = false, want true")
			}
			if record.BudgetLimitUSD != 12 {
				t.Fatalf("BudgetLimitUSD = %v, want 12", record.BudgetLimitUSD)
			}
			if record.BudgetPeriod != config.BudgetPeriodWeekly {
				t.Fatalf("BudgetPeriod = %q, want %q", record.BudgetPeriod, config.BudgetPeriodWeekly)
			}

			manager, err := NewPersistentManager(cfg, nil, store)
			if err != nil {
				t.Fatalf("NewPersistentManager() error = %v", err)
			}
			t.Cleanup(func() {
				_ = manager.Close()
			})

			if tt.recordSpend > 0 {
				manager.RecordSpend("email-agent", tt.recordSpend)
				if flushErr := manager.Flush(context.Background()); flushErr != nil {
					t.Fatalf("Flush() error = %v", flushErr)
				}

				record, found, err = store.GetAgent(context.Background(), "email-agent")
				if err != nil {
					t.Fatalf("GetAgent(after flush) error = %v", err)
				}
				if !found {
					t.Fatal("GetAgent(after flush) found = false, want true")
				}
				if record.BudgetSpentUSD != tt.recordSpend {
					t.Fatalf("BudgetSpentUSD = %v, want %v", record.BudgetSpentUSD, tt.recordSpend)
				}
			}
		})
	}
}

func TestNewPersistentManager_DoesNotPersistConfiguredAgentsWithoutTraffic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentName string
	}{
		{
			name:      "configured agent stays absent until first traffic",
			agentName: "email-agent",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := baseGateConfig()
			cfg.Agents = []config.AgentBudgetConfig{
				{
					Name:           tt.agentName,
					LimitUSD:       12,
					Period:         config.BudgetPeriodWeekly,
					ActionOnExceed: config.BudgetActionDowngrade,
					DowngradeChain: []string{"claude-opus-4-6", "claude-sonnet-4-6"},
				},
			}

			store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "budget.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() {
				_ = store.Close()
			})

			manager, err := NewPersistentManager(cfg, nil, store)
			if err != nil {
				t.Fatalf("NewPersistentManager() error = %v", err)
			}
			t.Cleanup(func() {
				_ = manager.Close()
			})

			if flushErr := manager.Flush(context.Background()); flushErr != nil {
				t.Fatalf("Flush() error = %v", flushErr)
			}

			record, found, err := store.GetAgent(context.Background(), tt.agentName)
			if err != nil {
				t.Fatalf("GetAgent() error = %v", err)
			}
			if found {
				t.Fatalf("GetAgent() found = true, want false, record = %#v", record)
			}

			manager.RecordSpend(tt.agentName, 1.25)
			if flushErr := manager.Flush(context.Background()); flushErr != nil {
				t.Fatalf("Flush(after traffic) error = %v", flushErr)
			}

			record, found, err = store.GetAgent(context.Background(), tt.agentName)
			if err != nil {
				t.Fatalf("GetAgent(after traffic) error = %v", err)
			}
			if !found {
				t.Fatal("GetAgent(after traffic) found = false, want true")
			}
			if record.BudgetLimitUSD != 12 {
				t.Fatalf("BudgetLimitUSD = %v, want 12", record.BudgetLimitUSD)
			}
			if record.BudgetSpentUSD != 1.25 {
				t.Fatalf("BudgetSpentUSD = %v, want 1.25", record.BudgetSpentUSD)
			}
		})
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

func TestBudgetManager_DispatchesThresholdAndKillAlerts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*config.GateConfig)
		prepare   func(*BudgetManager)
		act       func(*BudgetManager)
		wantTypes []alert.Type
	}{
		{
			name: "record spend crossing threshold dispatches budget_threshold",
			prepare: func(manager *BudgetManager) {
				manager.RecordSpend("agent-a", 4.9)
			},
			act: func(manager *BudgetManager) {
				manager.RecordSpend("agent-a", 0.2) // cross 50%
			},
			wantTypes: []alert.Type{alert.TypeBudgetThreshold},
		},
		{
			name: "over-limit kill dispatches budget_exceeded and agent_killed",
			setup: func(cfg *config.GateConfig) {
				cfg.DefaultBudget.ActionOnExceed = config.BudgetActionKill
				cfg.DefaultBudget.LimitUSD = 1
			},
			act: func(manager *BudgetManager) {
				_ = manager.CheckBudgetDetailed("agent-a", 2)
			},
			wantTypes: []alert.Type{alert.TypeBudgetExceeded, alert.TypeAgentKilled},
		},
		{
			name: "runaway detection dispatches runaway and killed alerts",
			setup: func(cfg *config.GateConfig) {
				cfg.Runaway.Enabled = true
				cfg.Runaway.MaxRequests = 1
				cfg.Runaway.WindowSeconds = 60
			},
			prepare: func(manager *BudgetManager) {
				_ = manager.CheckBudget("agent-a", 0)
			},
			act: func(manager *BudgetManager) {
				_ = manager.CheckBudget("agent-a", 0)
			},
			wantTypes: []alert.Type{alert.TypeRunawayDetected, alert.TypeAgentKilled},
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
			dispatcher := &capturingDispatcher{}
			manager := NewManagerWithClockAndDispatcher(cfg, nil, clock, dispatcher)

			if tt.prepare != nil {
				tt.prepare(manager)
			}
			tt.act(manager)

			events := dispatcher.snapshot()
			if len(events) != len(tt.wantTypes) {
				t.Fatalf("alert count = %d, want %d", len(events), len(tt.wantTypes))
			}
			for i := range tt.wantTypes {
				if events[i].Type != tt.wantTypes[i] {
					t.Fatalf("alert[%d].Type = %q, want %q", i, events[i].Type, tt.wantTypes[i])
				}
			}
		})
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

func TestRenameAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		oldName   string
		newName   string
		wantError bool
	}{
		{name: "same name is no-op", oldName: "agent-a", newName: "agent-a", wantError: false},
		{name: "rename to new name succeeds", oldName: "agent-a", newName: "agent-renamed", wantError: false},
		{name: "rename to existing name fails", oldName: "agent-a", newName: "agent-b", wantError: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := baseGateConfig()
			store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "rename.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
			manager, err := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store, clock, nil)
			if err != nil {
				t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", err)
			}
			t.Cleanup(func() { _ = manager.Close() })

			manager.RecordSpend("agent-a", 2.5)
			manager.RecordSpend("agent-b", 1.0)
			if flushErr := manager.Flush(context.Background()); flushErr != nil {
				t.Fatalf("Flush() error = %v", flushErr)
			}

			if tt.oldName == tt.newName {
				if renameErr := manager.RenameAgent(context.Background(), tt.oldName, tt.newName); renameErr != nil {
					t.Fatalf("RenameAgent(same) error = %v", renameErr)
				}
				return
			}

			renameErr := manager.RenameAgent(context.Background(), tt.oldName, tt.newName)
			if tt.wantError {
				if renameErr == nil {
					t.Fatal("RenameAgent() error = nil, want non-nil")
				}
				return
			}
			if renameErr != nil {
				t.Fatalf("RenameAgent() error = %v", renameErr)
			}

			view := manager.GetBudget(tt.newName)
			if view.SpentUSD != 2.5 {
				t.Fatalf("renamed agent spent = %v, want 2.5", view.SpentUSD)
			}
			oldView := manager.GetBudget(tt.oldName)
			if oldView.SpentUSD != 0 {
				t.Fatalf("old agent spent = %v, want 0", oldView.SpentUSD)
			}
		})
	}
}

func TestCloneState(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table readable.
	tests := []struct {
		name  string
		state *agentState
	}{
		{name: "nil state", state: nil},
		{
			name: "populated state",
			state: &agentState{
				spentUSD:        5.0,
				lastAlertedPct:  50,
				periodStartedAt: time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC),
				periodResetsAt:  time.Date(2026, time.March, 27, 12, 0, 0, 0, time.UTC),
				lastSeenAt:      time.Date(2026, time.March, 26, 13, 0, 0, 0, time.UTC),
				requestTimes:    []time.Time{time.Date(2026, time.March, 26, 12, 30, 0, 0, time.UTC)},
				triggeredAlerts: map[float64]bool{50: true},
				killed:          true,
				disableReason:   disableReasonManualKill,
				dirty:           true,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cloned := cloneState(tt.state)
			if cloned == nil {
				t.Fatal("cloneState() returned nil")
			}
			if tt.state == nil {
				if len(cloned.triggeredAlerts) != 0 {
					t.Fatalf("cloneState(nil).triggeredAlerts len = %d, want 0", len(cloned.triggeredAlerts))
				}
				return
			}
			if cloned.spentUSD != tt.state.spentUSD {
				t.Fatalf("cloned.spentUSD = %v, want %v", cloned.spentUSD, tt.state.spentUSD)
			}
			if cloned.killed != tt.state.killed {
				t.Fatalf("cloned.killed = %v, want %v", cloned.killed, tt.state.killed)
			}
			if cloned.disableReason != tt.state.disableReason {
				t.Fatalf("cloned.disableReason = %q, want %q", cloned.disableReason, tt.state.disableReason)
			}
			// Verify deep copy: mutate original, cloned should not change.
			tt.state.triggeredAlerts[80] = true
			if _, found := cloned.triggeredAlerts[80]; found {
				t.Fatal("cloned triggeredAlerts shares reference with original")
			}
		})
	}
}

func TestPersistedDisableReason(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table readable.
	tests := []struct {
		name           string
		status         string
		limitUSD       float64
		spentUSD       float64
		actionOnExceed config.BudgetAction
		want           string
	}{
		{name: "budget_exceeded status", status: "budget_exceeded", want: disableReasonBudgetExceeded},
		{name: "manual_kill status", status: "manual_kill", want: disableReasonManualKill},
		{name: "runaway_detected status", status: "runaway_detected", want: disableReasonRunaway},
		{name: "killed with budget exceeded", status: "killed", limitUSD: 10, spentUSD: 10, actionOnExceed: config.BudgetActionKill, want: disableReasonBudgetExceeded},
		{name: "killed manual", status: "killed", limitUSD: 10, spentUSD: 5, actionOnExceed: config.BudgetActionKill, want: disableReasonManualKill},
		{name: "active status", status: "active", want: disableReasonNone},
		{name: "empty status", status: "", want: disableReasonNone},
		{name: "unknown status", status: "something_else", want: disableReasonNone},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := persistedDisableReason(tt.status, tt.limitUSD, tt.spentUSD, tt.actionOnExceed)
			if got != tt.want {
				t.Fatalf("persistedDisableReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPersistedStatusForState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state *agentState
		want  string
	}{
		{name: "nil state", state: nil, want: "active"},
		{name: "not killed", state: &agentState{killed: false}, want: "active"},
		{name: "killed with reason", state: &agentState{killed: true, disableReason: disableReasonRunaway}, want: disableReasonRunaway},
		{name: "killed no reason", state: &agentState{killed: true, disableReason: ""}, want: "killed"},
		{name: "killed whitespace reason", state: &agentState{killed: true, disableReason: "  "}, want: "killed"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := persistedStatusForState(tt.state)
			if got != tt.want {
				t.Fatalf("persistedStatusForState() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFirstNonZeroTime(t *testing.T) {
	t.Parallel()

	fallback := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	value := time.Date(2026, time.March, 25, 6, 0, 0, 0, time.UTC)

	//nolint:govet // keep test table readable.
	tests := []struct {
		name     string
		value    time.Time
		fallback time.Time
		want     time.Time
	}{
		{name: "zero value uses fallback", value: time.Time{}, fallback: fallback, want: fallback.UTC()},
		{name: "non-zero value used", value: value, fallback: fallback, want: value.UTC()},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := firstNonZeroTime(tt.value, tt.fallback)
			if !got.Equal(tt.want) {
				t.Fatalf("firstNonZeroTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTriggeredAlerts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		thresholds []float64
		limitUSD   float64
		spentUSD   float64
		wantFired  int
	}{
		{name: "no thresholds", thresholds: nil, limitUSD: 10, spentUSD: 5, wantFired: 0},
		{name: "none crossed", thresholds: []float64{50, 80}, limitUSD: 10, spentUSD: 4, wantFired: 0},
		{name: "one crossed", thresholds: []float64{50, 80}, limitUSD: 10, spentUSD: 6, wantFired: 1},
		{name: "all crossed", thresholds: []float64{50, 80, 100}, limitUSD: 10, spentUSD: 10, wantFired: 3},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := triggeredAlerts(tt.thresholds, tt.limitUSD, tt.spentUSD)
			if len(got) != tt.wantFired {
				t.Fatalf("triggeredAlerts() fired = %d, want %d", len(got), tt.wantFired)
			}
		})
	}
}

func TestToBudgetView_NegativeRemaining(t *testing.T) {
	t.Parallel()

	policy := agentPolicy{limitUSD: 5, period: config.BudgetPeriodDaily, actionOnExceed: config.BudgetActionReject}
	state := &agentState{spentUSD: 8, triggeredAlerts: make(map[float64]bool)}

	view := toBudgetView("over-agent", policy, state)
	if view.RemainingUSD != 0 {
		t.Fatalf("toBudgetView().RemainingUSD = %v, want 0", view.RemainingUSD)
	}
	if view.Status != "active" {
		t.Fatalf("toBudgetView().Status = %q, want %q", view.Status, "active")
	}
}

func TestPersistentManagerWithClockAndDispatcher(t *testing.T) {
	t.Parallel()

	cfg := baseGateConfig()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "dispatch.db"), 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	dispatcher := &capturingDispatcher{}
	manager, err := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store, clock, dispatcher)
	if err != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	manager.RecordSpend("agent-a", 5.1) // 51% — crosses 50% threshold
	events := dispatcher.snapshot()
	foundThreshold := false
	for _, e := range events {
		if e.Type == alert.TypeBudgetThreshold {
			foundThreshold = true
		}
	}
	if !foundThreshold {
		t.Fatal("expected budget_threshold alert, got none")
	}

	if flushErr := manager.Flush(context.Background()); flushErr != nil {
		t.Fatalf("Flush() error = %v", flushErr)
	}
	record, found, getErr := store.GetAgent(context.Background(), "agent-a")
	if getErr != nil {
		t.Fatalf("GetAgent() error = %v", getErr)
	}
	if !found {
		t.Fatal("GetAgent() found = false, want true")
	}
	if record.BudgetSpentUSD != 5.1 {
		t.Fatalf("persisted BudgetSpentUSD = %v, want 5.1", record.BudgetSpentUSD)
	}
}

func TestLoadPersistedAgents_RestoresState(t *testing.T) {
	t.Parallel()

	cfg := baseGateConfig()
	dbPath := filepath.Join(t.TempDir(), "load.db")
	store, err := storage.NewSQLiteStore(dbPath, 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))

	// Create first manager, add state, flush, close.
	m1, err := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store, clock, nil)
	if err != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher(m1) error = %v", err)
	}
	m1.RecordSpend("persisted-agent", 7.5)
	m1.KillAgent("killed-agent")
	if flushErr := m1.Flush(context.Background()); flushErr != nil {
		t.Fatalf("m1.Flush() error = %v", flushErr)
	}
	_ = m1.Close()
	_ = store.Close()

	// Reopen store and create new manager — should load persisted state.
	store2, err := storage.NewSQLiteStore(dbPath, 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen) error = %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	m2, err := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store2, clock, nil)
	if err != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher(m2) error = %v", err)
	}
	t.Cleanup(func() { _ = m2.Close() })

	view := m2.GetBudget("persisted-agent")
	if view.SpentUSD != 7.5 {
		t.Fatalf("loaded SpentUSD = %v, want 7.5", view.SpentUSD)
	}

	killedView := m2.GetBudget("killed-agent")
	if killedView.Status != "killed" {
		t.Fatalf("loaded killed agent status = %q, want %q", killedView.Status, "killed")
	}
}

func TestFlushAgentIfNeeded_EmptyAgent(t *testing.T) {
	t.Parallel()

	cfg := baseGateConfig()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "flush-empty.db"), 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	manager, err := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store, clock, nil)
	if err != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	// Should not panic or error for empty/whitespace agent.
	manager.flushAgentIfNeeded("")
	manager.flushAgentIfNeeded("   ")
}

func TestBudgetViewsAndMutations(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	clock := newMockClock(start)
	cfg := baseGateConfig()
	cfg.Agents = []config.AgentBudgetConfig{
		{
			Name:           "configured-agent",
			LimitUSD:       20,
			Period:         config.BudgetPeriodWeekly,
			ActionOnExceed: config.BudgetActionAlert,
		},
	}
	manager := NewManagerWithClock(cfg, nil, clock)

	manager.RecordSpend("agent-a", 3)
	manager.KillAgent("agent-b")

	views := manager.ListBudgets()
	if len(views) < 3 {
		t.Fatalf("ListBudgets() len = %d, want at least 3", len(views))
	}

	agentA := manager.GetBudget("agent-a")
	if agentA.Agent != "agent-a" {
		t.Fatalf("GetBudget(agent-a).Agent = %q, want %q", agentA.Agent, "agent-a")
	}
	if agentA.SpentUSD != 3 {
		t.Fatalf("GetBudget(agent-a).SpentUSD = %v, want 3", agentA.SpentUSD)
	}
	if agentA.Status != "active" {
		t.Fatalf("GetBudget(agent-a).Status = %q, want %q", agentA.Status, "active")
	}
	if agentA.RemainingUSD != 7 {
		t.Fatalf("GetBudget(agent-a).RemainingUSD = %v, want 7", agentA.RemainingUSD)
	}
	if agentA.PercentageUsed != 30 {
		t.Fatalf("GetBudget(agent-a).PercentageUsed = %v, want 30", agentA.PercentageUsed)
	}

	agentB := manager.GetBudget("agent-b")
	if agentB.Status != "killed" {
		t.Fatalf("GetBudget(agent-b).Status = %q, want %q", agentB.Status, "killed")
	}
	if !agentB.PeriodResetsAt.Equal(start.Add(24 * time.Hour)) {
		t.Fatalf("GetBudget(agent-b).PeriodResetsAt = %v, want %v", agentB.PeriodResetsAt, start.Add(24*time.Hour))
	}

	updateTests := []struct {
		name      string
		update    BudgetUpdate
		wantError bool
	}{
		{
			name: "reject negative limit",
			update: BudgetUpdate{
				LimitUSD:              -1,
				Period:                config.BudgetPeriodDaily,
				ActionOnExceed:        config.BudgetActionAlert,
				DowngradeThresholdPct: 80,
				DowngradeChain:        []string{"a", "b"},
				AlertThresholdsPct:    []float64{50, 80},
			},
			wantError: true,
		},
		{
			name: "reject threshold over 100",
			update: BudgetUpdate{
				LimitUSD:              10,
				Period:                config.BudgetPeriodDaily,
				ActionOnExceed:        config.BudgetActionAlert,
				DowngradeThresholdPct: 101,
				DowngradeChain:        []string{"a", "b"},
				AlertThresholdsPct:    []float64{50, 80},
			},
			wantError: true,
		},
		{
			name: "apply valid update",
			update: BudgetUpdate{
				LimitUSD:              25,
				Period:                config.BudgetPeriodWeekly,
				ActionOnExceed:        config.BudgetActionReject,
				DowngradeThresholdPct: 70,
				DowngradeChain:        []string{"m1", "m2"},
				AlertThresholdsPct:    []float64{40, 70, 100},
			},
			wantError: false,
		},
	}

	for _, tt := range updateTests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := manager.UpdateBudget("agent-a", tt.update)
			if tt.wantError {
				if err == nil {
					t.Fatal("UpdateBudget() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateBudget() error = %v", err)
			}
		})
	}

	updated := manager.GetBudget("agent-a")
	if updated.LimitUSD != 25 {
		t.Fatalf("updated limit = %v, want 25", updated.LimitUSD)
	}
	if updated.ActionOnExceed != config.BudgetActionReject {
		t.Fatalf("updated action = %q, want %q", updated.ActionOnExceed, config.BudgetActionReject)
	}
	if updated.Period != config.BudgetPeriodWeekly {
		t.Fatalf("updated period = %q, want %q", updated.Period, config.BudgetPeriodWeekly)
	}

	manager.ResetBudget("agent-a")
	reset := manager.GetBudget("agent-a")
	if reset.SpentUSD != 0 {
		t.Fatalf("ResetBudget spent = %v, want 0", reset.SpentUSD)
	}
	if reset.PercentageUsed != 0 {
		t.Fatalf("ResetBudget percentage = %v, want 0", reset.PercentageUsed)
	}
	if !reset.PeriodResetsAt.Equal(start.Add(7 * 24 * time.Hour)) {
		t.Fatalf("ResetBudget period reset = %v, want %v", reset.PeriodResetsAt, start.Add(7*24*time.Hour))
	}
}

func TestGlobalBudgetNotConfigured(t *testing.T) {
	cfg := baseGateConfig()
	cfg.DefaultBudget.LimitUSD = 100
	cfg.GlobalBudget.LimitUSD = 0

	m := NewManagerWithClock(cfg, nil, newMockClock(time.Now()))

	decision := m.CheckBudgetDetailed("agent-x", 5.00)
	if decision.Action != ActionAllow {
		t.Fatalf("want ActionAllow when global budget not configured, got %s (code=%s)", decision.Action, decision.Code)
	}
}

func TestGlobalBudgetUnderLimit(t *testing.T) {
	cfg := baseGateConfig()
	cfg.DefaultBudget.LimitUSD = 100
	cfg.GlobalBudget.LimitUSD = 10
	cfg.GlobalBudget.Period = config.BudgetPeriodDaily

	clk := newMockClock(time.Now())
	m := NewManagerWithClock(cfg, nil, clk)

	decision := m.CheckBudgetDetailed("agent-a", 5)
	if decision.Action != ActionAllow {
		t.Fatalf("want ActionAllow when under global limit, got %s", decision.Action)
	}
}

func TestGlobalBudgetExceededRejectsAllAgents(t *testing.T) {
	cfg := baseGateConfig()
	cfg.DefaultBudget.LimitUSD = 100
	cfg.GlobalBudget.LimitUSD = 1.00
	cfg.GlobalBudget.Period = config.BudgetPeriodDaily

	clk := newMockClock(time.Now())
	m := NewManagerWithClock(cfg, nil, clk)

	m.RecordSpend("agent-a", 0.80)
	m.RecordSpend("agent-b", 0.25)

	for _, agent := range []string{"agent-a", "agent-b", "agent-c"} {
		d := m.CheckBudgetDetailed(agent, 0.01)
		if d.Action != ActionReject {
			t.Fatalf("agent %q: want ActionReject after global exceeded, got %s (code=%s)", agent, d.Action, d.Code)
		}
		if d.Code != "global_budget_exceeded" {
			t.Fatalf("agent %q: want code global_budget_exceeded, got %s", agent, d.Code)
		}
	}
}

func TestGlobalBudgetCheckedBeforePerAgent(t *testing.T) {
	cfg := baseGateConfig()
	cfg.DefaultBudget.LimitUSD = 0
	cfg.GlobalBudget.LimitUSD = 0.50
	cfg.GlobalBudget.Period = config.BudgetPeriodDaily

	clk := newMockClock(time.Now())
	m := NewManagerWithClock(cfg, nil, clk)

	m.RecordSpend("agent-a", 0.60)

	d := m.CheckBudgetDetailed("agent-a", 0.01)
	if d.Action != ActionReject || d.Code != "global_budget_exceeded" {
		t.Fatalf("want global_budget_exceeded before per-agent check, got action=%s code=%s", d.Action, d.Code)
	}
}

func TestGlobalBudgetPeriodReset(t *testing.T) {
	cfg := baseGateConfig()
	cfg.DefaultBudget.LimitUSD = 100
	cfg.GlobalBudget.LimitUSD = 1.00
	cfg.GlobalBudget.Period = config.BudgetPeriodHourly

	start := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := newMockClock(start)
	m := NewManagerWithClock(cfg, nil, clk)

	m.RecordSpend("agent-a", 0.90)
	m.RecordSpend("agent-b", 0.15)

	d := m.CheckBudgetDetailed("agent-a", 0.01)
	if d.Action != ActionReject {
		t.Fatalf("want reject before period reset, got %s", d.Action)
	}

	clk.Advance(61 * time.Minute)

	d = m.CheckBudgetDetailed("agent-a", 0.01)
	if d.Action != ActionAllow {
		t.Fatalf("want allow after global period reset, got %s (code=%s)", d.Action, d.Code)
	}
}

func TestGetGlobalBudgetView(t *testing.T) {
	cfg := baseGateConfig()
	cfg.GlobalBudget.LimitUSD = 5.00
	cfg.GlobalBudget.Period = config.BudgetPeriodMonthly

	clk := newMockClock(time.Now())
	m := NewManagerWithClock(cfg, nil, clk)

	m.RecordSpend("agent-a", 2.00)
	m.RecordSpend("agent-b", 1.50)

	gv := m.GetGlobalBudget()
	if gv.LimitUSD != 5.00 {
		t.Fatalf("limit = %v, want 5.00", gv.LimitUSD)
	}
	if gv.SpentUSD != 3.50 {
		t.Fatalf("spent = %v, want 3.50", gv.SpentUSD)
	}
	if gv.RemainingUSD != 1.50 {
		t.Fatalf("remaining = %v, want 1.50", gv.RemainingUSD)
	}
	if gv.PercentageUsed != 70.0 {
		t.Fatalf("pct = %v, want 70.0", gv.PercentageUsed)
	}
	if gv.Period != config.BudgetPeriodMonthly {
		t.Fatalf("period = %q, want monthly", gv.Period)
	}
}
