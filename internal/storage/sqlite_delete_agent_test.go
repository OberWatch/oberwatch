package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/config"
)

// seedTwoAgents writes two agents with cost records, alerts, budget snapshots,
// task budgets and one setting, so a delete of the first can be checked for
// leaving everything owned by the second (and global state) alone.
func seedTwoAgents(t *testing.T, store *SQLiteStore, now time.Time) {
	t.Helper()
	ctx := context.Background()

	for _, name := range []string{"doomed", "survivor"} {
		if err := store.UpsertAgent(ctx, AgentRecord{
			Name:            name,
			Status:          "active",
			BudgetLimitUSD:  10,
			BudgetPeriod:    config.BudgetPeriodDaily,
			ActionOnExceed:  config.BudgetActionAlert,
			PeriodStartedAt: now,
			PeriodResetsAt:  now.Add(24 * time.Hour),
			FirstSeenAt:     now,
			LastSeenAt:      now,
		}); err != nil {
			t.Fatalf("UpsertAgent(%q) error = %v", name, err)
		}
		for index := 0; index < 2; index++ {
			if err := store.SaveCostRecord(ctx, CostRecord{
				Agent: name, Model: "gpt-4o", Provider: "openai",
				InputTokens: 10, OutputTokens: 5, CostUSD: 0.25,
				CreatedAt: now.Add(time.Duration(index) * time.Minute),
			}); err != nil {
				t.Fatalf("SaveCostRecord(%q) error = %v", name, err)
			}
		}
		if err := store.SaveAlert(ctx, alert.NewBudgetThresholdAlert(name, 80, 8, 10, "alert", now)); err != nil {
			t.Fatalf("SaveAlert(%q) error = %v", name, err)
		}
		if err := store.SaveBudgetSnapshot(ctx, BudgetSnapshot{
			Agent: name, Period: "daily", PeriodStartedAt: now, PeriodResetsAt: now.Add(24 * time.Hour),
			SpentUSD: 0.5, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("SaveBudgetSnapshot(%q) error = %v", name, err)
		}
		if err := store.UpsertTask(ctx, TaskRecord{
			TaskID: "task-" + name, LastAgent: name, SpentUSD: 0.5, LimitUSD: 5, RequestCount: 2,
			FirstSeenAt: now, LastSeenAt: now,
		}); err != nil {
			t.Fatalf("UpsertTask(%q) error = %v", name, err)
		}
	}
	if err := store.SetSetting(ctx, "setup_complete", "true"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}
}

func TestSQLiteStore_DeleteAgent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 28, 9, 0, 0, 0, time.UTC)

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name    string
		target  string
		wantErr error
		want    AgentDeletion
		assert  func(*testing.T, *SQLiteStore)
	}{
		{
			name:   "removes only the rows owned by the deleted agent",
			target: "doomed",
			want:   AgentDeletion{Agent: "doomed", CostRecords: 2, Alerts: 1, BudgetSnapshots: 1, TasksDetached: 1},
			assert: func(t *testing.T, store *SQLiteStore) {
				t.Helper()
				ctx := context.Background()

				if _, found, err := store.GetAgent(ctx, "doomed"); err != nil || found {
					t.Fatalf("GetAgent(doomed) = found %v, err %v; want gone", found, err)
				}
				if _, found, err := store.GetAgent(ctx, "survivor"); err != nil || !found {
					t.Fatalf("GetAgent(survivor) = found %v, err %v; want kept", found, err)
				}

				for agent, wantRows := range map[string]int{"doomed": 0, "survivor": 2} {
					rows, err := store.QueryCosts(ctx, CostQuery{Agent: agent, GroupBy: "none"})
					if err != nil {
						t.Fatalf("QueryCosts(%q) error = %v", agent, err)
					}
					if len(rows) != wantRows {
						t.Fatalf("cost rows for %q = %d, want %d", agent, len(rows), wantRows)
					}
					alerts, err := store.QueryAlerts(ctx, AlertQuery{Agent: agent})
					if err != nil {
						t.Fatalf("QueryAlerts(%q) error = %v", agent, err)
					}
					if wantAlerts := min(wantRows, 1); len(alerts) != wantAlerts {
						t.Fatalf("alerts for %q = %d, want %d", agent, len(alerts), wantAlerts)
					}
				}

				snapshots, err := store.LoadBudgetSnapshots(ctx)
				if err != nil {
					t.Fatalf("LoadBudgetSnapshots() error = %v", err)
				}
				if len(snapshots) != 1 || snapshots[0].Agent != "survivor" {
					t.Fatalf("snapshots = %#v, want only survivor", snapshots)
				}

				tasks, err := store.ListTasks(ctx)
				if err != nil {
					t.Fatalf("ListTasks() error = %v", err)
				}
				if len(tasks) != 2 {
					t.Fatalf("len(ListTasks()) = %d, want both task budgets kept", len(tasks))
				}
				for _, task := range tasks {
					switch task.TaskID {
					case "task-doomed":
						if task.LastAgent != "" || task.SpentUSD != 0.5 {
							t.Fatalf("task-doomed = %#v, want last_agent cleared and spend kept", task)
						}
					case "task-survivor":
						if task.LastAgent != "survivor" {
							t.Fatalf("task-survivor = %#v, want last_agent kept", task)
						}
					}
				}

				value, found, err := store.GetSetting(ctx, "setup_complete")
				if err != nil || !found || value != "true" {
					t.Fatalf("GetSetting(setup_complete) = %q, %v, %v; want kept", value, found, err)
				}
			},
		},
		{
			name:    "unknown agent is not found",
			target:  "never-seen",
			wantErr: ErrAgentNotFound,
		},
		{
			name:    "blank agent name is rejected",
			target:  "   ",
			wantErr: errBlankAgent,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "delete.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			seedTwoAgents(t, store, now)

			got, err := store.DeleteAgent(context.Background(), tt.target)
			switch {
			case tt.wantErr == errBlankAgent:
				if err == nil {
					t.Fatal("DeleteAgent(blank) error = nil, want non-nil")
				}
				return
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DeleteAgent() error = %v, want %v", err, tt.wantErr)
				}
				return
			case err != nil:
				t.Fatalf("DeleteAgent() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DeleteAgent() = %#v, want %#v", got, tt.want)
			}
			if tt.assert != nil {
				tt.assert(t, store)
			}
		})
	}
}

// errBlankAgent marks the table case where any error is acceptable.
var errBlankAgent = errors.New("blank agent")

func TestSQLiteStore_DeleteAgentRepeatedAndConcurrent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 28, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		deleters int
	}{
		{name: "second sequential delete is not found", deleters: 1},
		{name: "exactly one of many concurrent deletes wins", deleters: 8},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "delete-race.db"), 0, nil)
			if err != nil {
				t.Fatalf("NewSQLiteStore() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			seedTwoAgents(t, store, now)

			results := make([]error, tt.deleters)
			var wg sync.WaitGroup
			for index := 0; index < tt.deleters; index++ {
				wg.Add(1)
				go func(slot int) {
					defer wg.Done()
					_, results[slot] = store.DeleteAgent(context.Background(), "doomed")
				}(index)
			}
			wg.Wait()

			wins := 0
			for _, err := range results {
				switch {
				case err == nil:
					wins++
				case errors.Is(err, ErrAgentNotFound):
				default:
					t.Fatalf("DeleteAgent() unexpected error = %v", err)
				}
			}
			if wins != 1 {
				t.Fatalf("successful deletes = %d, want exactly 1", wins)
			}

			if _, err := store.DeleteAgent(context.Background(), "doomed"); !errors.Is(err, ErrAgentNotFound) {
				t.Fatalf("DeleteAgent(already deleted) error = %v, want ErrAgentNotFound", err)
			}
			if _, found, err := store.GetAgent(context.Background(), "survivor"); err != nil || !found {
				t.Fatalf("GetAgent(survivor) = found %v, err %v; want kept", found, err)
			}
		})
	}
}
