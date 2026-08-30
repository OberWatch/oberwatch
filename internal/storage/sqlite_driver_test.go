package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	sqlite "modernc.org/sqlite"
)

func newFileStore(t *testing.T, path string) *SQLiteStore {
	t.Helper()

	store, err := NewSQLiteStore(path, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewSQLiteStore(%q) error = %v", path, err)
	}
	return store
}

// downgradeSchema rewinds a freshly migrated database to an older schema
// version by reversing the migrations in sqlite.go, so the upgrade path from an
// existing installation can be tested.
func downgradeSchema(t *testing.T, db *sql.DB, target int) {
	t.Helper()

	steps := map[int][]string{
		5: {
			"ALTER TABLE agents DROP COLUMN task_budget_usd",
		},
		4: {
			"DROP INDEX IF EXISTS idx_cost_task",
			"DROP INDEX IF EXISTS idx_task_budgets_last_seen",
			"DROP TABLE IF EXISTS task_budgets",
		},
		3: {
			"DROP INDEX IF EXISTS idx_agents_status",
			"DROP INDEX IF EXISTS idx_agents_last_seen",
			"DROP TABLE IF EXISTS agents",
		},
		2: {"DROP TABLE IF EXISTS settings"},
	}

	for version := currentSchemaVersion; version > target; version-- {
		for _, statement := range steps[version] {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("downgrade v%d %q: %v", version, statement, err)
			}
		}
	}
	if _, err := db.Exec("UPDATE schema_migrations SET version = ?", target); err != nil {
		t.Fatalf("set schema version %d: %v", target, err)
	}
}

func schemaObjects(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()

	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type IN ('table','index')")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return names
}

// TestSQLiteStore_LegacySchemaMigration covers the v1 and v2 upgrade paths.
// TestSQLiteStore_TaskBudgetsMigration covers v3.
func TestSQLiteStore_LegacySchemaMigration(t *testing.T) {
	t.Parallel()

	for _, from := range []int{1, 2} {
		t.Run(fmt.Sprintf("v%d_to_v%d", from, currentSchemaVersion), func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "legacy.db")

			store := newFileStore(t, path)
			for i := 0; i < 25; i++ {
				record := CostRecord{
					ID:        fmt.Sprintf("legacy-%d", i),
					Agent:     "agent-1",
					Model:     "gpt-4o",
					Provider:  "openai",
					CostUSD:   0.001,
					CreatedAt: time.Now().UTC(),
				}
				if err := store.SaveCostRecord(ctx, record); err != nil {
					t.Fatalf("SaveCostRecord() error = %v", err)
				}
			}
			savedAlert := alert.Alert{
				Type:     alert.TypeBudgetThreshold,
				Agent:    "agent-1",
				Message:  "threshold",
				Severity: "warning",
			}
			if err := store.SaveAlert(ctx, savedAlert); err != nil {
				t.Fatalf("SaveAlert() error = %v", err)
			}

			downgradeSchema(t, store.db, from)
			if err := store.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}

			reopened := newFileStore(t, path)
			t.Cleanup(func() {
				_ = reopened.Close()
			})

			var version int
			if err := reopened.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations LIMIT 1").Scan(&version); err != nil {
				t.Fatalf("read schema version: %v", err)
			}
			if version != currentSchemaVersion {
				t.Fatalf("schema version after reopen = %d, want %d", version, currentSchemaVersion)
			}

			objects := schemaObjects(t, reopened.db)
			wanted := []string{
				"cost_records", "alerts", "budget_snapshots", "settings", "agents",
				"task_budgets", "idx_cost_task", "idx_task_budgets_last_seen",
				"idx_agents_status", "idx_agents_last_seen",
			}
			for _, name := range wanted {
				if !objects[name] {
					t.Errorf("missing %q after migration from v%d", name, from)
				}
			}

			var costs, alerts int
			if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cost_records").Scan(&costs); err != nil {
				t.Fatalf("count cost_records: %v", err)
			}
			if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM alerts").Scan(&alerts); err != nil {
				t.Fatalf("count alerts: %v", err)
			}
			if costs != 25 || alerts != 1 {
				t.Fatalf("rows after migration from v%d: costs = %d, alerts = %d, want 25 and 1", from, costs, alerts)
			}

			// Tables added by the migrations must be usable, not just present.
			if err := reopened.SetSetting(ctx, "migrated", "yes"); err != nil {
				t.Fatalf("SetSetting() on migrated database error = %v", err)
			}
			if err := reopened.UpsertAgent(ctx, AgentRecord{Name: "agent-1"}); err != nil {
				t.Fatalf("UpsertAgent() on migrated database error = %v", err)
			}
			if err := reopened.UpsertTask(ctx, TaskRecord{TaskID: "task-1", LimitUSD: 1}); err != nil {
				t.Fatalf("UpsertTask() on migrated database error = %v", err)
			}
		})
	}
}

// TestSQLiteStore_ConstraintErrorMasksExtendedCode pins the driver-coupled
// branch in isSQLiteConstraint. The driver reports extended result codes, so a
// primary key violation arrives as 1555 and a unique violation as 2067; only
// the masked low byte equals SQLITE_CONSTRAINT. A test that just asserts
// ErrAgentExists still passes when the check fails closed, so assert the codes.
func TestSQLiteStore_ConstraintErrorMasksExtendedCode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newFileStore(t, filepath.Join(t.TempDir(), "constraint.db"))
	t.Cleanup(func() {
		_ = store.Close()
	})

	if _, err := store.db.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES ('dup', '1')"); err != nil {
		t.Fatalf("seed settings row: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE uniq (v TEXT UNIQUE)`); err != nil {
		t.Fatalf("create uniq table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "INSERT INTO uniq(v) VALUES ('dup')"); err != nil {
		t.Fatalf("seed uniq row: %v", err)
	}

	violations := []struct {
		name      string
		statement string
	}{
		{name: "primary key", statement: "INSERT INTO settings(key, value) VALUES ('dup', '2')"},
		{name: "unique index", statement: "INSERT INTO uniq(v) VALUES ('dup')"},
	}
	for _, violation := range violations {
		_, err := store.db.ExecContext(ctx, violation.statement)
		if err == nil {
			t.Fatalf("%s violation: insert succeeded", violation.name)
		}

		var driverErr *sqlite.Error
		if !errors.As(err, &driverErr) {
			t.Fatalf("%s violation: errors.As(*sqlite.Error) = false for %v", violation.name, err)
		}
		code := driverErr.Code()
		t.Logf("%s violation: result code = %d (masked %d): %v", violation.name, code, code&0xff, err)
		if code&0xff != sqliteConstraint {
			t.Errorf("%s violation: masked result code = %d, want %d", violation.name, code&0xff, sqliteConstraint)
		}
		// The driver carries the extended code, so an unmasked comparison
		// against the primary code would not match. Masking is load-bearing.
		if code == sqliteConstraint {
			t.Errorf("%s violation: result code = %d, expected an extended code above the primary one", violation.name, code)
		}
		if !isSQLiteConstraint(err) {
			t.Errorf("%s violation: isSQLiteConstraint() = false for %v", violation.name, err)
		}
	}

	// A non-constraint failure must not be classified as a constraint error.
	_, err := store.db.ExecContext(ctx, "SELECT * FROM table_that_does_not_exist")
	if err == nil {
		t.Fatal("query against a missing table succeeded")
	}
	if isSQLiteConstraint(err) {
		t.Errorf("isSQLiteConstraint() = true for a non-constraint error (%v)", err)
	}
	if isSQLiteConstraint(errors.New("not a driver error")) {
		t.Error("isSQLiteConstraint() = true for a plain error")
	}
}

// TestSQLiteStore_PragmasAndWALShutdown asserts the pragmas NewSQLiteStore
// applies are in force on the connection, and that Close checkpoints the WAL
// and removes its sidecar files.
func TestSQLiteStore_PragmasAndWALShutdown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pragmas.db")
	store := newFileStore(t, path)

	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var synchronous int
	if err := store.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	if synchronous != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", synchronous)
	}

	var busyTimeout int
	if err := store.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}

	record := CostRecord{ID: "wal-1", Agent: "agent-1", Model: "m", Provider: "p", CreatedAt: time.Now().UTC()}
	if err := store.SaveCostRecord(ctx, record); err != nil {
		t.Fatalf("SaveCostRecord() error = %v", err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Errorf("expected a WAL sidecar while the store is open: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s sidecar still present after Close (err = %v)", sidecar, err)
		}
	}

	reopened := newFileStore(t, path)
	t.Cleanup(func() {
		_ = reopened.Close()
	})
	var count int
	if err := reopened.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM cost_records").Scan(&count); err != nil {
		t.Fatalf("count cost_records after reopen: %v", err)
	}
	if count != 1 {
		t.Fatalf("cost_records after reopen = %d, want 1", count)
	}
}
