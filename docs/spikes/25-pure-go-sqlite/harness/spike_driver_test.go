//go:build ignore

package storage

// Spike harness for issue #25. This file is copied into a scratch copy of
// internal/storage by docs/spikes/25-pure-go-sqlite/run.sh and run against
// each candidate driver. It is not part of the production test suite.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
)

func spikeLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func spikeFileStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(path, 0, spikeLogger())
	if err != nil {
		t.Fatalf("NewSQLiteStore(%q) error = %v", path, err)
	}
	return store
}

func spikeSeed(t testing.TB, store *SQLiteStore, n int) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Duration(n) * time.Second)
	for i := 0; i < n; i++ {
		rec := CostRecord{
			ID:           fmt.Sprintf("seed-%d", i),
			Agent:        fmt.Sprintf("agent-%d", i%5),
			Model:        "gpt-4o",
			Provider:     "openai",
			InputTokens:  100,
			OutputTokens: 50,
			CostUSD:      0.001,
			TraceID:      fmt.Sprintf("trace-%d", i),
			TaskID:       fmt.Sprintf("task-%d", i%7),
			CreatedAt:    base.Add(time.Duration(i) * time.Second),
		}
		if err := store.SaveCostRecord(ctx, rec); err != nil {
			t.Fatalf("SaveCostRecord() error = %v", err)
		}
	}
}

// downgradeSchema rewinds a freshly migrated v4 database to an older version by
// reversing the migrations in sqlite.go, so the legacy open path can be tested.
func downgradeSchema(t *testing.T, db *sql.DB, target int) {
	t.Helper()
	steps := map[int][]string{
		4: {"DROP INDEX IF EXISTS idx_cost_task", "DROP INDEX IF EXISTS idx_task_budgets_last_seen", "DROP TABLE IF EXISTS task_budgets"},
		3: {"DROP INDEX IF EXISTS idx_agents_status", "DROP INDEX IF EXISTS idx_agents_last_seen", "DROP TABLE IF EXISTS agents"},
		2: {"DROP TABLE IF EXISTS settings"},
	}
	for v := currentSchemaVersion; v > target; v-- {
		for _, stmt := range steps[v] {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("downgrade v%d %q: %v", v, stmt, err)
			}
		}
	}
	if _, err := db.Exec("UPDATE schema_migrations SET version = ?", target); err != nil {
		t.Fatalf("set version %d: %v", target, err)
	}
}

func spikeTables(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type IN ('table','index') ORDER BY name")
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	return out
}

// TestSpike_LegacySchemaMigration builds a v1, v2 and v3 database on disk and
// reopens each through NewSQLiteStore, checking the migration to v4 and that
// pre-existing rows survive.
func TestSpike_LegacySchemaMigration(t *testing.T) {
	for _, from := range []int{1, 2, 3} {
		from := from
		t.Run(fmt.Sprintf("v%d_to_v%d", from, currentSchemaVersion), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "legacy.db")

			store := spikeFileStore(t, path)
			spikeSeed(t, store, 25)
			if err := store.SaveAlert(context.Background(), alert.Alert{Type: alert.TypeBudgetThreshold, Agent: "agent-1", Message: "m", Severity: "warning"}); err != nil {
				t.Fatalf("SaveAlert: %v", err)
			}
			downgradeSchema(t, store.db, from)
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			reopened := spikeFileStore(t, path)
			defer reopened.Close()

			var version int
			if err := reopened.db.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if version != currentSchemaVersion {
				t.Fatalf("schema version after reopen = %d, want %d", version, currentSchemaVersion)
			}
			tables := spikeTables(t, reopened.db)
			for _, want := range []string{"cost_records", "alerts", "budget_snapshots", "settings", "agents", "task_budgets", "idx_cost_task", "idx_task_budgets_last_seen", "idx_agents_status"} {
				if !tables[want] {
					t.Errorf("missing %q after migration from v%d", want, from)
				}
			}
			var count int
			if err := reopened.db.QueryRow("SELECT COUNT(*) FROM cost_records").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 25 {
				t.Fatalf("cost_records after migration = %d, want 25", count)
			}
			if err := reopened.UpsertTask(context.Background(), TaskRecord{TaskID: "t1", LimitUSD: 1}); err != nil {
				t.Fatalf("UpsertTask on migrated db: %v", err)
			}
		})
	}
}

// TestSpike_WriteFixture writes a v1/v2/v3/v4 database set into SPIKE_FIXTURE_OUT
// so the run script can open files produced by one driver with the other.
func TestSpike_WriteFixture(t *testing.T) {
	out := os.Getenv("SPIKE_FIXTURE_OUT")
	if out == "" {
		t.Skip("SPIKE_FIXTURE_OUT not set")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int{1, 2, 3, 4} {
		path := filepath.Join(out, fmt.Sprintf("v%d.db", v))
		_ = os.Remove(path)
		_ = os.Remove(path + "-wal")
		_ = os.Remove(path + "-shm")
		store := spikeFileStore(t, path)
		spikeSeed(t, store, 100)
		if err := store.SaveAlert(context.Background(), alert.Alert{Type: alert.TypeBudgetThreshold, Agent: "agent-1", Message: "m", Severity: "warning"}); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveBudgetSnapshot(context.Background(), BudgetSnapshot{Agent: "agent-1", Period: "daily", SpentUSD: 1}); err != nil {
			t.Fatal(err)
		}
		if v >= 2 {
			if err := store.SetSetting(context.Background(), "k", "v"); err != nil {
				t.Fatal(err)
			}
		}
		if v >= 3 {
			if err := store.UpsertAgent(context.Background(), AgentRecord{Name: "agent-1"}); err != nil {
				t.Fatal(err)
			}
		}
		if v >= 4 {
			if err := store.UpsertTask(context.Background(), TaskRecord{TaskID: "task-1", LimitUSD: 1}); err != nil {
				t.Fatal(err)
			}
		}
		downgradeSchema(t, store.db, v)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSpike_OpenFixture opens fixture files from SPIKE_FIXTURE_IN (written by
// another driver build) and checks migration plus row counts.
func TestSpike_OpenFixture(t *testing.T) {
	in := os.Getenv("SPIKE_FIXTURE_IN")
	if in == "" {
		t.Skip("SPIKE_FIXTURE_IN not set")
	}
	for _, v := range []int{1, 2, 3, 4} {
		src := filepath.Join(in, fmt.Sprintf("v%d.db", v))
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		path := filepath.Join(t.TempDir(), "fixture.db")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		store := spikeFileStore(t, path)
		var version, costs, alerts int
		if err := store.db.QueryRow("SELECT version FROM schema_migrations").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRow("SELECT COUNT(*) FROM cost_records").Scan(&costs); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRow("SELECT COUNT(*) FROM alerts").Scan(&alerts); err != nil {
			t.Fatal(err)
		}
		if version != currentSchemaVersion || costs != 100 || alerts != 1 {
			t.Fatalf("fixture v%d: version=%d costs=%d alerts=%d", v, version, costs, alerts)
		}
		aggs, err := store.QueryCosts(context.Background(), CostQuery{})
		if err != nil || len(aggs) == 0 {
			t.Fatalf("QueryCosts on fixture v%d: %v (%d rows)", v, err, len(aggs))
		}
		var integrity string
		if err := store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity_check fixture v%d = %q, %v", v, integrity, err)
		}
		t.Logf("fixture v%d opened: version=%d costs=%d integrity=%s", v, version, costs, integrity)
		store.Close()
	}
}

// TestSpike_ConstraintErrorMapping records the raw result code each driver
// reports for a UNIQUE violation and checks that isSQLiteConstraint classifies
// it. This is the only driver-coupled branch in internal/storage, so the exact
// code (primary 19 vs extended 2067) is part of the port contract.
func TestSpike_ConstraintErrorMapping(t *testing.T) {
	store := spikeFileStore(t, filepath.Join(t.TempDir(), "constraint.db"))
	defer store.Close()
	ctx := context.Background()

	if _, err := store.db.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES ('dup', '1')"); err != nil {
		t.Fatal(err)
	}
	_, err := store.db.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES ('dup', '2')")
	if err == nil {
		t.Fatal("duplicate primary key insert succeeded")
	}
	t.Logf("driver=%s unique_violation raw_result_code=%d masked=%d err=%v",
		sqliteDriverLabel, sqliteRawResultCode(err), sqliteRawResultCode(err)&0xff, err)
	if !isSQLiteConstraint(err) {
		t.Fatalf("isSQLiteConstraint() = false for a UNIQUE violation (%v)", err)
	}

	// A non-constraint failure must not be classified as a constraint error.
	_, err = store.db.ExecContext(ctx, "SELECT * FROM table_that_does_not_exist")
	if err == nil {
		t.Fatal("query against a missing table succeeded")
	}
	t.Logf("driver=%s missing_table raw_result_code=%d err=%v", sqliteDriverLabel, sqliteRawResultCode(err), err)
	if isSQLiteConstraint(err) {
		t.Fatalf("isSQLiteConstraint() = true for a non-constraint error (%v)", err)
	}

	// Same check through the production ErrAgentExists path.
	if err := store.UpsertAgent(ctx, AgentRecord{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAgent(ctx, AgentRecord{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameAgent(ctx, "a", "b"); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("RenameAgent onto an existing name = %v, want ErrAgentExists", err)
	}
}

// TestSpike_PragmasAndShutdown checks that WAL, synchronous and busy_timeout
// are actually applied by the driver and that Close checkpoints the WAL.
func TestSpike_PragmasAndShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	store := spikeFileStore(t, path)

	var journal string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	var sync int
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	var busy int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	var sqliteVersion string
	if err := store.db.QueryRow("SELECT sqlite_version()").Scan(&sqliteVersion); err != nil {
		t.Fatal(err)
	}
	t.Logf("sqlite_version=%s journal_mode=%s synchronous=%d busy_timeout=%d", sqliteVersion, journal, sync, busy)
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}
	if sync != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", sync)
	}
	if busy != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busy)
	}

	spikeSeed(t, store, 50)
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Errorf("expected WAL file while open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("WAL file still present after Close (err=%v)", err)
	}
	if _, err := os.Stat(path + "-shm"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SHM file still present after Close (err=%v)", err)
	}

	// Reopen: the data must be intact without the WAL sidecar.
	reopened := spikeFileStore(t, path)
	defer reopened.Close()
	var count int
	if err := reopened.db.QueryRow("SELECT COUNT(*) FROM cost_records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 50 {
		t.Fatalf("rows after reopen = %d, want 50", count)
	}
}

// TestSpike_BusyTimeoutAcrossConnections opens two stores on the same file,
// holds a write transaction on one and writes from the other. The second
// writer must block on busy_timeout and then succeed once the lock is released.
func TestSpike_BusyTimeoutAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	a := spikeFileStore(t, path)
	defer a.Close()
	b := spikeFileStore(t, path)
	defer b.Close()

	ctx := context.Background()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO settings(key, value) VALUES ('lock', '1')"); err != nil {
		t.Fatal(err)
	}

	const hold = 400 * time.Millisecond
	release := make(chan struct{})
	go func() {
		time.Sleep(hold)
		_ = tx.Commit()
		close(release)
	}()

	start := time.Now()
	err = b.SaveCostRecord(ctx, CostRecord{ID: "busy-1", Agent: "a", Model: "m", Provider: "p", CreatedAt: time.Now().UTC()})
	elapsed := time.Since(start)
	<-release
	if err != nil {
		t.Fatalf("second writer failed instead of waiting on busy_timeout: %v (after %s)", err, elapsed)
	}
	if elapsed < hold/2 {
		t.Fatalf("second writer returned after %s; expected it to wait for the held lock (~%s)", elapsed, hold)
	}
	t.Logf("second writer waited %s for a %s lock hold", elapsed, hold)

	// Now hold longer than busy_timeout and expect a busy error, not a hang.
	tx2, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.Exec("INSERT INTO settings(key, value) VALUES ('lock2', '1')"); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	err = b.SaveCostRecord(ctx, CostRecord{ID: "busy-2", Agent: "a", Model: "m", Provider: "p", CreatedAt: time.Now().UTC()})
	elapsed = time.Since(start)
	_ = tx2.Rollback()
	if err == nil {
		t.Fatalf("second writer succeeded while lock held for longer than busy_timeout")
	}
	t.Logf("held lock > busy_timeout: writer error after %s: %v", elapsed, err)
	if elapsed < 4*time.Second || elapsed > 8*time.Second {
		t.Errorf("busy wait %s not close to the 5s busy_timeout", elapsed)
	}
}

// TestSpike_ConcurrentReadersAndWriters mixes writers, readers, RenameAgent
// and retention cleanup through the single pooled connection.
func TestSpike_ConcurrentReadersAndWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conc.db")
	store, err := NewSQLiteStore(path, time.Hour, spikeLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.UpsertAgent(ctx, AgentRecord{Name: "old"}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 1000)
	for w := 0; w < 8; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				agent := "old"
				if i%2 == 0 {
					agent = fmt.Sprintf("agent-%d", w)
				}
				if err := store.SaveCostRecord(ctx, CostRecord{ID: fmt.Sprintf("c-%d-%d", w, i), Agent: agent, Model: "m", Provider: "p", CostUSD: 0.01, CreatedAt: time.Now().UTC()}); err != nil {
					errCh <- err
				}
			}
		}()
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				if _, err := store.QueryCosts(ctx, CostQuery{}); err != nil {
					errCh <- err
				}
				if _, err := store.ListAgents(ctx); err != nil {
					errCh <- err
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if err := store.CleanupRetention(ctx); err != nil {
				errCh <- err
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent op error: %v", err)
	}

	if err := store.RenameAgent(ctx, "old", "new"); err != nil {
		t.Fatalf("RenameAgent: %v", err)
	}
	var oldCount, newCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM cost_records WHERE agent='old'").Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT COUNT(*) FROM cost_records WHERE agent='new'").Scan(&newCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 || newCount != 200 {
		t.Fatalf("rename moved rows old=%d new=%d, want 0/200", oldCount, newCount)
	}
	if err := store.UpsertAgent(ctx, AgentRecord{Name: "agent-0"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameAgent(ctx, "new", "agent-0"); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("RenameAgent conflict error = %v, want ErrAgentExists (constraint mapping)", err)
	}
}

// TestSpike_ContextCancellation checks the driver honours a cancelled context.
func TestSpike_ContextCancellation(t *testing.T) {
	store := spikeFileStore(t, filepath.Join(t.TempDir(), "ctx.db"))
	defer store.Close()
	spikeSeed(t, store, 200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.QueryCosts(ctx, CostQuery{})
	if err == nil {
		t.Fatal("QueryCosts with cancelled context returned nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Logf("note: error does not wrap context.Canceled: %v", err)
	}
}

// TestSpike_MemoryFootprint reports heap growth for a 20k-row insert and a
// full aggregate query, for comparison between drivers.
func TestSpike_MemoryFootprint(t *testing.T) {
	store := spikeFileStore(t, filepath.Join(t.TempDir(), "mem.db"))
	defer store.Close()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	spikeSeed(t, store, 20000)
	if _, err := store.QueryCosts(context.Background(), CostQuery{}); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("heap_inuse_before=%dKB heap_inuse_after=%dKB sys_before=%dKB sys_after=%dKB total_alloc_delta=%dKB",
		before.HeapInuse/1024, after.HeapInuse/1024, before.Sys/1024, after.Sys/1024, (after.TotalAlloc-before.TotalAlloc)/1024)
}

func BenchmarkSpike_SaveCostRecord(b *testing.B) {
	store, err := NewSQLiteStore(filepath.Join(b.TempDir(), "bench.db"), 0, spikeLogger())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.SaveCostRecord(ctx, CostRecord{ID: fmt.Sprintf("b-%d", i), Agent: "a", Model: "m", Provider: "p", InputTokens: 1, OutputTokens: 1, CostUSD: 0.01, CreatedAt: time.Now().UTC()}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSpike_SaveCostRecordParallel(b *testing.B) {
	store, err := NewSQLiteStore(filepath.Join(b.TempDir(), "benchp.db"), 0, spikeLogger())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	var n int64
	var mu sync.Mutex
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			mu.Lock()
			n++
			id := n
			mu.Unlock()
			if err := store.SaveCostRecord(ctx, CostRecord{ID: fmt.Sprintf("p-%d", id), Agent: "a", Model: "m", Provider: "p", CostUSD: 0.01, CreatedAt: time.Now().UTC()}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkSpike_QueryCosts10k(b *testing.B) {
	store, err := NewSQLiteStore(filepath.Join(b.TempDir(), "benchq.db"), 0, spikeLogger())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	spikeSeed(b, store, 10000)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.QueryCosts(ctx, CostQuery{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSpike_UpsertAgent(b *testing.B) {
	store, err := NewSQLiteStore(filepath.Join(b.TempDir(), "bencha.db"), 0, spikeLogger())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.UpsertAgent(ctx, AgentRecord{Name: fmt.Sprintf("agent-%d", i%50)}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSpike_OpenAndMigrate(b *testing.B) {
	for i := 0; i < b.N; i++ {
		store, err := NewSQLiteStore(filepath.Join(b.TempDir(), "open.db"), 0, spikeLogger())
		if err != nil {
			b.Fatal(err)
		}
		store.Close()
	}
}
