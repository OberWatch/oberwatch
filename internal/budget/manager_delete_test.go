package budget

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

func newDeleteFixture(t *testing.T, gate config.GateConfig) (*BudgetManager, *storage.SQLiteStore) {
	t.Helper()

	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "delete.db"), 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	clock := newMockClock(time.Date(2026, time.August, 28, 9, 0, 0, 0, time.UTC))
	manager, err := NewPersistentManagerWithClockAndDispatcher(gate, nil, store, clock, nil)
	if err != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, store
}

func TestDeleteAgent(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name    string
		target  string
		prepare func(*testing.T, *BudgetManager)
		wantErr error
		assert  func(*testing.T, *BudgetManager, *storage.SQLiteStore)
	}{
		{
			name:   "forgets runtime state and persisted row, keeps other agents",
			target: "agent-a",
			prepare: func(t *testing.T, manager *BudgetManager) {
				t.Helper()
				manager.RecordSpend("agent-a", 2.5)
				manager.RecordSpend("agent-b", 1.0)
				if err := manager.UpdateBudget("agent-a", BudgetUpdate{LimitUSD: 99, Period: config.BudgetPeriodDaily, ActionOnExceed: config.BudgetActionReject}); err != nil {
					t.Fatalf("UpdateBudget() error = %v", err)
				}
				if err := manager.Flush(context.Background()); err != nil {
					t.Fatalf("Flush() error = %v", err)
				}
			},
			assert: func(t *testing.T, manager *BudgetManager, store *storage.SQLiteStore) {
				t.Helper()
				if _, found, err := store.GetAgent(context.Background(), "agent-a"); err != nil || found {
					t.Fatalf("GetAgent(agent-a) = found %v, err %v; want gone", found, err)
				}
				if _, found, err := store.GetAgent(context.Background(), "agent-b"); err != nil || !found {
					t.Fatalf("GetAgent(agent-b) = found %v, err %v; want kept", found, err)
				}
				for _, view := range manager.ListBudgets() {
					if view.Agent == "agent-a" {
						t.Fatalf("ListBudgets() still lists agent-a: %#v", view)
					}
				}
				if view := manager.GetBudget("agent-b"); view.SpentUSD != 1.0 {
					t.Fatalf("agent-b spent = %v, want 1.0", view.SpentUSD)
				}
				// A background flush after the delete must not resurrect the row.
				if err := manager.Flush(context.Background()); err != nil {
					t.Fatalf("Flush() error = %v", err)
				}
				if _, found, err := store.GetAgent(context.Background(), "agent-a"); err != nil || found {
					t.Fatalf("GetAgent(agent-a) after flush = found %v, err %v; want still gone", found, err)
				}
			},
		},
		{
			name:   "next request rediscovers the agent with default policy and zero spend",
			target: "agent-a",
			prepare: func(t *testing.T, manager *BudgetManager) {
				t.Helper()
				manager.RecordSpend("agent-a", 2.5)
				if err := manager.UpdateBudget("agent-a", BudgetUpdate{LimitUSD: 99, Period: config.BudgetPeriodDaily, ActionOnExceed: config.BudgetActionReject}); err != nil {
					t.Fatalf("UpdateBudget() error = %v", err)
				}
			},
			assert: func(t *testing.T, manager *BudgetManager, store *storage.SQLiteStore) {
				t.Helper()
				if action := manager.CheckBudget("agent-a", 0.01); action != ActionAllow {
					t.Fatalf("CheckBudget() after delete = %v, want allow", action)
				}
				view := manager.GetBudget("agent-a")
				if view.SpentUSD != 0 || view.LimitUSD != 10 {
					t.Fatalf("rediscovered view = %#v, want zero spend and default limit 10", view)
				}
				record, found, err := store.GetAgent(context.Background(), "agent-a")
				if err != nil || !found {
					t.Fatalf("GetAgent(agent-a) after rediscovery = found %v, err %v; want recreated", found, err)
				}
				if record.BudgetSpentUSD != 0 || record.BudgetLimitUSD != 10 {
					t.Fatalf("recreated record = %#v, want fresh defaults", record)
				}
			},
		},
		{
			name:   "tracked but never flushed agent is deleted without a store row",
			target: "agent-a",
			prepare: func(t *testing.T, manager *BudgetManager) {
				t.Helper()
				// Only touch runtime state; the delete must not be a not-found.
				manager.mu.Lock()
				manager.knownAgents["agent-a"] = struct{}{}
				manager.mu.Unlock()
			},
			assert: func(t *testing.T, manager *BudgetManager, _ *storage.SQLiteStore) {
				t.Helper()
				manager.mu.Lock()
				defer manager.mu.Unlock()
				if _, ok := manager.knownAgents["agent-a"]; ok {
					t.Fatal("knownAgents still holds agent-a")
				}
			},
		},
		{
			name:    "unknown agent is not found",
			target:  "ghost",
			wantErr: storage.ErrAgentNotFound,
		},
		{
			name:    "blank name is not found",
			target:  "  ",
			wantErr: storage.ErrAgentNotFound,
		},
		{
			name:    "configured agent is protected",
			target:  "configured-agent",
			wantErr: ErrAgentProtected,
			prepare: func(t *testing.T, manager *BudgetManager) {
				t.Helper()
				manager.RecordSpend("configured-agent", 1)
			},
			assert: func(t *testing.T, manager *BudgetManager, _ *storage.SQLiteStore) {
				t.Helper()
				if view := manager.GetBudget("configured-agent"); view.SpentUSD != 1 {
					t.Fatalf("protected agent spent = %v, want untouched 1", view.SpentUSD)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gate := baseGateConfig()
			gate.Agents = []config.AgentBudgetConfig{{
				Name: "configured-agent", LimitUSD: 5, Period: config.BudgetPeriodDaily, ActionOnExceed: config.BudgetActionAlert,
			}}
			manager, store := newDeleteFixture(t, gate)
			if tt.prepare != nil {
				tt.prepare(t, manager)
			}

			_, err := manager.DeleteAgent(context.Background(), tt.target)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DeleteAgent() error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("DeleteAgent() error = %v", err)
			}
			if tt.assert != nil {
				tt.assert(t, manager, store)
			}
		})
	}
}

func TestDeleteAgent_DetachesTasksAndTaskLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "task keeps its spend but drops the agent pointer"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gate := baseGateConfig()
			gate.TaskBudgetUSD = 5
			manager, store := newDeleteFixture(t, gate)

			decision, reservation := manager.ReserveTask("agent-a", "task-1", 0.5)
			if !decision.Allowed {
				t.Fatalf("ReserveTask() = %#v, want allowed", decision)
			}
			reservation.Settle(0.5)
			manager.RecordSpend("agent-a", 0.5)
			if err := manager.Flush(context.Background()); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}

			deletion, err := manager.DeleteAgent(context.Background(), "agent-a")
			if err != nil {
				t.Fatalf("DeleteAgent() error = %v", err)
			}
			if deletion.TasksDetached != 1 {
				t.Fatalf("TasksDetached = %d, want 1", deletion.TasksDetached)
			}

			view, found := manager.GetTask("task-1")
			if !found {
				t.Fatal("GetTask(task-1) found = false, want the task kept")
			}
			if view.LastAgent != "" || view.SpentUSD != 0.5 {
				t.Fatalf("task view = %#v, want cleared agent and kept spend", view)
			}
			record, found, err := store.GetTask(context.Background(), "task-1")
			if err != nil || !found {
				t.Fatalf("store GetTask(task-1) = found %v, err %v", found, err)
			}
			if record.LastAgent != "" || record.SpentUSD != 0.5 {
				t.Fatalf("persisted task = %#v, want cleared agent and kept spend", record)
			}
		})
	}
}

// resurrectGateTimeout bounds how long a parked store write below waits for
// the delete. It is only a liveness backstop: when the flush and the delete are
// properly serialized the delete cannot finish first, so the gate times out.
const resurrectGateTimeout = time.Second

// flushGate parks one store write until the delete racing it has finished, so
// the pending-write window is exercised the same way on every run.
type flushGate struct {
	entered chan struct{}
	release chan struct{}
	armed   atomic.Bool
}

func newFlushGate() *flushGate {
	return &flushGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *flushGate) trip() {
	if !g.armed.CompareAndSwap(true, false) {
		return
	}
	close(g.entered)
	select {
	case <-g.release:
	case <-time.After(resurrectGateTimeout):
	}
}

// gatedStore parks the first armed flush write for one agent or one task so a
// delete can be attempted while that write is still pending.
type gatedStore struct {
	storage.Store

	agentGate *flushGate
	taskGate  *flushGate
	agent     string
	taskID    string
}

func newGatedStore(inner storage.Store, agent string, taskID string) *gatedStore {
	return &gatedStore{
		Store:     inner,
		agent:     agent,
		taskID:    taskID,
		agentGate: newFlushGate(),
		taskGate:  newFlushGate(),
	}
}

func (g *gatedStore) UpsertAgent(ctx context.Context, record storage.AgentRecord) error {
	if record.Name == g.agent {
		g.agentGate.trip()
	}
	return g.Store.UpsertAgent(ctx, record)
}

func (g *gatedStore) UpsertTask(ctx context.Context, record storage.TaskRecord) error {
	if record.TaskID == g.taskID {
		g.taskGate.trip()
	}
	return g.Store.UpsertTask(ctx, record)
}

// TestDeleteAgent_FlushInFlightDoesNotResurrect pins the window between the
// flush snapshot and its store write. Flush collects dirty agent records under
// the lock but writes them outside it, so a delete landing in between used to
// be undone by the write that followed: the agents row came back with its old
// spend, nothing in memory referenced it any more, and a restart reloaded it.
func TestDeleteAgent_FlushInFlightDoesNotResurrect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a delete during an in-flight flush is not undone by the pending write"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			base, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "resurrect.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })

			gated := newGatedStore(base, "victim", "")
			clock := newMockClock(time.Date(2026, time.August, 28, 9, 0, 0, 0, time.UTC))
			manager, err := NewPersistentManagerWithClockAndDispatcher(baseGateConfig(), nil, gated, clock, nil)
			if err != nil {
				t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", err)
			}
			t.Cleanup(func() { _ = manager.Close() })

			manager.RecordSpend("victim", 1)
			manager.RecordSpend("bystander", 1)
			if err := manager.Flush(ctx); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			// Dirty again, so the next flush has a record to write back.
			manager.RecordSpend("victim", 2)

			gated.agentGate.armed.Store(true)
			flushDone := make(chan error, 1)
			go func() { flushDone <- manager.Flush(context.Background()) }()

			<-gated.agentGate.entered

			deleteDone := make(chan error, 1)
			go func() {
				_, deleteErr := manager.DeleteAgent(context.Background(), "victim")
				// Releasing here is what makes the old behavior fail
				// deterministically: the parked write only resumes once the
				// delete has already committed.
				close(gated.agentGate.release)
				deleteDone <- deleteErr
			}()

			if err := <-flushDone; err != nil {
				t.Fatalf("Flush() during delete error = %v", err)
			}
			if err := <-deleteDone; err != nil {
				t.Fatalf("DeleteAgent() error = %v", err)
			}

			if _, found, err := base.GetAgent(ctx, "victim"); err != nil || found {
				t.Fatalf("GetAgent(victim) = found %v, err %v; want the deleted row to stay deleted", found, err)
			}
			if _, found, err := base.GetAgent(ctx, "bystander"); err != nil || !found {
				t.Fatalf("GetAgent(bystander) = found %v, err %v; want kept", found, err)
			}
			for _, view := range manager.ListBudgets() {
				if view.Agent == "victim" {
					t.Fatalf("ListBudgets() still tracks victim: %#v", view)
				}
			}
			// A later flush must not bring it back either.
			if err := manager.Flush(ctx); err != nil {
				t.Fatalf("Flush() after delete error = %v", err)
			}
			if _, found, err := base.GetAgent(ctx, "victim"); err != nil || found {
				t.Fatalf("GetAgent(victim) after later flush = found %v, err %v; want still gone", found, err)
			}
		})
	}
}

// TestDeleteAgent_SettlingTaskDoesNotReattach pins the same pending-write
// window on the task side. Every settled task request writes its own row from
// outside the lock, so a write that was snapshotted before the delete used to
// point the task back at the agent that had just been removed.
func TestDeleteAgent_SettlingTaskDoesNotReattach(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a task settling during a delete does not point back at the deleted agent"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			base, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "reattach.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() { _ = base.Close() })

			gated := newGatedStore(base, "", "task-1")
			gateCfg := baseGateConfig()
			gateCfg.TaskBudgetUSD = 5
			clock := newMockClock(time.Date(2026, time.August, 28, 9, 0, 0, 0, time.UTC))
			manager, err := NewPersistentManagerWithClockAndDispatcher(gateCfg, nil, gated, clock, nil)
			if err != nil {
				t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", err)
			}
			t.Cleanup(func() { _ = manager.Close() })

			settle := func(estimate float64) {
				t.Helper()
				decision, reservation := manager.ReserveTask("victim", "task-1", estimate)
				if !decision.Allowed {
					t.Fatalf("ReserveTask() = %#v, want allowed", decision)
				}
				reservation.Settle(estimate)
			}

			settle(0.5)
			manager.RecordSpend("victim", 0.5)
			if flushErr := manager.Flush(ctx); flushErr != nil {
				t.Fatalf("Flush() error = %v", flushErr)
			}

			// The next settlement writes task-1 from the request path; park it
			// there so the delete has to race the pending write.
			gated.taskGate.armed.Store(true)
			settleDone := make(chan struct{})
			go func() {
				defer close(settleDone)
				settle(0.25)
			}()

			<-gated.taskGate.entered

			deleteDone := make(chan error, 1)
			go func() {
				_, deleteErr := manager.DeleteAgent(context.Background(), "victim")
				close(gated.taskGate.release)
				deleteDone <- deleteErr
			}()

			<-settleDone
			if deleteErr := <-deleteDone; deleteErr != nil {
				t.Fatalf("DeleteAgent() error = %v", deleteErr)
			}

			record, found, err := base.GetTask(ctx, "task-1")
			if err != nil || !found {
				t.Fatalf("GetTask(task-1) = found %v, err %v; want the task kept", found, err)
			}
			if record.LastAgent != "" {
				t.Fatalf("persisted task last_agent = %q, want cleared by the delete", record.LastAgent)
			}
			if view, ok := manager.GetTask("task-1"); !ok || view.LastAgent != "" {
				t.Fatalf("GetTask(task-1) = %#v, ok %v; want cleared agent in memory too", view, ok)
			}
			if _, found, err := base.GetAgent(ctx, "victim"); err != nil || found {
				t.Fatalf("GetAgent(victim) = found %v, err %v; want gone", found, err)
			}
		})
	}
}

func TestDeleteAgent_ConcurrentWithDiscovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		requests int
		deletes  int
	}{
		{name: "deletes interleaved with requests never leave stale state", requests: 200, deletes: 20},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager, store := newDeleteFixture(t, baseGateConfig())

			var wg sync.WaitGroup
			for index := 0; index < tt.requests; index++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					manager.CheckBudget("racer", 0.01)
					manager.RecordSpend("racer", 0.01)
				}()
			}
			for index := 0; index < tt.deletes; index++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := manager.DeleteAgent(context.Background(), "racer"); err != nil && !errors.Is(err, storage.ErrAgentNotFound) {
						t.Errorf("DeleteAgent() error = %v", err)
					}
				}()
			}
			wg.Wait()

			// Whatever the interleaving, the final states must agree: after one
			// more request the agent is tracked and persisted, and after one more
			// delete it is gone from both.
			manager.RecordSpend("racer", 0.01)
			if err := manager.Flush(context.Background()); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			if _, found, err := store.GetAgent(context.Background(), "racer"); err != nil || !found {
				t.Fatalf("GetAgent(racer) after request = found %v, err %v; want present", found, err)
			}
			if _, err := manager.DeleteAgent(context.Background(), "racer"); err != nil {
				t.Fatalf("final DeleteAgent() error = %v", err)
			}
			if err := manager.Flush(context.Background()); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			if _, found, err := store.GetAgent(context.Background(), "racer"); err != nil || found {
				t.Fatalf("GetAgent(racer) after delete = found %v, err %v; want gone", found, err)
			}
			for _, view := range manager.ListBudgets() {
				if view.Agent == "racer" {
					t.Fatalf("ListBudgets() still tracks racer after delete: %#v", view)
				}
			}
		})
	}
}
