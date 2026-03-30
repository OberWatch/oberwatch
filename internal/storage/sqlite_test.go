package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/config"
)

func newStore(t *testing.T, retention time.Duration) *SQLiteStore {
	t.Helper()

	store, err := NewSQLiteStore(":memory:", retention, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("Close() error = %v", closeErr)
		}
	})
	return store
}

func TestSQLiteStore_SchemaAutoCreation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tableName string
	}{
		{name: "cost records table exists", tableName: "cost_records"},
		{name: "alerts table exists", tableName: "alerts"},
		{name: "budget snapshots table exists", tableName: "budget_snapshots"},
		{name: "settings table exists", tableName: "settings"},
		{name: "schema migrations table exists", tableName: "schema_migrations"},
	}

	store := newStore(t, 0)
	ctx := context.Background()

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var count int
			err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tt.tableName).Scan(&count)
			if err != nil {
				t.Fatalf("QueryRowContext() error = %v", err)
			}
			if count != 1 {
				t.Fatalf("table %q existence count = %d, want 1", tt.tableName, count)
			}
		})
	}

}

func TestSQLiteStore_SaveAndQueryCosts(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()
	base := time.Date(2026, time.March, 26, 10, 0, 0, 0, time.UTC)

	records := []CostRecord{
		{ID: "c1", Agent: "a1", Model: "gpt-4o", Provider: "openai", InputTokens: 100, OutputTokens: 50, CostUSD: 0.01, CreatedAt: base},
		{ID: "c2", Agent: "a2", Model: "claude-sonnet-4-6", Provider: "anthropic", InputTokens: 200, OutputTokens: 80, CostUSD: 0.02, CreatedAt: base.Add(20 * time.Minute)},
		{ID: "c3", Agent: "a1", Model: "gpt-4o", Provider: "openai", InputTokens: 150, OutputTokens: 60, CostUSD: 0.03, CreatedAt: base.Add(2 * time.Hour)},
	}
	for _, record := range records {
		if err := store.SaveCostRecord(ctx, record); err != nil {
			t.Fatalf("SaveCostRecord() error = %v", err)
		}
	}

	//nolint:govet // keep grouping test matrix explicit.
	tests := []struct {
		name       string
		query      CostQuery
		wantRows   int
		wantBucket string
	}{
		{name: "raw rows", query: CostQuery{}, wantRows: 3},
		{name: "filter by agent", query: CostQuery{Agent: "a1"}, wantRows: 2},
		{name: "filter by model", query: CostQuery{Model: "claude-sonnet-4-6"}, wantRows: 1},
		{name: "group by agent", query: CostQuery{GroupBy: "agent"}, wantRows: 2},
		{name: "group by model", query: CostQuery{GroupBy: "model"}, wantRows: 2},
		{name: "group by hour", query: CostQuery{GroupBy: "hour"}, wantRows: 2, wantBucket: "2026-03-26T10:00:00Z"},
		{name: "group by day", query: CostQuery{GroupBy: "day"}, wantRows: 1, wantBucket: "2026-03-26"},
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
				t.Fatalf("len(QueryCosts()) = %d, want %d", len(rows), tt.wantRows)
			}
			if tt.wantBucket != "" {
				found := false
				for _, row := range rows {
					if row.Bucket == tt.wantBucket {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected bucket %q in results %#v", tt.wantBucket, rows)
				}
			}
		})
	}

	csvOutput, err := store.QueryCostsCSV(ctx, CostQuery{GroupBy: "agent"})
	if err != nil {
		t.Fatalf("QueryCostsCSV() error = %v", err)
	}
	if csvOutput == "" || csvOutput[:5] != "agent" {
		t.Fatalf("csv output = %q, want csv header", csvOutput)
	}
}

func TestSQLiteStore_AgentLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 28, 14, 0, 0, 0, time.UTC)

	seed := AgentRecord{
		Name:                  "unknown",
		Status:                "active",
		BudgetLimitUSD:        15,
		BudgetSpentUSD:        2.5,
		BudgetPeriod:          config.BudgetPeriodDaily,
		ActionOnExceed:        config.BudgetActionAlert,
		DowngradeChain:        []string{"claude-sonnet-4-6", "claude-haiku-4-5"},
		DowngradeThresholdPct: 80,
		AlertThresholdsPct:    []float64{50, 80, 100},
		PeriodStartedAt:       now,
		PeriodResetsAt:        now.Add(24 * time.Hour),
		FirstSeenAt:           now,
		LastSeenAt:            now,
	}

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name    string
		prepare func(*testing.T, *SQLiteStore)
		assert  func(*testing.T, *SQLiteStore)
	}{
		{
			name: "upsert get and list agents",
			prepare: func(t *testing.T, store *SQLiteStore) {
				t.Helper()
				if err := store.UpsertAgent(context.Background(), seed); err != nil {
					t.Fatalf("UpsertAgent() error = %v", err)
				}
			},
			assert: func(t *testing.T, store *SQLiteStore) {
				t.Helper()
				record, found, err := store.GetAgent(context.Background(), "unknown")
				if err != nil {
					t.Fatalf("GetAgent() error = %v", err)
				}
				if !found {
					t.Fatal("GetAgent() found = false, want true")
				}
				if record.DowngradeThresholdPct != 80 {
					t.Fatalf("DowngradeThresholdPct = %v, want 80", record.DowngradeThresholdPct)
				}

				records, err := store.ListAgents(context.Background())
				if err != nil {
					t.Fatalf("ListAgents() error = %v", err)
				}
				if len(records) != 1 {
					t.Fatalf("len(ListAgents()) = %d, want 1", len(records))
				}
			},
		},
		{
			name: "rename agent migrates cost records",
			prepare: func(t *testing.T, store *SQLiteStore) {
				t.Helper()
				if err := store.UpsertAgent(context.Background(), seed); err != nil {
					t.Fatalf("UpsertAgent() error = %v", err)
				}
				if err := store.SaveCostRecord(context.Background(), CostRecord{
					ID:           "cost-1",
					Agent:        "unknown",
					Model:        "gpt-4o",
					Provider:     "openai",
					InputTokens:  10,
					OutputTokens: 5,
					CostUSD:      0.2,
					CreatedAt:    now,
				}); err != nil {
					t.Fatalf("SaveCostRecord() error = %v", err)
				}
			},
			assert: func(t *testing.T, store *SQLiteStore) {
				t.Helper()
				if err := store.RenameAgent(context.Background(), "unknown", "email-agent"); err != nil {
					t.Fatalf("RenameAgent() error = %v", err)
				}

				if _, found, err := store.GetAgent(context.Background(), "unknown"); err != nil {
					t.Fatalf("GetAgent(old) error = %v", err)
				} else if found {
					t.Fatal("old agent still exists after rename")
				}

				rows, err := store.QueryCosts(context.Background(), CostQuery{Agent: "email-agent", GroupBy: "agent"})
				if err != nil {
					t.Fatalf("QueryCosts(renamed) error = %v", err)
				}
				if len(rows) != 1 {
					t.Fatalf("len(QueryCosts(renamed)) = %d, want 1", len(rows))
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newStore(t, 0)
			tt.prepare(t, store)
			tt.assert(t, store)
		})
	}
}

func TestSQLiteStore_AgentDefaultsAndRenameConflict(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()

	if err := store.UpsertAgent(ctx, AgentRecord{Name: "agent-a"}); err != nil {
		t.Fatalf("UpsertAgent(defaults) error = %v", err)
	}
	if err := store.UpsertAgent(ctx, AgentRecord{
		Name:            "agent-b",
		Status:          "active",
		BudgetPeriod:    config.BudgetPeriodDaily,
		ActionOnExceed:  config.BudgetActionAlert,
		FirstSeenAt:     time.Now().UTC(),
		LastSeenAt:      time.Now().UTC(),
		PeriodStartedAt: time.Now().UTC(),
		PeriodResetsAt:  time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertAgent(second seed) error = %v", err)
	}

	record, found, err := store.GetAgent(ctx, "agent-a")
	if err != nil {
		t.Fatalf("GetAgent(defaults) error = %v", err)
	}
	if !found {
		t.Fatal("GetAgent(defaults) found = false, want true")
	}
	if record.Status != "active" {
		t.Fatalf("Status = %q, want active", record.Status)
	}
	if record.BudgetPeriod != config.BudgetPeriodDaily {
		t.Fatalf("BudgetPeriod = %q, want daily", record.BudgetPeriod)
	}
	if record.ActionOnExceed != config.BudgetActionAlert {
		t.Fatalf("ActionOnExceed = %q, want alert", record.ActionOnExceed)
	}
	if record.DowngradeThresholdPct != 80 {
		t.Fatalf("DowngradeThresholdPct = %v, want 80", record.DowngradeThresholdPct)
	}

	err = store.RenameAgent(ctx, "agent-a", "agent-b")
	if err != ErrAgentExists {
		t.Fatalf("RenameAgent(conflict) error = %v, want %v", err, ErrAgentExists)
	}
}

func TestSQLiteStore_SaveAndQueryAlerts(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()
	now := time.Date(2026, time.March, 26, 11, 0, 0, 0, time.UTC)

	entries := []alert.Alert{
		{ID: "a1", Type: alert.TypeBudgetThreshold, Agent: "agent-a", Message: "threshold", Severity: "warning", Timestamp: now, Data: map[string]any{"threshold": 80}},
		{ID: "a2", Type: alert.TypeAgentKilled, Agent: "agent-b", Message: "killed", Severity: "critical", Timestamp: now.Add(time.Minute)},
	}
	for _, entry := range entries {
		if err := store.SaveAlert(ctx, entry); err != nil {
			t.Fatalf("SaveAlert() error = %v", err)
		}
	}

	//nolint:govet // keep alert query table explicit.
	tests := []struct {
		name     string
		query    AlertQuery
		wantRows int
	}{
		{name: "all alerts", query: AlertQuery{}, wantRows: 2},
		{name: "filter by agent", query: AlertQuery{Agent: "agent-a"}, wantRows: 1},
		{name: "filter by type", query: AlertQuery{Type: alert.TypeAgentKilled}, wantRows: 1},
		{name: "limit", query: AlertQuery{Limit: 1}, wantRows: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results, err := store.QueryAlerts(ctx, tt.query)
			if err != nil {
				t.Fatalf("QueryAlerts() error = %v", err)
			}
			if len(results) != tt.wantRows {
				t.Fatalf("len(QueryAlerts()) = %d, want %d", len(results), tt.wantRows)
			}
		})
	}

	results, err := store.QueryAlerts(ctx, AlertQuery{Agent: "agent-a"})
	if err != nil {
		t.Fatalf("QueryAlerts(agent-a) error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(QueryAlerts(agent-a)) = %d, want 1", len(results))
	}
	if results[0].Data["threshold"] != float64(80) {
		t.Fatalf("results[0].Data[threshold] = %v, want 80", results[0].Data["threshold"])
	}
}

func TestSQLiteStore_BudgetSnapshotSaveRestore(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()

	snapshot := BudgetSnapshot{
		Agent:           "agent-a",
		Period:          "daily",
		PeriodStartedAt: time.Date(2026, time.March, 26, 0, 0, 0, 0, time.UTC),
		PeriodResetsAt:  time.Date(2026, time.March, 27, 0, 0, 0, 0, time.UTC),
		SpentUSD:        12.5,
		LastAlertedPct:  80,
		Killed:          true,
		UpdatedAt:       time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC),
	}
	if err := store.SaveBudgetSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("SaveBudgetSnapshot() error = %v", err)
	}

	loaded, err := store.LoadBudgetSnapshots(ctx)
	if err != nil {
		t.Fatalf("LoadBudgetSnapshots() error = %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(LoadBudgetSnapshots()) = %d, want 1", len(loaded))
	}
	if loaded[0].Agent != snapshot.Agent || !loaded[0].Killed {
		t.Fatalf("loaded snapshot = %#v, want agent=%q killed=true", loaded[0], snapshot.Agent)
	}
}

func TestSQLiteStore_RetentionCleanup(t *testing.T) {
	t.Parallel()

	store := newStore(t, 24*time.Hour)
	ctx := context.Background()
	now := time.Now().UTC()

	records := []CostRecord{
		{ID: "old", Agent: "a", Model: "m", Provider: "p", InputTokens: 1, OutputTokens: 1, CostUSD: 1, CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "new", Agent: "a", Model: "m", Provider: "p", InputTokens: 1, OutputTokens: 1, CostUSD: 1, CreatedAt: now.Add(-1 * time.Hour)},
	}
	for _, record := range records {
		if err := store.SaveCostRecord(ctx, record); err != nil {
			t.Fatalf("SaveCostRecord() error = %v", err)
		}
	}

	alerts := []alert.Alert{
		{ID: "old-alert", Type: alert.TypeBudgetThreshold, Agent: "a", Message: "old", Severity: "warning", Timestamp: now.Add(-48 * time.Hour)},
		{ID: "new-alert", Type: alert.TypeBudgetThreshold, Agent: "a", Message: "new", Severity: "warning", Timestamp: now.Add(-1 * time.Hour)},
	}
	for _, entry := range alerts {
		if err := store.SaveAlert(ctx, entry); err != nil {
			t.Fatalf("SaveAlert() error = %v", err)
		}
	}

	if err := store.CleanupRetention(ctx); err != nil {
		t.Fatalf("CleanupRetention() error = %v", err)
	}

	costs, err := store.QueryCosts(ctx, CostQuery{})
	if err != nil {
		t.Fatalf("QueryCosts() error = %v", err)
	}
	if len(costs) != 1 {
		t.Fatalf("remaining costs = %d, want 1", len(costs))
	}

	remainingAlerts, err := store.QueryAlerts(ctx, AlertQuery{})
	if err != nil {
		t.Fatalf("QueryAlerts() error = %v", err)
	}
	if len(remainingAlerts) != 1 {
		t.Fatalf("remaining alerts = %d, want 1", len(remainingAlerts))
	}
}

func TestSQLiteStore_SettingsCRUD(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()

	tests := []struct {
		name      string
		key       string
		value     string
		wantFound bool
	}{
		{name: "missing setting returns not found", key: "missing", wantFound: false},
		{name: "stored setting returns found", key: "admin_username", value: "admin", wantFound: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.wantFound {
				if err := store.SetSetting(ctx, tt.key, tt.value); err != nil {
					t.Fatalf("SetSetting() error = %v", err)
				}
			}

			got, found, err := store.GetSetting(ctx, tt.key)
			if err != nil {
				t.Fatalf("GetSetting() error = %v", err)
			}
			if found != tt.wantFound {
				t.Fatalf("GetSetting() found = %v, want %v", found, tt.wantFound)
			}
			if found && got != tt.value {
				t.Fatalf("GetSetting() value = %q, want %q", got, tt.value)
			}
		})
	}

	if err := store.SetSetting(ctx, "session_token", "first"); err != nil {
		t.Fatalf("SetSetting(first) error = %v", err)
	}
	if err := store.SetSetting(ctx, "session_token", "second"); err != nil {
		t.Fatalf("SetSetting(second) error = %v", err)
	}
	got, found, err := store.GetSetting(ctx, "session_token")
	if err != nil {
		t.Fatalf("GetSetting(updated) error = %v", err)
	}
	if !found || got != "second" {
		t.Fatalf("GetSetting(updated) = (%q, %v), want (%q, true)", got, found, "second")
	}

	deleteErr := store.DeleteSetting(ctx, "session_token")
	if deleteErr != nil {
		t.Fatalf("DeleteSetting() error = %v", deleteErr)
	}
	_, found, err = store.GetSetting(ctx, "session_token")
	if err != nil {
		t.Fatalf("GetSetting(after delete) error = %v", err)
	}
	if found {
		t.Fatal("GetSetting(after delete) found = true, want false")
	}
}

func TestSQLiteStore_ConcurrentWrites(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()

	const workers = 20
	const writesPerWorker = 25

	var wg sync.WaitGroup
	errCh := make(chan error, workers*writesPerWorker)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writesPerWorker; i++ {
				record := CostRecord{
					ID:           fmt.Sprintf("%d-%d", worker, i),
					Agent:        "agent",
					Model:        "model",
					Provider:     "openai",
					InputTokens:  10,
					OutputTokens: 5,
					CostUSD:      0.01,
					CreatedAt:    time.Now().UTC(),
				}
				if err := store.SaveCostRecord(ctx, record); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent SaveCostRecord() error = %v", err)
		}
	}

	rows, err := store.QueryCosts(ctx, CostQuery{})
	if err != nil {
		t.Fatalf("QueryCosts() error = %v", err)
	}
	want := workers * writesPerWorker
	if len(rows) != want {
		t.Fatalf("len(QueryCosts()) = %d, want %d", len(rows), want)
	}
}

func TestBufferedCostWriter_EnqueueAndClose(t *testing.T) {
	t.Parallel()

	//nolint:govet // helper struct keeps embedded interface and captured state together.
	//nolint:govet // helper struct keeps embedded interface and captured state together.
	collector := &struct {
		Store
		mu    sync.Mutex
		saved []CostRecord
	}{
		Store: nil,
	}
	collector.Store = storeFunc{
		saveCost: func(_ context.Context, record CostRecord) error {
			collector.mu.Lock()
			collector.saved = append(collector.saved, record)
			collector.mu.Unlock()
			return nil
		},
	}

	writer := NewBufferedCostWriter(collector.Store, 2, nil)
	writer.Enqueue(CostRecord{ID: "one", Agent: "a"})
	writer.Enqueue(CostRecord{ID: "two", Agent: "a"})
	writer.Close()

	collector.mu.Lock()
	count := len(collector.saved)
	collector.mu.Unlock()
	if count == 0 {
		t.Fatal("buffered writer saved count = 0, want > 0")
	}
}

func TestSQLiteStore_BranchCoverage(t *testing.T) {
	t.Parallel()

	store := newStore(t, 0)
	ctx := context.Background()

	//nolint:govet // keep branch-case table explicit.
	tests := []struct {
		name    string
		runCase func(*testing.T)
	}{
		{
			name: "save cost auto id and timestamp",
			runCase: func(t *testing.T) {
				t.Helper()
				record := CostRecord{
					Agent:        "agent-auto",
					Model:        "gpt-4o",
					Provider:     "openai",
					InputTokens:  10,
					OutputTokens: 5,
					CostUSD:      0.1,
				}
				if err := store.SaveCostRecord(ctx, record); err != nil {
					t.Fatalf("SaveCostRecord() error = %v", err)
				}
				results, err := store.QueryCosts(ctx, CostQuery{Agent: "agent-auto"})
				if err != nil {
					t.Fatalf("QueryCosts() error = %v", err)
				}
				if len(results) != 1 {
					t.Fatalf("len(QueryCosts()) = %d, want 1", len(results))
				}
			},
		},
		{
			name: "unsupported group by returns error",
			runCase: func(t *testing.T) {
				t.Helper()
				_, err := store.QueryCosts(ctx, CostQuery{GroupBy: "invalid"})
				if err == nil {
					t.Fatal("QueryCosts() error = nil, want non-nil")
				}
			},
		},
		{
			name: "save budget snapshot empty agent fails",
			runCase: func(t *testing.T) {
				t.Helper()
				err := store.SaveBudgetSnapshot(ctx, BudgetSnapshot{})
				if err == nil {
					t.Fatal("SaveBudgetSnapshot() error = nil, want non-nil")
				}
			},
		},
		{
			name: "cleanup retention disabled is no-op",
			runCase: func(t *testing.T) {
				t.Helper()
				if err := store.CleanupRetention(ctx); err != nil {
					t.Fatalf("CleanupRetention() error = %v", err)
				}
			},
		},
		{
			name: "format cost aggregates csv",
			runCase: func(t *testing.T) {
				t.Helper()
				csvText, err := FormatCostAggregatesCSV([]CostAggregate{{Agent: "a", Requests: 1, CostUSD: 0.1}})
				if err != nil {
					t.Fatalf("FormatCostAggregatesCSV() error = %v", err)
				}
				if !strings.Contains(csvText, "agent") || !strings.Contains(csvText, "a") {
					t.Fatalf("csv output = %q, want header and row", csvText)
				}
			},
		},
		{
			name: "generate id and bool helpers",
			runCase: func(t *testing.T) {
				t.Helper()
				if got := generateID("x"); got == "" || !strings.HasPrefix(got, "x_") {
					t.Fatalf("generateID() = %q, want non-empty prefixed ID", got)
				}
				if got := boolToInt(true); got != 1 {
					t.Fatalf("boolToInt(true) = %d, want 1", got)
				}
				if !intToBool(1) || intToBool(0) {
					t.Fatal("intToBool() conversion mismatch")
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.runCase(t)
		})
	}
}

func TestBufferedCostWriter_DropsWhenFull(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	//nolint:govet // helper struct keeps embedded interface and captured state together.
	collector := &struct {
		Store
		mu    sync.Mutex
		saved []CostRecord
	}{
		Store: nil,
	}
	collector.Store = storeFunc{
		saveCost: func(_ context.Context, record CostRecord) error {
			once.Do(func() { close(started) })
			collector.mu.Lock()
			collector.saved = append(collector.saved, record)
			collector.mu.Unlock()
			<-block
			return nil
		},
	}

	writer := NewBufferedCostWriter(collector.Store, 1, nil)
	writer.Enqueue(CostRecord{ID: "one"})
	<-started
	writer.Enqueue(CostRecord{ID: "two"})
	writer.Enqueue(CostRecord{ID: "three"}) // should drop on full buffer
	close(block)
	writer.Close()

	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.saved) != 2 {
		t.Fatalf("saved count = %d, want 2 (third dropped)", len(collector.saved))
	}
}

func TestSQLiteStore_DefaultDSNAndAlertMarshalError(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "default dsn creates local database file and alert marshal errors are returned"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			originalWD, err := os.Getwd()
			if err != nil {
				t.Fatalf("Getwd() error = %v", err)
			}
			if chdirErr := os.Chdir(tempDir); chdirErr != nil {
				t.Fatalf("Chdir(tempDir) error = %v", chdirErr)
			}
			t.Cleanup(func() {
				if chdirErr := os.Chdir(originalWD); chdirErr != nil {
					t.Fatalf("Chdir(originalWD) error = %v", chdirErr)
				}
			})

			store, err := NewSQLiteStore("", time.Hour, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore(default dsn) error = %v", err)
			}
			t.Cleanup(func() {
				if closeErr := store.Close(); closeErr != nil {
					t.Fatalf("Close() error = %v", closeErr)
				}
			})

			dbPath := filepath.Join(tempDir, "oberwatch.db")
			if _, statErr := os.Stat(dbPath); statErr != nil {
				t.Fatalf("expected db file at %q: %v", dbPath, statErr)
			}

			err = store.SaveAlert(context.Background(), alert.Alert{
				Agent:     "agent-a",
				Type:      alert.TypeBudgetThreshold,
				Message:   "bad data",
				Severity:  "warning",
				Timestamp: time.Now().UTC(),
				Data: map[string]any{
					"bad": make(chan int),
				},
			})
			if err == nil {
				t.Fatal("SaveAlert() error = nil, want marshal error")
			}
		})
	}
}

func TestSQLiteStore_SaveBudgetSnapshotDefaultTimestamps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "zero timestamps are populated during save"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newStore(t, 0)
			ctx := context.Background()

			snapshot := BudgetSnapshot{
				Agent:  "agent-zero",
				Period: "daily",
			}
			if err := store.SaveBudgetSnapshot(ctx, snapshot); err != nil {
				t.Fatalf("SaveBudgetSnapshot() error = %v", err)
			}

			loaded, err := store.LoadBudgetSnapshots(ctx)
			if err != nil {
				t.Fatalf("LoadBudgetSnapshots() error = %v", err)
			}
			if len(loaded) != 1 {
				t.Fatalf("len(LoadBudgetSnapshots()) = %d, want 1", len(loaded))
			}
			if loaded[0].PeriodStartedAt.IsZero() || loaded[0].PeriodResetsAt.IsZero() || loaded[0].UpdatedAt.IsZero() {
				t.Fatalf("loaded snapshot timestamps = %#v, want non-zero values", loaded[0])
			}
		})
	}
}

func TestSQLiteStore_InvalidDSNAndNilWriterSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "invalid dsn returns error and nil writer methods are safe"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewSQLiteStore(t.TempDir(), 0, nil); err == nil {
				t.Fatal("NewSQLiteStore(directory dsn) error = nil, want non-nil")
			}

			var writer *BufferedCostWriter
			writer.Enqueue(CostRecord{ID: "x"})
			writer.Close()
		})
	}
}

type storeFunc struct {
	saveCost func(context.Context, CostRecord) error
}

func (s storeFunc) SaveCostRecord(ctx context.Context, record CostRecord) error {
	if s.saveCost == nil {
		return nil
	}
	return s.saveCost(ctx, record)
}

func (s storeFunc) QueryCosts(context.Context, CostQuery) ([]CostAggregate, error) {
	return nil, nil
}

func (s storeFunc) SaveAlert(context.Context, alert.Alert) error {
	return nil
}

func (s storeFunc) QueryAlerts(context.Context, AlertQuery) ([]alert.Alert, error) {
	return nil, nil
}

func (s storeFunc) UpsertAgent(context.Context, AgentRecord) error {
	return nil
}

func (s storeFunc) GetAgent(context.Context, string) (AgentRecord, bool, error) {
	return AgentRecord{}, false, nil
}

func (s storeFunc) ListAgents(context.Context) ([]AgentRecord, error) {
	return nil, nil
}

func (s storeFunc) RenameAgent(context.Context, string, string) error {
	return nil
}

func (s storeFunc) SaveBudgetSnapshot(context.Context, BudgetSnapshot) error {
	return nil
}

func (s storeFunc) LoadBudgetSnapshots(context.Context) ([]BudgetSnapshot, error) {
	return nil, nil
}

func (s storeFunc) GetSetting(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (s storeFunc) SetSetting(context.Context, string, string) error {
	return nil
}

func (s storeFunc) DeleteSetting(context.Context, string) error {
	return nil
}
