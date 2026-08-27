package budget

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/config"
)

// runawayStep is one CheckBudget call in a scripted runaway scenario.
// advance moves the mock clock before the call; enable re-enables the agent
// before the call.
type runawayStep struct {
	agent   string
	want    Action
	advance time.Duration
	enable  bool
}

func runawayGateConfig(enabled bool, maxRequests, windowSeconds int) config.GateConfig {
	cfg := baseGateConfig()
	cfg.Runaway.Enabled = enabled
	cfg.Runaway.MaxRequests = maxRequests
	cfg.Runaway.WindowSeconds = windowSeconds
	return cfg
}

func repeatSteps(agent string, count int, want Action) []runawayStep {
	steps := make([]runawayStep, 0, count)
	for i := 0; i < count; i++ {
		steps = append(steps, runawayStep{agent: agent, want: want})
	}
	return steps
}

func concatSteps(groups ...[]runawayStep) []runawayStep {
	var steps []runawayStep
	for _, group := range groups {
		steps = append(steps, group...)
	}
	return steps
}

func countAlerts(events []alert.Alert, agent string, alertType alert.Type) int {
	count := 0
	for _, event := range events {
		if event.Agent == agent && event.Type == alertType {
			count++
		}
	}
	return count
}

func TestRunawayDetection_SlidingWindow(t *testing.T) {
	t.Parallel()

	const window = 60

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name           string
		cfg            config.GateConfig
		steps          []runawayStep
		wantKilled     map[string]bool
		wantAlertPairs map[string]int
	}{
		{
			name: "exactly max requests inside the window are allowed",
			cfg:  runawayGateConfig(true, 3, window),
			steps: concatSteps(
				repeatSteps("agent-a", 3, ActionAllow),
			),
			wantKilled:     map[string]bool{"agent-a": false},
			wantAlertPairs: map[string]int{"agent-a": 0},
		},
		{
			name: "max plus one request inside the window kills the agent",
			cfg:  runawayGateConfig(true, 3, window),
			steps: concatSteps(
				repeatSteps("agent-a", 3, ActionAllow),
				repeatSteps("agent-a", 1, ActionKill),
			),
			wantKilled:     map[string]bool{"agent-a": true},
			wantAlertPairs: map[string]int{"agent-a": 1},
		},
		{
			name:           "disabled detector never kills even far above the limit",
			cfg:            runawayGateConfig(false, 1, window),
			steps:          repeatSteps("agent-a", 25, ActionAllow),
			wantKilled:     map[string]bool{"agent-a": false},
			wantAlertPairs: map[string]int{"agent-a": 0},
		},
		{
			name:           "enabled detector with zero max_requests is inactive",
			cfg:            runawayGateConfig(true, 0, window),
			steps:          repeatSteps("agent-a", 25, ActionAllow),
			wantKilled:     map[string]bool{"agent-a": false},
			wantAlertPairs: map[string]int{"agent-a": 0},
		},
		{
			name:           "enabled detector with zero window_seconds is inactive",
			cfg:            runawayGateConfig(true, 1, 0),
			steps:          repeatSteps("agent-a", 25, ActionAllow),
			wantKilled:     map[string]bool{"agent-a": false},
			wantAlertPairs: map[string]int{"agent-a": 0},
		},
		{
			name: "independent agents keep separate windows",
			cfg:  runawayGateConfig(true, 2, window),
			steps: concatSteps(
				repeatSteps("agent-a", 2, ActionAllow),
				repeatSteps("agent-b", 2, ActionAllow),
				repeatSteps("agent-a", 1, ActionKill),
				repeatSteps("agent-b", 1, ActionKill),
				repeatSteps("agent-c", 2, ActionAllow),
			),
			wantKilled:     map[string]bool{"agent-a": true, "agent-b": true, "agent-c": false},
			wantAlertPairs: map[string]int{"agent-a": 1, "agent-b": 1, "agent-c": 0},
		},
		{
			name: "entry exactly window_seconds old still counts toward the limit",
			cfg:  runawayGateConfig(true, 2, window),
			steps: []runawayStep{
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", advance: time.Second, want: ActionAllow},
				// now == first entry + window: the first entry sits on the boundary and is kept.
				{agent: "agent-a", advance: (window - 1) * time.Second, want: ActionKill},
			},
			wantKilled:     map[string]bool{"agent-a": true},
			wantAlertPairs: map[string]int{"agent-a": 1},
		},
		{
			name: "entry one nanosecond older than the window is dropped",
			cfg:  runawayGateConfig(true, 2, window),
			steps: []runawayStep{
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", advance: time.Second, want: ActionAllow},
				// now == first entry + window + 1ns: the first entry expires, so only two remain.
				{agent: "agent-a", advance: (window-1)*time.Second + time.Nanosecond, want: ActionAllow},
				{agent: "agent-a", want: ActionKill},
			},
			wantKilled:     map[string]bool{"agent-a": true},
			wantAlertPairs: map[string]int{"agent-a": 1},
		},
		{
			name: "old entries expire and free the whole window again",
			cfg:  runawayGateConfig(true, 2, window),
			steps: []runawayStep{
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", advance: (window + 1) * time.Second, want: ActionAllow},
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", want: ActionKill},
			},
			wantKilled:     map[string]bool{"agent-a": true},
			wantAlertPairs: map[string]int{"agent-a": 1},
		},
		{
			name: "kill stays sticky after the window and repeated checks alert only once",
			cfg:  runawayGateConfig(true, 1, window),
			steps: []runawayStep{
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", want: ActionKill},
				{agent: "agent-a", advance: 2 * window * time.Second, want: ActionKill},
				{agent: "agent-a", advance: 24 * time.Hour, want: ActionKill},
				{agent: "agent-a", want: ActionKill},
			},
			wantKilled:     map[string]bool{"agent-a": true},
			wantAlertPairs: map[string]int{"agent-a": 1},
		},
		{
			name: "manual enable after the window restores traffic",
			cfg:  runawayGateConfig(true, 2, window),
			steps: []runawayStep{
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", want: ActionKill},
				{agent: "agent-a", advance: (window + 1) * time.Second, want: ActionKill},
				{agent: "agent-a", enable: true, want: ActionAllow},
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", want: ActionKill},
			},
			wantKilled:     map[string]bool{"agent-a": true},
			wantAlertPairs: map[string]int{"agent-a": 2},
		},
		{
			name: "manual enable does not disturb other agents",
			cfg:  runawayGateConfig(true, 1, window),
			steps: []runawayStep{
				{agent: "agent-a", want: ActionAllow},
				{agent: "agent-a", want: ActionKill},
				{agent: "agent-b", want: ActionAllow},
				{agent: "agent-b", want: ActionKill},
				{agent: "agent-a", advance: (window + 1) * time.Second, enable: true, want: ActionAllow},
				{agent: "agent-b", want: ActionKill},
			},
			wantKilled:     map[string]bool{"agent-a": false, "agent-b": true},
			wantAlertPairs: map[string]int{"agent-a": 1, "agent-b": 1},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
			dispatcher := &capturingDispatcher{}
			manager := NewManagerWithClockAndDispatcher(tt.cfg, nil, clock, dispatcher)

			for i, step := range tt.steps {
				clock.Advance(step.advance)
				if step.enable {
					manager.EnableAgent(step.agent)
				}
				if got := manager.CheckBudget(step.agent, 0); got != step.want {
					t.Fatalf("step %d (%s): action = %q, want %q", i, step.agent, got, step.want)
				}
			}

			for agent, wantKilled := range tt.wantKilled {
				if got := manager.Snapshot(agent).Killed; got != wantKilled {
					t.Fatalf("Snapshot(%q).Killed = %v, want %v", agent, got, wantKilled)
				}
			}

			events := dispatcher.snapshot()
			for agent, wantPairs := range tt.wantAlertPairs {
				if got := countAlerts(events, agent, alert.TypeRunawayDetected); got != wantPairs {
					t.Fatalf("runaway_detected alerts for %q = %d, want %d", agent, got, wantPairs)
				}
				if got := countAlerts(events, agent, alert.TypeAgentKilled); got != wantPairs {
					t.Fatalf("agent_killed alerts for %q = %d, want %d", agent, got, wantPairs)
				}
			}
		})
	}
}

func TestRunawayDetection_KillDecisionAndAlertPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		wantMessagePart string
		maxRequests     int
		windowSeconds   int
	}{
		{name: "single request limit", maxRequests: 1, windowSeconds: 30, wantMessagePart: "runaway request volume"},
		{name: "larger limit", maxRequests: 5, windowSeconds: 120, wantMessagePart: "runaway request volume"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
			dispatcher := &capturingDispatcher{}
			manager := NewManagerWithClockAndDispatcher(runawayGateConfig(true, tt.maxRequests, tt.windowSeconds), nil, clock, dispatcher)

			for i := 0; i < tt.maxRequests; i++ {
				if got := manager.CheckBudgetDetailed("agent-a", 0); got.Action != ActionAllow {
					t.Fatalf("request %d action = %q, want %q", i+1, got.Action, ActionAllow)
				}
			}

			decision := manager.CheckBudgetDetailed("agent-a", 0)
			if decision.Action != ActionKill {
				t.Fatalf("kill decision action = %q, want %q", decision.Action, ActionKill)
			}
			if decision.Code != "agent_killed" {
				t.Fatalf("kill decision code = %q, want agent_killed", decision.Code)
			}
			if decision.Agent != "agent-a" {
				t.Fatalf("kill decision agent = %q, want agent-a", decision.Agent)
			}
			if !strings.Contains(decision.Message, tt.wantMessagePart) {
				t.Fatalf("kill decision message = %q, want it to mention %q", decision.Message, tt.wantMessagePart)
			}

			// A killed agent is reported with the generic disabled message on later checks.
			sticky := manager.CheckBudgetDetailed("agent-a", 0)
			if sticky.Action != ActionKill || sticky.Code != "agent_killed" {
				t.Fatalf("sticky decision = %+v, want kill/agent_killed", sticky)
			}

			events := dispatcher.snapshot()
			if len(events) != 2 {
				t.Fatalf("alert count = %d, want 2 (%+v)", len(events), events)
			}
			runaway, killed := events[0], events[1]
			if runaway.Type != alert.TypeRunawayDetected {
				t.Fatalf("first alert type = %q, want %q", runaway.Type, alert.TypeRunawayDetected)
			}
			if runaway.Agent != "agent-a" || runaway.Severity != "critical" {
				t.Fatalf("runaway alert = %+v, want agent-a/critical", runaway)
			}
			if got := runaway.Data["request_count"]; got != tt.maxRequests+1 {
				t.Fatalf("runaway request_count = %v, want %d", got, tt.maxRequests+1)
			}
			if got := runaway.Data["window_seconds"]; got != tt.windowSeconds {
				t.Fatalf("runaway window_seconds = %v, want %d", got, tt.windowSeconds)
			}
			if killed.Type != alert.TypeAgentKilled || killed.Agent != "agent-a" {
				t.Fatalf("second alert = %+v, want agent_killed for agent-a", killed)
			}
			if got := killed.Data["reason"]; got != "runaway_detected" {
				t.Fatalf("killed reason = %v, want runaway_detected", got)
			}
		})
	}
}

func TestRunawayDetection_ConcurrentAgents(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name        string
		maxRequests int
		agents      int
		perAgent    int
		quietAgents int
		quietCalls  int
	}{
		{name: "three agents flood while one stays under the limit", maxRequests: 10, agents: 3, perAgent: 50, quietAgents: 1, quietCalls: 5},
		{name: "many agents at max plus one", maxRequests: 4, agents: 8, perAgent: 5, quietAgents: 2, quietCalls: 4},
		{name: "single request limit under heavy contention", maxRequests: 1, agents: 4, perAgent: 40, quietAgents: 0, quietCalls: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
			dispatcher := &capturingDispatcher{}
			manager := NewManagerWithClockAndDispatcher(runawayGateConfig(true, tt.maxRequests, 60), nil, clock, dispatcher)

			var mu sync.Mutex
			allowed := make(map[string]int)
			killed := make(map[string]int)
			record := func(agent string, action Action) {
				mu.Lock()
				defer mu.Unlock()
				switch action {
				case ActionAllow:
					allowed[agent]++
				case ActionKill:
					killed[agent]++
				default:
					t.Errorf("unexpected action %q for %s", action, agent)
				}
			}

			var wg sync.WaitGroup
			start := make(chan struct{})
			for a := 0; a < tt.agents; a++ {
				agent := fmt.Sprintf("flood-%d", a)
				for i := 0; i < tt.perAgent; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						record(agent, manager.CheckBudget(agent, 0))
					}()
				}
			}
			for q := 0; q < tt.quietAgents; q++ {
				agent := fmt.Sprintf("quiet-%d", q)
				for i := 0; i < tt.quietCalls; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						record(agent, manager.CheckBudget(agent, 0))
					}()
				}
			}
			close(start)
			wg.Wait()

			events := dispatcher.snapshot()
			for a := 0; a < tt.agents; a++ {
				agent := fmt.Sprintf("flood-%d", a)
				if allowed[agent] != tt.maxRequests {
					t.Fatalf("%s allowed = %d, want %d", agent, allowed[agent], tt.maxRequests)
				}
				if killed[agent] != tt.perAgent-tt.maxRequests {
					t.Fatalf("%s killed = %d, want %d", agent, killed[agent], tt.perAgent-tt.maxRequests)
				}
				if !manager.Snapshot(agent).Killed {
					t.Fatalf("%s should be killed", agent)
				}
				if got := countAlerts(events, agent, alert.TypeRunawayDetected); got != 1 {
					t.Fatalf("%s runaway_detected alerts = %d, want 1", agent, got)
				}
				if got := countAlerts(events, agent, alert.TypeAgentKilled); got != 1 {
					t.Fatalf("%s agent_killed alerts = %d, want 1", agent, got)
				}
			}
			for q := 0; q < tt.quietAgents; q++ {
				agent := fmt.Sprintf("quiet-%d", q)
				if allowed[agent] != tt.quietCalls || killed[agent] != 0 {
					t.Fatalf("%s allowed/killed = %d/%d, want %d/0", agent, allowed[agent], killed[agent], tt.quietCalls)
				}
				if manager.Snapshot(agent).Killed {
					t.Fatalf("%s should not be killed", agent)
				}
				if got := countAlerts(events, agent, alert.TypeAgentKilled); got != 0 {
					t.Fatalf("%s agent_killed alerts = %d, want 0", agent, got)
				}
			}
			if want := 2 * tt.agents; len(events) != want {
				t.Fatalf("total alerts = %d, want %d", len(events), want)
			}
		})
	}
}

func TestRunawayDetection_ConcurrentEnableDuringFlood(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		floods   int
		enables  int
		requests int
	}{
		{name: "enables interleaved with checks", floods: 4, enables: 8, requests: 30},
		{name: "more enables than checks", floods: 2, enables: 40, requests: 10},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
			dispatcher := &capturingDispatcher{}
			manager := NewManagerWithClockAndDispatcher(runawayGateConfig(true, 2, 60), nil, clock, dispatcher)

			var wg sync.WaitGroup
			start := make(chan struct{})
			for a := 0; a < tt.floods; a++ {
				agent := fmt.Sprintf("agent-%d", a)
				for i := 0; i < tt.requests; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						_ = manager.CheckBudget(agent, 0)
						_ = manager.Snapshot(agent)
					}()
				}
				for i := 0; i < tt.enables; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						manager.EnableAgent(agent)
					}()
				}
			}
			close(start)
			wg.Wait()

			// Whatever the interleaving, every kill dispatches exactly one runaway and
			// one killed alert, so the two counters must always match per agent.
			events := dispatcher.snapshot()
			for a := 0; a < tt.floods; a++ {
				agent := fmt.Sprintf("agent-%d", a)
				runaway := countAlerts(events, agent, alert.TypeRunawayDetected)
				killedAlerts := countAlerts(events, agent, alert.TypeAgentKilled)
				if runaway < 1 || runaway != killedAlerts {
					t.Fatalf("%s runaway/killed alerts = %d/%d, want equal and at least 1", agent, runaway, killedAlerts)
				}
			}

			// After the flood, enabling once more restores traffic once the window has passed.
			clock.Advance(61 * time.Second)
			for a := 0; a < tt.floods; a++ {
				agent := fmt.Sprintf("agent-%d", a)
				manager.EnableAgent(agent)
				if got := manager.CheckBudget(agent, 0); got != ActionAllow {
					t.Fatalf("%s after enable action = %q, want %q", agent, got, ActionAllow)
				}
			}
		})
	}
}
