package storage

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteStore_TaskBudgetsMigration(t *testing.T) {
	t.Parallel()

	dsn := filepath.Join(t.TempDir(), "tasks-migration.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	store, openErr := NewSQLiteStore(dsn, 0, logger)
	if openErr != nil {
		t.Fatalf("NewSQLiteStore() error = %v", openErr)
	}

	var version int
	if err := store.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if currentSchemaVersion != 4 || version != 4 {
		t.Fatalf("schema version = %d (current %d), want 4", version, currentSchemaVersion)
	}

	// Roll the database back to the v3 shape and reopen it so the v4 migration
	// runs against an existing installation rather than a fresh file.
	if _, err := store.db.ExecContext(ctx, "DROP TABLE task_budgets"); err != nil {
		t.Fatalf("drop task_budgets: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "DROP INDEX IF EXISTS idx_cost_task"); err != nil {
		t.Fatalf("drop idx_cost_task: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE schema_migrations SET version = 3"); err != nil {
		t.Fatalf("downgrade schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewSQLiteStore(dsn, 0, logger)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen) error = %v", err)
	}
	t.Cleanup(func() {
		_ = reopened.Close()
	})

	var count int
	if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='task_budgets'").Scan(&count); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if count != 1 {
		t.Fatalf("task_budgets table count = %d, want 1", count)
	}
	if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_cost_task'").Scan(&count); err != nil {
		t.Fatalf("query sqlite_master for index: %v", err)
	}
	if count != 1 {
		t.Fatalf("idx_cost_task index count = %d, want 1", count)
	}
	if err := reopened.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema version after reopen: %v", err)
	}
	if version != 4 {
		t.Fatalf("schema version after reopen = %d, want 4", version)
	}
}

func TestSQLiteStore_TaskLifecycle(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()
	first := time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC)

	if err := store.UpsertTask(ctx, TaskRecord{}); err == nil {
		t.Fatal("UpsertTask(empty id) error = nil, want error")
	}

	_, found, getErr := store.GetTask(ctx, "missing")
	if getErr != nil {
		t.Fatalf("GetTask(missing) error = %v", getErr)
	}
	if found {
		t.Fatal("GetTask(missing) found = true, want false")
	}

	if err := store.UpsertTask(ctx, TaskRecord{
		TaskID:       " task-1 ",
		LastAgent:    "agent-a",
		SpentUSD:     0.5,
		LimitUSD:     2,
		RequestCount: 1,
		FirstSeenAt:  first,
		LastSeenAt:   first,
	}); err != nil {
		t.Fatalf("UpsertTask(insert) error = %v", err)
	}

	record, found, err := store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if !found {
		t.Fatal("GetTask() found = false, want true")
	}
	if record.TaskID != "task-1" || record.LastAgent != "agent-a" || record.SpentUSD != 0.5 || record.LimitUSD != 2 || record.RequestCount != 1 {
		t.Fatalf("record = %#v, want trimmed id and stored totals", record)
	}
	if !record.FirstSeenAt.Equal(first) || !record.LastSeenAt.Equal(first) {
		t.Fatalf("record timestamps = %v/%v, want %v", record.FirstSeenAt, record.LastSeenAt, first)
	}

	later := first.Add(time.Hour)
	if err := store.UpsertTask(ctx, TaskRecord{
		TaskID:       "task-1",
		LastAgent:    "agent-b",
		SpentUSD:     1.25,
		LimitUSD:     2,
		RequestCount: 3,
		FirstSeenAt:  later,
		LastSeenAt:   later,
	}); err != nil {
		t.Fatalf("UpsertTask(update) error = %v", err)
	}
	record, _, err = store.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask(after update) error = %v", err)
	}
	if record.SpentUSD != 1.25 || record.RequestCount != 3 || record.LastAgent != "agent-b" {
		t.Fatalf("updated record = %#v, want spent 1.25, 3 requests, agent-b", record)
	}
	if !record.FirstSeenAt.Equal(first) {
		t.Fatalf("FirstSeenAt = %v, want original %v to be kept", record.FirstSeenAt, first)
	}
	if !record.LastSeenAt.Equal(later) {
		t.Fatalf("LastSeenAt = %v, want %v", record.LastSeenAt, later)
	}

	if err := store.UpsertTask(ctx, TaskRecord{TaskID: "task-0", SpentUSD: 0.1}); err != nil {
		t.Fatalf("UpsertTask(defaults) error = %v", err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	if len(tasks) != 2 || tasks[0].TaskID != "task-0" || tasks[1].TaskID != "task-1" {
		t.Fatalf("ListTasks() = %#v, want task-0 then task-1", tasks)
	}
	if tasks[0].FirstSeenAt.IsZero() || tasks[0].LastSeenAt.IsZero() {
		t.Fatalf("ListTasks()[0] timestamps = %#v, want defaults filled in", tasks[0])
	}
}

func TestSQLiteStore_QueryCostsTaskFilterIsExact(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()
	base := time.Date(2026, time.August, 27, 10, 0, 0, 0, time.UTC)

	records := []CostRecord{
		{ID: "c1", Agent: "agent-a", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "task-1", InputTokens: 10, OutputTokens: 5, CostUSD: 0.10, CreatedAt: base},
		{ID: "c2", Agent: "agent-b", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "task-1", InputTokens: 20, OutputTokens: 5, CostUSD: 0.20, CreatedAt: base.Add(time.Minute)},
		{ID: "c3", Agent: "agent-a", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "task-10", InputTokens: 30, OutputTokens: 5, CostUSD: 0.40, CreatedAt: base.Add(2 * time.Minute)},
		{ID: "c4", Agent: "agent-a", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "", InputTokens: 40, OutputTokens: 5, CostUSD: 0.80, CreatedAt: base.Add(3 * time.Minute)},
		{ID: "c5", Agent: "agent-a", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "task_1", InputTokens: 50, OutputTokens: 5, CostUSD: 1.60, CreatedAt: base.Add(4 * time.Minute)},
	}
	for _, record := range records {
		if err := store.SaveCostRecord(ctx, record); err != nil {
			t.Fatalf("SaveCostRecord(%s) error = %v", record.ID, err)
		}
	}

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name      string
		query     CostQuery
		wantRows  int
		wantCost  float64
		wantAgent string
	}{
		{name: "exact task id spans agents", query: CostQuery{Task: "task-1", GroupBy: "none"}, wantRows: 2, wantCost: 0.30},
		{name: "exact task id combined with agent filter", query: CostQuery{Task: "task-1", Agent: "agent-b", GroupBy: "agent"}, wantRows: 1, wantCost: 0.20, wantAgent: "agent-b"},
		{name: "prefix does not match longer ids", query: CostQuery{Task: "task-", GroupBy: "none"}, wantRows: 0},
		{name: "like wildcards are literal", query: CostQuery{Task: "task-%", GroupBy: "none"}, wantRows: 0},
		{name: "underscore is literal not single-char wildcard", query: CostQuery{Task: "task_1", GroupBy: "none"}, wantRows: 1, wantCost: 1.60},
		{name: "quotes are parameterized not injected", query: CostQuery{Task: "task-1' OR '1'='1", GroupBy: "none"}, wantRows: 0},
		{name: "surrounding whitespace is trimmed", query: CostQuery{Task: "  task-10  ", GroupBy: "none"}, wantRows: 1, wantCost: 0.40},
		{name: "empty task filter returns everything", query: CostQuery{Task: "", GroupBy: "none"}, wantRows: 5, wantCost: 3.10},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows, err := store.QueryCosts(ctx, tt.query)
			if err != nil {
				t.Fatalf("QueryCosts() error = %v", err)
			}
			if len(rows) != tt.wantRows {
				t.Fatalf("len(rows) = %d, want %d (%#v)", len(rows), tt.wantRows, rows)
			}
			total := 0.0
			for _, row := range rows {
				total += row.CostUSD
			}
			if total < tt.wantCost-1e-9 || total > tt.wantCost+1e-9 {
				t.Fatalf("total cost = %v, want %v", total, tt.wantCost)
			}
			if tt.wantAgent != "" && rows[0].Agent != tt.wantAgent {
				t.Fatalf("rows[0].Agent = %q, want %q", rows[0].Agent, tt.wantAgent)
			}
		})
	}

	csvData, err := store.QueryCostsCSV(ctx, CostQuery{Task: "task-1", GroupBy: "agent"})
	if err != nil {
		t.Fatalf("QueryCostsCSV() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(csvData), "\n")
	if len(lines) != 3 {
		t.Fatalf("csv lines = %d, want header plus two agent rows: %q", len(lines), csvData)
	}
	if lines[0] != "agent,model,provider,requests,input_tokens,output_tokens,cost_usd" {
		t.Fatalf("csv header = %q, want unchanged export contract", lines[0])
	}
	if !strings.HasPrefix(lines[1], "agent-a,,openai,1,10,5,0.10000000") || !strings.HasPrefix(lines[2], "agent-b,,openai,1,20,5,0.20000000") {
		t.Fatalf("csv rows = %q, want only task-1 rows", lines[1:])
	}
}
