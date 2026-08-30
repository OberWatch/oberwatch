package budget

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

func taskGateConfig(taskLimit float64) config.GateConfig {
	cfg := baseGateConfig()
	cfg.TaskBudgetUSD = taskLimit
	return cfg
}

func newTaskStore(t *testing.T) *storage.SQLiteStore {
	t.Helper()
	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "tasks.db"), 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func approxEqual(got float64, want float64) bool {
	return got > want-1e-9 && got < want+1e-9
}

func TestReserveTask_BlankTaskIDIsNeverBudgeted(t *testing.T) {
	t.Parallel()

	manager := NewManagerWithClock(taskGateConfig(0.000001), nil, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)))

	for _, taskID := range []string{"", "   ", "\t"} {
		decision, reservation := manager.ReserveTask("agent-a", taskID, 100)
		if !decision.Allowed || decision.Enforced || reservation != nil {
			t.Fatalf("ReserveTask(%q) = %#v, %v; want allowed, unenforced, nil reservation", taskID, decision, reservation)
		}
		// Settling a nil reservation must be a safe no-op.
		reservation.Settle(5)
		reservation.Release()
	}

	if got := manager.ListTasks(); len(got) != 0 {
		t.Fatalf("ListTasks() = %#v, want no shared bucket for blank task IDs", got)
	}
}

func TestReserveTask_ZeroLimitDisablesEnforcementButStillReports(t *testing.T) {
	t.Parallel()

	manager := NewManagerWithClock(taskGateConfig(0), nil, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)))

	decision, reservation := manager.ReserveTask("agent-a", "task-1", 1_000_000)
	if !decision.Allowed || decision.Enforced || reservation == nil {
		t.Fatalf("ReserveTask() = %#v, want allowed and unenforced with a reservation", decision)
	}
	reservation.Settle(3)

	view, found := manager.GetTask("task-1")
	if !found {
		t.Fatal("GetTask() found = false, want true")
	}
	if view.LimitUSD != 0 || view.SpentUSD != 3 || view.RequestCount != 1 || view.Status != "active" || view.RemainingUSD != 0 || view.PercentageUsed != 0 {
		t.Fatalf("view = %#v, want unlimited task with spent 3", view)
	}
}

func TestReserveTask_ProjectedCapRejectsAndReportsStructure(t *testing.T) {
	t.Parallel()

	manager := NewManagerWithClock(taskGateConfig(1), nil, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)))

	first, reservation := manager.ReserveTask("agent-a", "task-1", 0.6)
	if !first.Allowed || !first.Enforced || reservation == nil {
		t.Fatalf("first ReserveTask() = %#v, want allowed", first)
	}

	// While the first request is in flight its estimate counts against the cap.
	second, secondReservation := manager.ReserveTask("agent-a", "task-1", 0.5)
	if second.Allowed || secondReservation != nil {
		t.Fatalf("second ReserveTask() = %#v, want rejection while 0.6 is reserved", second)
	}
	if second.Code != TaskBudgetExceededCode || second.TaskID != "task-1" || second.Agent != "agent-a" {
		t.Fatalf("rejection = %#v, want code/task/agent populated", second)
	}
	if second.LimitUSD != 1 || second.SpentUSD != 0 || !approxEqual(second.ReservedUSD, 0.6) || !approxEqual(second.ProjectedUSD, 1.1) {
		t.Fatalf("rejection totals = %#v, want limit 1, spent 0, reserved 0.6, projected 1.1", second)
	}
	if second.Message == "" {
		t.Fatal("rejection message is empty")
	}

	// The first request turns out to cost less than estimated.
	reservation.Settle(0.4)
	reservation.Settle(0.4) // idempotent
	reservation.Release()   // idempotent

	view, _ := manager.GetTask("task-1")
	if !approxEqual(view.SpentUSD, 0.4) || view.ReservedUSD != 0 || view.InFlight != 0 || view.RequestCount != 1 {
		t.Fatalf("view after settle = %#v, want spent 0.4 and nothing reserved", view)
	}
	if !approxEqual(view.RemainingUSD, 0.6) || !approxEqual(view.PercentageUsed, 40) {
		t.Fatalf("view remaining/pct = %v/%v, want 0.6/40", view.RemainingUSD, view.PercentageUsed)
	}

	third, thirdReservation := manager.ReserveTask("agent-a", "task-1", 0.5)
	if !third.Allowed || thirdReservation == nil {
		t.Fatalf("third ReserveTask() = %#v, want allowed after settle freed headroom", third)
	}
	thirdReservation.Release()

	view, _ = manager.GetTask("task-1")
	if !approxEqual(view.SpentUSD, 0.4) || view.ReservedUSD != 0 || view.RequestCount != 1 {
		t.Fatalf("view after release = %#v, want released request to leave no trace", view)
	}

	exact, exactReservation := manager.ReserveTask("agent-a", "task-1", 0.6)
	if !exact.Allowed {
		t.Fatalf("ReserveTask(reaching the cap exactly) = %#v, want allowed", exact)
	}
	exactReservation.Settle(0.6)
	view, _ = manager.GetTask("task-1")
	if view.Status != "exceeded" {
		t.Fatalf("status = %q, want exceeded once spent reaches the cap", view.Status)
	}
	over, _ := manager.ReserveTask("agent-a", "task-1", 0.000001)
	if over.Allowed {
		t.Fatalf("ReserveTask(after cap) = %#v, want rejection", over)
	}
}

func TestReserveTask_NegativeEstimateAndCostAreClamped(t *testing.T) {
	t.Parallel()

	manager := NewManagerWithClock(taskGateConfig(1), nil, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)))
	decision, reservation := manager.ReserveTask("agent-a", "task-1", -5)
	if !decision.Allowed || decision.ProjectedUSD != 0 {
		t.Fatalf("ReserveTask(negative) = %#v, want projected 0", decision)
	}
	reservation.Settle(-2)
	view, _ := manager.GetTask("task-1")
	if view.SpentUSD != 0 || view.ReservedUSD != 0 {
		t.Fatalf("view = %#v, want nothing negative recorded", view)
	}
}

func TestReserveTask_AgentAndTaskIndependence(t *testing.T) {
	t.Parallel()

	cfg := taskGateConfig(1)
	cfg.DefaultBudget.LimitUSD = 0.5
	cfg.DefaultBudget.ActionOnExceed = config.BudgetActionKill
	clock := newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	manager := NewManagerWithClock(cfg, nil, clock)

	// Tasks are independent of each other.
	_, resA := manager.ReserveTask("agent-a", "task-a", 0.9)
	resA.Settle(0.9)
	decisionB, resB := manager.ReserveTask("agent-a", "task-b", 0.9)
	if !decisionB.Allowed {
		t.Fatalf("task-b decision = %#v, want independence from task-a spend", decisionB)
	}
	resB.Settle(0.9)

	// Task spend does not count against the agent budget.
	if snapshot := manager.Snapshot("agent-a"); snapshot.SpentUSD != 0 {
		t.Fatalf("agent spent = %v, want 0 (task settlement is not agent spend)", snapshot.SpentUSD)
	}

	// Agent spend does not count against the task budget, and an agent kill
	// does not change the task decision.
	manager.RecordSpend("agent-a", 10)
	if decision := manager.CheckBudgetDetailed("agent-a", 0.01); decision.Action != ActionKill {
		t.Fatalf("agent decision = %#v, want kill", decision)
	}
	decisionC, resC := manager.ReserveTask("agent-a", "task-c", 0.9)
	if !decisionC.Allowed || decisionC.SpentUSD != 0 {
		t.Fatalf("task-c decision = %#v, want allowed with zero task spend", decisionC)
	}
	resC.Release()

	// The same task keeps one shared total across agents, and the agent budget
	// period reset never resets task spend.
	over, _ := manager.ReserveTask("agent-b", "task-a", 0.2)
	if over.Allowed {
		t.Fatalf("agent-b on task-a = %#v, want rejection from shared task total", over)
	}
	clock.Advance(48 * time.Hour)
	if snapshot := manager.Snapshot("agent-a"); snapshot.SpentUSD != 0 {
		t.Fatalf("agent spent after period reset = %v, want 0", snapshot.SpentUSD)
	}
	stillOver, _ := manager.ReserveTask("agent-a", "task-a", 0.2)
	if stillOver.Allowed || !approxEqual(stillOver.SpentUSD, 0.9) {
		t.Fatalf("task-a after agent period reset = %#v, want still exceeded with spent 0.9", stillOver)
	}
}

func TestReserveTask_PerAgentTaskLimitIsPreferred(t *testing.T) {
	t.Parallel()

	cfg := taskGateConfig(1)
	manager := NewManagerWithClock(cfg, nil, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)))

	premiumTaskLimit := 5.0
	if err := manager.UpdateBudget("premium-agent", BudgetUpdate{LimitUSD: 50, Period: config.BudgetPeriodDaily, ActionOnExceed: config.BudgetActionAlert, TaskBudgetUSD: &premiumTaskLimit}); err != nil {
		t.Fatalf("UpdateBudget(premium-agent) error = %v", err)
	}
	inheritTaskLimit := 0.0
	if err := manager.UpdateBudget("inherit-agent", BudgetUpdate{LimitUSD: 50, Period: config.BudgetPeriodDaily, ActionOnExceed: config.BudgetActionAlert, TaskBudgetUSD: &inheritTaskLimit}); err != nil {
		t.Fatalf("UpdateBudget(inherit-agent) error = %v", err)
	}

	if got := manager.TaskLimitUSD("premium-agent"); got != 5 {
		t.Fatalf("TaskLimitUSD(premium-agent) = %v, want 5", got)
	}
	if got := manager.TaskLimitUSD("inherit-agent"); got != 1 {
		t.Fatalf("TaskLimitUSD(inherit-agent) = %v, want gate default 1", got)
	}
	if got := manager.TaskLimitUSD("unknown-agent"); got != 1 {
		t.Fatalf("TaskLimitUSD(unknown-agent) = %v, want gate default 1", got)
	}

	premium, reservation := manager.ReserveTask("premium-agent", "task-1", 3)
	if !premium.Allowed || premium.LimitUSD != 5 {
		t.Fatalf("premium decision = %#v, want allowed with limit 5", premium)
	}
	reservation.Settle(3)

	inherited, _ := manager.ReserveTask("inherit-agent", "task-1", 0.1)
	if inherited.Allowed || inherited.LimitUSD != 1 {
		t.Fatalf("inherit decision = %#v, want rejection under the gate default of 1", inherited)
	}
}

func TestReserveTask_ConcurrentReservationsNeverExceedCap(t *testing.T) {
	t.Parallel()

	manager := NewManagerWithClock(taskGateConfig(1), nil, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)))

	const workers = 64
	const estimate = 0.1

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	reservations := make([]*TaskReservation, 0, workers)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			decision, reservation := manager.ReserveTask("agent-a", "task-race", estimate)
			if !decision.Allowed {
				return
			}
			mu.Lock()
			allowed++
			reservations = append(reservations, reservation)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if allowed != 10 {
		t.Fatalf("allowed reservations = %d, want exactly 10 for a $1 cap at $0.10 each", allowed)
	}

	wg.Add(len(reservations))
	for _, reservation := range reservations {
		go func(reservation *TaskReservation) {
			defer wg.Done()
			reservation.Settle(estimate)
		}(reservation)
	}
	wg.Wait()

	view, _ := manager.GetTask("task-race")
	if !approxEqual(view.SpentUSD, 1) || view.ReservedUSD != 0 || view.InFlight != 0 || view.RequestCount != 10 {
		t.Fatalf("view = %#v, want spent 1.0 with no outstanding reservations", view)
	}
}

func TestTaskBudget_PersistsAcrossRestartAndReset(t *testing.T) {
	t.Parallel()

	cfg := taskGateConfig(1)
	store := newTaskStore(t)
	clock := newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))

	first, newErr := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store, clock, nil)
	if newErr != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", newErr)
	}

	_, settled := first.ReserveTask("agent-a", "task-1", 0.5)
	settled.Settle(0.7)
	// An in-flight reservation at shutdown must not be persisted as spend.
	_, inFlight := first.ReserveTask("agent-a", "task-1", 0.2)
	_ = inFlight
	// An unlimited task is still reported after restart.
	_, other := first.ReserveTask("agent-a", "task-2", 0)
	other.Settle(0.05)
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	record, found, err := store.GetTask(context.Background(), "task-1")
	if err != nil || !found {
		t.Fatalf("GetTask() = %#v, %v, %v; want persisted record", record, found, err)
	}
	if !approxEqual(record.SpentUSD, 0.7) || record.RequestCount != 1 || record.LimitUSD != 1 || record.LastAgent != "agent-a" {
		t.Fatalf("persisted record = %#v, want spent 0.7, 1 request, limit 1", record)
	}

	second, err := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store, clock, nil)
	if err != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher(restart) error = %v", err)
	}
	t.Cleanup(func() {
		_ = second.Close()
	})

	view, found := second.GetTask("task-1")
	if !found || !approxEqual(view.SpentUSD, 0.7) || view.ReservedUSD != 0 || view.InFlight != 0 {
		t.Fatalf("restored view = %#v, want spent 0.7 with reservations dropped", view)
	}
	if tasks := second.ListTasks(); len(tasks) != 2 || tasks[0].TaskID != "task-1" || tasks[1].TaskID != "task-2" {
		t.Fatalf("ListTasks() after restart = %#v, want task-1 and task-2", tasks)
	}

	rejected, _ := second.ReserveTask("agent-a", "task-1", 0.4)
	if rejected.Allowed {
		t.Fatalf("ReserveTask(after restart) = %#v, want rejection from restored spend", rejected)
	}

	if resetErr := second.ResetTask("task-1"); resetErr != nil {
		t.Fatalf("ResetTask() error = %v", resetErr)
	}
	view, _ = second.GetTask("task-1")
	if view.SpentUSD != 0 || view.RequestCount != 0 || view.Status != "active" {
		t.Fatalf("view after reset = %#v, want zero spend", view)
	}
	record, _, err = store.GetTask(context.Background(), "task-1")
	if err != nil || record.SpentUSD != 0 || record.RequestCount != 0 {
		t.Fatalf("persisted record after reset = %#v, %v; want zero spend persisted immediately", record, err)
	}
	allowed, _ := second.ReserveTask("agent-a", "task-1", 0.4)
	if !allowed.Allowed {
		t.Fatalf("ReserveTask(after reset) = %#v, want allowed", allowed)
	}

	if err := second.ResetTask("never-seen"); !errors.Is(err, storage.ErrTaskNotFound) {
		t.Fatalf("ResetTask(unknown) error = %v, want ErrTaskNotFound", err)
	}
	if _, found := second.GetTask("never-seen"); found {
		t.Fatal("GetTask(unknown) found = true, want false")
	}
}

func TestTaskView_ReportsTheCurrentlyEnforcedLimit(t *testing.T) {
	t.Parallel()

	// The gate does not cap tasks; only premium-agent does.
	cfg := taskGateConfig(0)
	manager := NewManagerWithClock(cfg, nil, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)))

	premiumTaskLimit := 1.0
	if err := manager.UpdateBudget("premium-agent", BudgetUpdate{LimitUSD: 50, Period: config.BudgetPeriodDaily, ActionOnExceed: config.BudgetActionAlert, TaskBudgetUSD: &premiumTaskLimit}); err != nil {
		t.Fatalf("UpdateBudget(premium-agent) error = %v", err)
	}

	_, reservation := manager.ReserveTask("premium-agent", "task-1", 1)
	reservation.Settle(1)

	view, _ := manager.GetTask("task-1")
	if view.LimitUSD != 1 || view.Status != "exceeded" || view.RemainingUSD != 0 || view.PercentageUsed != 100 {
		t.Fatalf("view under the premium cap = %#v, want limit 1 and exceeded", view)
	}

	// The same task driven by an agent with no task cap is not enforced, so the
	// view must stop advertising a limit that no request would be held to.
	decision, plain := manager.ReserveTask("plain-agent", "task-1", 5)
	if !decision.Allowed || decision.Enforced || decision.LimitUSD != 0 {
		t.Fatalf("plain-agent decision = %#v, want an allowed unenforced reservation", decision)
	}
	plain.Settle(5)

	view, _ = manager.GetTask("task-1")
	if view.LimitUSD != 0 || view.Status != "active" || view.RemainingUSD != 0 || view.PercentageUsed != 0 {
		t.Fatalf("view after an unenforced request = %#v, want no cap reported", view)
	}
	if !approxEqual(view.SpentUSD, 6) || view.RequestCount != 2 {
		t.Fatalf("view totals = %#v, want spent 6 over 2 requests", view)
	}
}

func TestRenameAgent_CarriesPerAgentTaskLimit(t *testing.T) {
	t.Parallel()

	cfg := taskGateConfig(10)
	store := newTaskStore(t)
	ctx := context.Background()
	manager, newErr := NewPersistentManagerWithClockAndDispatcher(cfg, nil, store, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)), nil)
	if newErr != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", newErr)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})

	// The agent has to exist in the store before it can be renamed.
	manager.RecordSpend("research", 1)
	researchTaskLimit := 1.0
	if err := manager.UpdateBudget("research", BudgetUpdate{LimitUSD: 50, Period: config.BudgetPeriodDaily, ActionOnExceed: config.BudgetActionAlert, TaskBudgetUSD: &researchTaskLimit}); err != nil {
		t.Fatalf("UpdateBudget(research) error = %v", err)
	}
	if err := manager.Flush(ctx); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	_, reservation := manager.ReserveTask("research", "task-1", 0.9)
	reservation.Settle(0.9)

	if err := manager.RenameAgent(ctx, "research", "research-v2"); err != nil {
		t.Fatalf("RenameAgent() error = %v", err)
	}

	if got := manager.TaskLimitUSD("research-v2"); got != 1 {
		t.Fatalf("TaskLimitUSD(research-v2) = %v, want the $1 task cap to follow the rename", got)
	}
	rejected, rejectedReservation := manager.ReserveTask("research-v2", "task-1", 0.2)
	if rejected.Allowed || rejected.LimitUSD != 1 || rejectedReservation != nil {
		t.Fatalf("decision after rename = %#v, want rejection under the $1 cap, not the $10 gate cap", rejected)
	}

	view, found := manager.GetTask("task-1")
	if !found || view.LastAgent != "research-v2" || view.LimitUSD != 1 {
		t.Fatalf("view after rename = %#v (found=%v), want the new agent name and its cap", view, found)
	}
	record, found, err := store.GetTask(ctx, "task-1")
	if err != nil || !found || record.LastAgent != "research-v2" {
		t.Fatalf("persisted record = %#v, %v, %v; want the renamed agent persisted", record, found, err)
	}
}

func TestTaskBudget_FailedFlushKeepsEveryTotalDirty(t *testing.T) {
	t.Parallel()

	// This store is closed by hand below, so it is not registered for cleanup.
	store, storeErr := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "flush-failure.db"), 0, nil)
	if storeErr != nil {
		t.Fatalf("NewSQLiteStore() error = %v", storeErr)
	}
	manager, newErr := NewPersistentManagerWithClockAndDispatcher(taskGateConfig(0), nil, store, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)), nil)
	if newErr != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", newErr)
	}
	t.Cleanup(func() {
		// The final flush cannot succeed against a closed store.
		_ = manager.Close()
	})

	// Closing the store makes every write fail, which stands in for any
	// transient storage failure.
	if err := store.Close(); err != nil {
		t.Fatalf("Close(store) error = %v", err)
	}

	for _, taskID := range []string{"task-1", "task-2"} {
		manager.mu.Lock()
		state := manager.taskStateLocked(taskID, manager.clock.Now())
		state.spentUSD = 0.5
		state.requestCount = 1
		state.dirty = true
		manager.mu.Unlock()
	}

	if err := manager.Flush(context.Background()); err == nil {
		t.Fatal("Flush() error = nil, want the store failure reported")
	}

	// A dropped dirty flag would lose the settled total for good, and a restart
	// would then under-count the task. Both tasks must still be pending, not
	// just the one that failed first.
	records := manager.snapshotDirtyTaskRecords()
	if len(records) != 2 {
		t.Fatalf("dirty tasks after a failed flush = %#v, want both still pending", records)
	}
	for _, record := range records {
		if !approxEqual(record.SpentUSD, 0.5) {
			t.Fatalf("pending record = %#v, want spent 0.5 preserved", record)
		}
	}
}

func TestTaskBudget_FlushIncludesDirtyTasks(t *testing.T) {
	t.Parallel()

	store := newTaskStore(t)
	manager, newErr := NewPersistentManagerWithClockAndDispatcher(taskGateConfig(0), nil, store, newMockClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)), nil)
	if newErr != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", newErr)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})

	// Mark a task dirty without the immediate per-settlement flush by
	// mutating state directly, then rely on Flush.
	manager.mu.Lock()
	state := manager.taskStateLocked("task-dirty", manager.clock.Now())
	state.spentUSD = 0.3
	state.requestCount = 2
	state.lastAgent = "agent-z"
	state.dirty = true
	manager.mu.Unlock()

	if err := manager.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	record, found, err := store.GetTask(context.Background(), "task-dirty")
	if err != nil || !found || !approxEqual(record.SpentUSD, 0.3) || record.RequestCount != 2 || record.LastAgent != "agent-z" {
		t.Fatalf("GetTask() = %#v, %v, %v; want flushed dirty task", record, found, err)
	}

	// A second flush has nothing dirty and stays a no-op.
	if records := manager.snapshotDirtyTaskRecords(); len(records) != 0 {
		t.Fatalf("snapshotDirtyTaskRecords() after flush = %#v, want none", records)
	}
}
