package budget

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OberWatch/oberwatch/internal/storage"
)

// TaskBudgetExceededCode is the structured error code for a task cap rejection.
const TaskBudgetExceededCode = "task_budget_exceeded"

// taskState tracks the lifetime spend of one task.
//
// spentUSD is settled spend and is persisted. reservedUSD is the estimated cost
// of requests that are in flight; it only lives in memory because a request
// that is still running when the process stops never completes.
//
//nolint:govet // keep state fields grouped by update/read patterns.
type taskState struct {
	spentUSD     float64
	reservedUSD  float64
	limitUSD     float64
	requestCount int
	inFlight     int
	lastAgent    string
	firstSeenAt  time.Time
	lastSeenAt   time.Time
	dirty        bool
}

// TaskDecision is the result of a task budget check.
//
//nolint:govet // field order kept for API clarity.
type TaskDecision struct {
	Allowed      bool
	Enforced     bool
	Code         string
	Message      string
	TaskID       string
	Agent        string
	LimitUSD     float64
	SpentUSD     float64
	ReservedUSD  float64
	ProjectedUSD float64
}

// TaskView is an API-friendly view of one task budget.
//
//nolint:govet // field order kept for API clarity.
type TaskView struct {
	TaskID         string
	Status         string
	LastAgent      string
	LimitUSD       float64
	SpentUSD       float64
	ReservedUSD    float64
	RemainingUSD   float64
	PercentageUsed float64
	RequestCount   int
	InFlight       int
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
}

// TaskReservation holds the estimated cost of one in-flight request against a
// task. Exactly one of Settle or Release takes effect; later calls are no-ops.
type TaskReservation struct {
	manager      *BudgetManager
	taskID       string
	agent        string
	estimatedUSD float64
	once         sync.Once
}

// Settle releases the reservation and records the actual cost of the request.
func (r *TaskReservation) Settle(costUSD float64) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.manager.settleTask(r, costUSD, true)
	})
}

// Release drops the reservation without recording spend, for requests that
// failed before producing a billable response.
func (r *TaskReservation) Release() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.manager.settleTask(r, 0, false)
	})
}

// TaskLimitUSD returns the task cap that applies to requests from the agent.
// A per-agent task_budget_usd greater than zero is preferred over the gate value.
func (m *BudgetManager) TaskLimitUSD(agent string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.taskLimitLocked(normalizeAgent(agent))
}

func (m *BudgetManager) taskLimitLocked(agent string) float64 {
	if limit, ok := m.agentTaskLimit[agent]; ok && limit > 0 {
		return limit
	}
	return m.defaultTaskLimit
}

// ReserveTask checks the task cap against settled spend plus in-flight
// reservations plus the estimated cost of this request, and reserves the
// estimate when allowed. Callers must Settle or Release the reservation.
//
// A blank task ID is never budgeted and never shares a bucket: it returns an
// allowed decision with a nil reservation.
func (m *BudgetManager) ReserveTask(agent string, taskID string, estimatedUSD float64) (TaskDecision, *TaskReservation) {
	taskID = strings.TrimSpace(taskID)
	normalizedAgent := normalizeAgent(agent)
	if taskID == "" {
		return TaskDecision{Allowed: true, Agent: normalizedAgent}, nil
	}
	if estimatedUSD < 0 {
		estimatedUSD = 0
	}
	now := m.clock.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	limit := m.taskLimitLocked(normalizedAgent)
	state := m.taskStateLocked(taskID, now)
	projected := state.spentUSD + state.reservedUSD + estimatedUSD

	decision := TaskDecision{
		Allowed:      true,
		Enforced:     limit > 0,
		TaskID:       taskID,
		Agent:        normalizedAgent,
		LimitUSD:     limit,
		SpentUSD:     state.spentUSD,
		ReservedUSD:  state.reservedUSD,
		ProjectedUSD: projected,
	}

	if limit > 0 && projected > limit {
		decision.Allowed = false
		decision.Code = TaskBudgetExceededCode
		decision.Message = fmt.Sprintf(
			"Task '%s' would exceed its budget of $%.2f (spent: $%.2f, reserved: $%.2f, projected: $%.2f)",
			taskID,
			limit,
			state.spentUSD,
			state.reservedUSD,
			projected,
		)
		return decision, nil
	}

	state.reservedUSD += estimatedUSD
	state.inFlight++
	state.lastSeenAt = now
	state.lastAgent = normalizedAgent
	if limit > 0 {
		state.limitUSD = limit
	}

	return decision, &TaskReservation{
		manager:      m,
		taskID:       taskID,
		agent:        normalizedAgent,
		estimatedUSD: estimatedUSD,
	}
}

func (m *BudgetManager) settleTask(reservation *TaskReservation, costUSD float64, billed bool) {
	if costUSD < 0 {
		costUSD = 0
	}
	now := m.clock.Now().UTC()

	m.mu.Lock()
	state := m.taskStateLocked(reservation.taskID, now)
	state.reservedUSD -= reservation.estimatedUSD
	if state.inFlight > 0 {
		state.inFlight--
	}
	// Reservations are a sum of float estimates; once nothing is in flight the
	// hold is exactly zero, so drop any rounding residue instead of leaking it.
	if state.reservedUSD < 0 || state.inFlight == 0 {
		state.reservedUSD = 0
	}
	if billed {
		state.spentUSD += costUSD
		state.requestCount++
		state.lastSeenAt = now
		state.lastAgent = reservation.agent
		state.dirty = true
	}
	m.mu.Unlock()

	if billed {
		m.flushTaskIfNeeded(reservation.taskID)
	}
}

func (m *BudgetManager) taskStateLocked(taskID string, now time.Time) *taskState {
	state, ok := m.tasks[taskID]
	if ok {
		return state
	}
	state = &taskState{
		firstSeenAt: now,
		lastSeenAt:  now,
	}
	m.tasks[taskID] = state
	return state
}

// GetTask returns one task budget view.
func (m *BudgetManager) GetTask(taskID string) (TaskView, bool) {
	taskID = strings.TrimSpace(taskID)

	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.tasks[taskID]
	if !ok {
		return TaskView{}, false
	}
	return m.toTaskViewLocked(taskID, state), true
}

// ListTasks returns all known task budgets ordered by task ID.
func (m *BudgetManager) ListTasks() []TaskView {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]TaskView, 0, len(m.tasks))
	for taskID, state := range m.tasks {
		result = append(result, m.toTaskViewLocked(taskID, state))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TaskID < result[j].TaskID
	})
	return result
}

// ResetTask clears the settled spend of one task. In-flight reservations are
// kept so concurrent requests stay accounted for. It returns
// storage.ErrTaskNotFound for unknown tasks.
func (m *BudgetManager) ResetTask(taskID string) error {
	taskID = strings.TrimSpace(taskID)
	now := m.clock.Now().UTC()

	m.mu.Lock()
	state, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return storage.ErrTaskNotFound
	}
	state.spentUSD = 0
	state.requestCount = 0
	state.lastSeenAt = now
	state.dirty = true
	m.mu.Unlock()

	m.flushTaskIfNeeded(taskID)
	return nil
}

func (m *BudgetManager) toTaskViewLocked(taskID string, state *taskState) TaskView {
	// Report the cap that would actually be enforced for the next request from
	// the agent that last drove this task. state.limitUSD is only the last
	// enforced value, so falling back to it would advertise a limit, a
	// remaining balance and an "exceeded" status for a cap that no longer
	// applies: an agent without a task cap, or a gate cap set back to zero.
	limit := m.taskLimitLocked(normalizeAgent(state.lastAgent))
	remaining := 0.0
	if limit > 0 {
		remaining = limit - state.spentUSD - state.reservedUSD
		if remaining < 0 {
			remaining = 0
		}
	}
	status := "active"
	if limit > 0 && state.spentUSD >= limit {
		status = "exceeded"
	}
	return TaskView{
		TaskID:         taskID,
		Status:         status,
		LastAgent:      state.lastAgent,
		LimitUSD:       limit,
		SpentUSD:       state.spentUSD,
		ReservedUSD:    state.reservedUSD,
		RemainingUSD:   remaining,
		PercentageUsed: percentageUsed(limit, state.spentUSD),
		RequestCount:   state.requestCount,
		InFlight:       state.inFlight,
		FirstSeenAt:    state.firstSeenAt,
		LastSeenAt:     state.lastSeenAt,
	}
}

func (m *BudgetManager) loadPersistedTasks(ctx context.Context) error {
	if m.store == nil {
		return nil
	}

	records, err := m.store.ListTasks(ctx)
	if err != nil {
		return fmt.Errorf("load persisted tasks: %w", err)
	}

	now := m.clock.Now().UTC()
	for _, record := range records {
		taskID := strings.TrimSpace(record.TaskID)
		if taskID == "" {
			continue
		}
		m.tasks[taskID] = &taskState{
			spentUSD:     record.SpentUSD,
			limitUSD:     record.LimitUSD,
			requestCount: record.RequestCount,
			lastAgent:    record.LastAgent,
			firstSeenAt:  firstNonZeroTime(record.FirstSeenAt, now),
			lastSeenAt:   firstNonZeroTime(record.LastSeenAt, now),
		}
	}
	return nil
}

// flushTaskIfNeeded persists the one task that just changed. Task totals are
// written on every settlement rather than only on the periodic flush, so a
// restart cannot lose settled spend; writing just the named task keeps that
// per-request cost independent of how many tasks the process has seen.
func (m *BudgetManager) flushTaskIfNeeded(taskID string) {
	taskID = strings.TrimSpace(taskID)
	if m.store == nil || taskID == "" {
		return
	}
	record, ok := m.takeDirtyTaskRecord(taskID)
	if !ok {
		return
	}
	if err := m.storeTaskRecord(context.Background(), record); err != nil && m.logger != nil {
		m.logger.Warn("flush task state failed", "task_id", taskID, "error", err)
	}
}

func (m *BudgetManager) flushTasks(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	var firstErr error
	// One failing task must not strand the rest of the batch: their dirty flags
	// are already cleared, so returning early would drop their totals.
	for _, record := range m.snapshotDirtyTaskRecords() {
		if err := m.storeTaskRecord(ctx, record); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// storeTaskRecord writes one task total. A failed write puts the dirty flag
// back: dropping it would lose the settled total for good and let a restart
// under-count the task, which is the same as raising its cap.
func (m *BudgetManager) storeTaskRecord(ctx context.Context, record storage.TaskRecord) error {
	if err := m.store.UpsertTask(ctx, record); err != nil {
		m.markTaskDirty(record.TaskID)
		return fmt.Errorf("flush task %q: %w", record.TaskID, err)
	}
	return nil
}

func (m *BudgetManager) markTaskDirty(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		state.dirty = true
	}
}

func (m *BudgetManager) takeDirtyTaskRecord(taskID string) (storage.TaskRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.tasks[taskID]
	if !ok || !state.dirty {
		return storage.TaskRecord{}, false
	}
	state.dirty = false
	return taskRecordLocked(taskID, state), true
}

func (m *BudgetManager) snapshotDirtyTaskRecords() []storage.TaskRecord {
	m.mu.Lock()
	defer m.mu.Unlock()

	records := make([]storage.TaskRecord, 0)
	for taskID, state := range m.tasks {
		if !state.dirty {
			continue
		}
		records = append(records, taskRecordLocked(taskID, state))
		state.dirty = false
	}
	return records
}

func taskRecordLocked(taskID string, state *taskState) storage.TaskRecord {
	return storage.TaskRecord{
		TaskID:       taskID,
		LastAgent:    state.lastAgent,
		SpentUSD:     state.spentUSD,
		LimitUSD:     state.limitUSD,
		RequestCount: state.requestCount,
		FirstSeenAt:  state.firstSeenAt,
		LastSeenAt:   state.lastSeenAt,
	}
}
