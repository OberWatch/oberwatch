package storage

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/config"
	// Register SQLite driver with database/sql.
	sqlite3 "github.com/mattn/go-sqlite3"
)

const currentSchemaVersion = 3

// SQLiteStore persists Oberwatch data in SQLite.
//
//nolint:govet // keep fields grouped by lifecycle/ownership.
type SQLiteStore struct {
	db        *sql.DB
	logger    *slog.Logger
	retention time.Duration

	cleanupStop chan struct{}
	cleanupWG   sync.WaitGroup
}

// NewSQLiteStore creates a SQLite-backed store and runs migrations.
func NewSQLiteStore(dsn string, retention time.Duration, logger *slog.Logger) (*SQLiteStore, error) {
	if strings.TrimSpace(dsn) == "" {
		dsn = "oberwatch.db"
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	store := &SQLiteStore{
		db:          db,
		logger:      logger,
		retention:   retention,
		cleanupStop: make(chan struct{}),
	}

	if err := store.applyPragmas(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	if retention > 0 {
		store.startRetentionCleanupLoop()
	}

	return store, nil
}

// Close stops background tasks and closes DB connections.
func (s *SQLiteStore) Close() error {
	close(s.cleanupStop)
	s.cleanupWG.Wait()
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite database: %w", err)
	}
	return nil
}

func (s *SQLiteStore) applyPragmas(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
	}
	for _, statement := range pragmas {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply sqlite pragma %q: %w", statement, err)
		}
	}
	return nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER NOT NULL
		);
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	var rows int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&rows); err != nil {
		return fmt.Errorf("count schema_migrations rows: %w", err)
	}
	if rows == 0 {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES (0)"); err != nil {
			return fmt.Errorf("initialize schema_migrations version row: %w", err)
		}
	}

	var current int
	if err := s.db.QueryRowContext(ctx, "SELECT version FROM schema_migrations LIMIT 1").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	migrations := map[int][]string{
		1: {
			`CREATE TABLE IF NOT EXISTS cost_records (
				id TEXT PRIMARY KEY,
				agent TEXT NOT NULL,
				model TEXT NOT NULL,
				provider TEXT NOT NULL,
				input_tokens INTEGER NOT NULL,
				output_tokens INTEGER NOT NULL,
				cost_usd REAL NOT NULL,
				trace_id TEXT,
				task_id TEXT,
				downgraded INTEGER DEFAULT 0,
				original_model TEXT,
				created_at TEXT NOT NULL
			);`,
			"CREATE INDEX IF NOT EXISTS idx_cost_agent ON cost_records(agent);",
			"CREATE INDEX IF NOT EXISTS idx_cost_timestamp ON cost_records(created_at);",
			"CREATE INDEX IF NOT EXISTS idx_cost_trace ON cost_records(trace_id);",
			`CREATE TABLE IF NOT EXISTS alerts (
				id TEXT PRIMARY KEY,
				type TEXT NOT NULL,
				agent TEXT,
				message TEXT NOT NULL,
				severity TEXT NOT NULL,
				data_json TEXT,
				created_at TEXT NOT NULL
			);`,
			"CREATE INDEX IF NOT EXISTS idx_alert_agent ON alerts(agent);",
			"CREATE INDEX IF NOT EXISTS idx_alert_type ON alerts(type);",
			`CREATE TABLE IF NOT EXISTS budget_snapshots (
				agent TEXT PRIMARY KEY,
				period TEXT NOT NULL,
				period_started_at TEXT NOT NULL,
				period_resets_at TEXT NOT NULL,
				spent_usd REAL NOT NULL,
				last_alerted_pct REAL NOT NULL,
				killed INTEGER NOT NULL,
				updated_at TEXT NOT NULL
			);`,
		},
		2: {
			`CREATE TABLE IF NOT EXISTS settings (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL
			);`,
		},
		3: {
			`CREATE TABLE IF NOT EXISTS agents (
				name TEXT PRIMARY KEY,
				status TEXT NOT NULL DEFAULT 'active',
				budget_limit_usd REAL NOT NULL DEFAULT 0,
				budget_period TEXT NOT NULL DEFAULT 'daily',
				budget_spent_usd REAL NOT NULL DEFAULT 0,
				action_on_exceed TEXT NOT NULL DEFAULT 'alert',
				downgrade_chain TEXT DEFAULT '',
				downgrade_threshold_pct REAL NOT NULL DEFAULT 80,
				alert_thresholds_pct TEXT NOT NULL DEFAULT '50,80,100',
				period_started_at TEXT,
				period_resets_at TEXT,
				first_seen_at TEXT NOT NULL,
				last_seen_at TEXT NOT NULL
			);`,
			"CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);",
			"CREATE INDEX IF NOT EXISTS idx_agents_last_seen ON agents(last_seen_at);",
		},
	}

	for version := current + 1; version <= currentSchemaVersion; version++ {
		statements := migrations[version]
		for _, statement := range statements {
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("run schema migration v%d: %w", version, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE schema_migrations SET version = ?", version); err != nil {
			return fmt.Errorf("update schema version to %d: %w", version, err)
		}
	}

	return nil
}

// SaveCostRecord inserts a proxied request cost record.
func (s *SQLiteStore) SaveCostRecord(ctx context.Context, record CostRecord) error {
	if strings.TrimSpace(record.ID) == "" {
		record.ID = generateID("cost")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cost_records (
			id, agent, model, provider, input_tokens, output_tokens, cost_usd,
			trace_id, task_id, downgraded, original_model, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.ID,
		record.Agent,
		record.Model,
		record.Provider,
		record.InputTokens,
		record.OutputTokens,
		record.CostUSD,
		record.TraceID,
		record.TaskID,
		boolToInt(record.Downgraded),
		record.OriginalModel,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert cost record: %w", err)
	}
	return nil
}

// QueryCosts returns grouped or raw cost rows with optional filters.
func (s *SQLiteStore) QueryCosts(ctx context.Context, query CostQuery) ([]CostAggregate, error) {
	groupBy := strings.ToLower(strings.TrimSpace(query.GroupBy))

	where := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if strings.TrimSpace(query.Agent) != "" {
		where = append(where, "agent = ?")
		args = append(args, strings.TrimSpace(query.Agent))
	}
	if strings.TrimSpace(query.Model) != "" {
		where = append(where, "model = ?")
		args = append(args, strings.TrimSpace(query.Model))
	}
	if !query.From.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, query.From.UTC().Format(time.RFC3339Nano))
	}
	if !query.To.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, query.To.UTC().Format(time.RFC3339Nano))
	}

	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var statement string
	switch groupBy {
	case "", "none":
		statement = `
			SELECT agent, model, created_at AS bucket,
				1 AS requests, input_tokens, output_tokens, cost_usd
			FROM cost_records` + whereSQL + `
			ORDER BY created_at ASC
		`
	case "agent":
		statement = `
			SELECT agent, '' AS model, '' AS bucket,
				COUNT(*) AS requests, SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
			FROM cost_records` + whereSQL + `
			GROUP BY agent
			ORDER BY agent ASC
		`
	case "model":
		statement = `
			SELECT '' AS agent, model, '' AS bucket,
				COUNT(*) AS requests, SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
			FROM cost_records` + whereSQL + `
			GROUP BY model
			ORDER BY model ASC
		`
	case "hour":
		statement = `
			SELECT '' AS agent, '' AS model, strftime('%Y-%m-%dT%H:00:00Z', created_at) AS bucket,
				COUNT(*) AS requests, SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
			FROM cost_records` + whereSQL + `
			GROUP BY bucket
			ORDER BY bucket ASC
		`
	case "day":
		statement = `
			SELECT '' AS agent, '' AS model, substr(created_at, 1, 10) AS bucket,
				COUNT(*) AS requests, SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
			FROM cost_records` + whereSQL + `
			GROUP BY bucket
			ORDER BY bucket ASC
		`
	default:
		return nil, fmt.Errorf("unsupported cost query group_by %q", query.GroupBy)
	}

	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("query costs: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	results := make([]CostAggregate, 0)
	for rows.Next() {
		var row CostAggregate
		if err := rows.Scan(
			&row.Agent,
			&row.Model,
			&row.Bucket,
			&row.Requests,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CostUSD,
		); err != nil {
			return nil, fmt.Errorf("scan cost query row: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cost query rows: %w", err)
	}

	return results, nil
}

// QueryCostsCSV exports QueryCosts rows as CSV.
func (s *SQLiteStore) QueryCostsCSV(ctx context.Context, query CostQuery) (string, error) {
	rows, err := s.QueryCosts(ctx, query)
	if err != nil {
		return "", err
	}
	return FormatCostAggregatesCSV(rows)
}

// FormatCostAggregatesCSV formats cost rows for export.
func FormatCostAggregatesCSV(rows []CostAggregate) (string, error) {
	var builder strings.Builder
	writer := csv.NewWriter(&builder)
	if err := writer.Write([]string{"agent", "model", "bucket", "requests", "input_tokens", "output_tokens", "cost_usd"}); err != nil {
		return "", fmt.Errorf("write csv header: %w", err)
	}
	for _, row := range rows {
		record := []string{
			row.Agent,
			row.Model,
			row.Bucket,
			fmt.Sprintf("%d", row.Requests),
			fmt.Sprintf("%d", row.InputTokens),
			fmt.Sprintf("%d", row.OutputTokens),
			fmt.Sprintf("%.8f", row.CostUSD),
		}
		if err := writer.Write(record); err != nil {
			return "", fmt.Errorf("write csv row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("flush csv writer: %w", err)
	}
	return builder.String(), nil
}

// SaveAlert persists an alert record.
func (s *SQLiteStore) SaveAlert(ctx context.Context, entry alert.Alert) error {
	if strings.TrimSpace(entry.ID) == "" {
		entry.ID = generateID("alert")
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	dataJSON := ""
	payload := make(map[string]any)
	if entry.ThresholdPct != 0 {
		payload["threshold_pct"] = entry.ThresholdPct
	}
	if entry.SpentUSD != 0 {
		payload["spent_usd"] = entry.SpentUSD
	}
	if entry.LimitUSD != 0 {
		payload["limit_usd"] = entry.LimitUSD
	}
	if entry.Action != "" {
		payload["action"] = entry.Action
	}
	if !entry.PeriodStartedAt.IsZero() {
		payload["period_started_at"] = entry.PeriodStartedAt.UTC().Format(time.RFC3339Nano)
	}
	if len(entry.Data) > 0 {
		payload["data"] = entry.Data
	}
	if len(payload) > 0 {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal alert data: %w", err)
		}
		dataJSON = string(encoded)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alerts (id, type, agent, message, severity, data_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		entry.ID,
		string(entry.Type),
		entry.Agent,
		entry.Message,
		entry.Severity,
		dataJSON,
		entry.Timestamp.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert alert record: %w", err)
	}
	return nil
}

// QueryAlerts loads alerts for dashboard and API consumption.
func (s *SQLiteStore) QueryAlerts(ctx context.Context, query AlertQuery) ([]alert.Alert, error) {
	where := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if strings.TrimSpace(query.Agent) != "" {
		where = append(where, "agent = ?")
		args = append(args, strings.TrimSpace(query.Agent))
	}
	if query.Type != "" {
		where = append(where, "type = ?")
		args = append(args, string(query.Type))
	}
	if !query.From.IsZero() {
		where = append(where, "created_at >= ?")
		args = append(args, query.From.UTC().Format(time.RFC3339Nano))
	}
	if !query.To.IsZero() {
		where = append(where, "created_at <= ?")
		args = append(args, query.To.UTC().Format(time.RFC3339Nano))
	}

	statement := `SELECT id, type, agent, message, severity, data_json, created_at FROM alerts`
	if len(where) > 0 {
		statement += " WHERE " + strings.Join(where, " AND ")
	}
	statement += " ORDER BY created_at DESC"
	if query.Limit > 0 {
		statement += " LIMIT ?"
		args = append(args, query.Limit)
	}

	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("query alerts: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	alerts := make([]alert.Alert, 0)
	for rows.Next() {
		var item alert.Alert
		var alertType string
		var dataJSON string
		var createdRaw string
		if err := rows.Scan(&item.ID, &alertType, &item.Agent, &item.Message, &item.Severity, &dataJSON, &createdRaw); err != nil {
			return nil, fmt.Errorf("scan alert row: %w", err)
		}
		item.Type = alert.Type(alertType)
		parsedTime, err := time.Parse(time.RFC3339Nano, createdRaw)
		if err != nil {
			return nil, fmt.Errorf("parse alert created_at: %w", err)
		}
		item.Timestamp = parsedTime.UTC()
		if strings.TrimSpace(dataJSON) != "" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
				return nil, fmt.Errorf("decode alert data_json: %w", err)
			}
			if value, ok := payload["threshold_pct"].(float64); ok {
				item.ThresholdPct = value
			}
			if value, ok := payload["spent_usd"].(float64); ok {
				item.SpentUSD = value
			}
			if value, ok := payload["limit_usd"].(float64); ok {
				item.LimitUSD = value
			}
			if value, ok := payload["action"].(string); ok {
				item.Action = value
			}
			if value, ok := payload["period_started_at"].(string); ok && strings.TrimSpace(value) != "" {
				parsedPeriodStart, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					return nil, fmt.Errorf("parse alert period_started_at: %w", err)
				}
				item.PeriodStartedAt = parsedPeriodStart.UTC()
			}
			if nested, ok := payload["data"].(map[string]any); ok {
				item.Data = nested
			} else {
				item.Data = payload
			}
		}
		alerts = append(alerts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alert rows: %w", err)
	}
	return alerts, nil
}

// UpsertAgent inserts or updates one persisted agent record.
func (s *SQLiteStore) UpsertAgent(ctx context.Context, record AgentRecord) error {
	if strings.TrimSpace(record.Name) == "" {
		return fmt.Errorf("agent name must not be empty")
	}

	now := time.Now().UTC()
	if record.Status == "" {
		record.Status = "active"
	}
	if record.BudgetPeriod == "" {
		record.BudgetPeriod = config.BudgetPeriodDaily
	}
	if record.ActionOnExceed == "" {
		record.ActionOnExceed = config.BudgetActionAlert
	}
	if record.DowngradeThresholdPct == 0 {
		record.DowngradeThresholdPct = 80
	}
	if len(record.AlertThresholdsPct) == 0 {
		record.AlertThresholdsPct = []float64{50, 80, 100}
	}
	if record.FirstSeenAt.IsZero() {
		record.FirstSeenAt = now
	}
	if record.LastSeenAt.IsZero() {
		record.LastSeenAt = now
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agents (
			name, status, budget_limit_usd, budget_period, budget_spent_usd,
			action_on_exceed, downgrade_chain, downgrade_threshold_pct,
			alert_thresholds_pct, period_started_at, period_resets_at,
			first_seen_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			status = excluded.status,
			budget_limit_usd = excluded.budget_limit_usd,
			budget_period = excluded.budget_period,
			budget_spent_usd = excluded.budget_spent_usd,
			action_on_exceed = excluded.action_on_exceed,
			downgrade_chain = excluded.downgrade_chain,
			downgrade_threshold_pct = excluded.downgrade_threshold_pct,
			alert_thresholds_pct = excluded.alert_thresholds_pct,
			period_started_at = excluded.period_started_at,
			period_resets_at = excluded.period_resets_at,
			first_seen_at = COALESCE(agents.first_seen_at, excluded.first_seen_at),
			last_seen_at = excluded.last_seen_at
	`,
		strings.TrimSpace(record.Name),
		record.Status,
		record.BudgetLimitUSD,
		string(record.BudgetPeriod),
		record.BudgetSpentUSD,
		string(record.ActionOnExceed),
		joinStrings(record.DowngradeChain),
		record.DowngradeThresholdPct,
		joinFloat64s(record.AlertThresholdsPct),
		formatOptionalTime(record.PeriodStartedAt),
		formatOptionalTime(record.PeriodResetsAt),
		record.FirstSeenAt.UTC().Format(time.RFC3339Nano),
		record.LastSeenAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert agent %q: %w", record.Name, err)
	}
	return nil
}

// GetAgent returns one persisted agent by name.
func (s *SQLiteStore) GetAgent(ctx context.Context, name string) (AgentRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, status, budget_limit_usd, budget_period, budget_spent_usd,
			action_on_exceed, downgrade_chain, downgrade_threshold_pct,
			alert_thresholds_pct, period_started_at, period_resets_at,
			first_seen_at, last_seen_at
		FROM agents
		WHERE name = ?
	`, strings.TrimSpace(name))

	record, found, err := scanAgentRecord(row.Scan)
	if err != nil {
		return AgentRecord{}, false, fmt.Errorf("query agent %q: %w", name, err)
	}
	return record, found, nil
}

// ListAgents returns all persisted agents ordered by name.
func (s *SQLiteStore) ListAgents(ctx context.Context) ([]AgentRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, status, budget_limit_usd, budget_period, budget_spent_usd,
			action_on_exceed, downgrade_chain, downgrade_threshold_pct,
			alert_thresholds_pct, period_started_at, period_resets_at,
			first_seen_at, last_seen_at
		FROM agents
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	records := make([]AgentRecord, 0)
	for rows.Next() {
		record, _, scanErr := scanAgentRecord(rows.Scan)
		if scanErr != nil {
			return nil, fmt.Errorf("scan agent row: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent rows: %w", err)
	}
	return records, nil
}

// RenameAgent renames one agent and migrates historical cost records.
func (s *SQLiteStore) RenameAgent(ctx context.Context, oldName string, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rename agent transaction: %w", err)
	}

	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	result, err := tx.ExecContext(ctx, "UPDATE agents SET name = ? WHERE name = ?", newName, oldName)
	if err != nil {
		if isSQLiteConstraint(err) {
			return ErrAgentExists
		}
		return fmt.Errorf("rename agent %q to %q: %w", oldName, newName, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read rename rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return ErrAgentNotFound
	}

	if _, err = tx.ExecContext(ctx, "UPDATE cost_records SET agent = ? WHERE agent = ?", newName, oldName); err != nil {
		return fmt.Errorf("rename cost records from %q to %q: %w", oldName, newName, err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit rename agent transaction: %w", err)
	}
	return nil
}

// SaveBudgetSnapshot persists one agent budget state snapshot.
func (s *SQLiteStore) SaveBudgetSnapshot(ctx context.Context, snapshot BudgetSnapshot) error {
	if strings.TrimSpace(snapshot.Agent) == "" {
		return fmt.Errorf("budget snapshot agent must not be empty")
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	if snapshot.PeriodStartedAt.IsZero() {
		snapshot.PeriodStartedAt = snapshot.UpdatedAt
	}
	if snapshot.PeriodResetsAt.IsZero() {
		snapshot.PeriodResetsAt = snapshot.UpdatedAt
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO budget_snapshots (
			agent, period, period_started_at, period_resets_at,
			spent_usd, last_alerted_pct, killed, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent) DO UPDATE SET
			period = excluded.period,
			period_started_at = excluded.period_started_at,
			period_resets_at = excluded.period_resets_at,
			spent_usd = excluded.spent_usd,
			last_alerted_pct = excluded.last_alerted_pct,
			killed = excluded.killed,
			updated_at = excluded.updated_at
	`,
		snapshot.Agent,
		snapshot.Period,
		snapshot.PeriodStartedAt.UTC().Format(time.RFC3339Nano),
		snapshot.PeriodResetsAt.UTC().Format(time.RFC3339Nano),
		snapshot.SpentUSD,
		snapshot.LastAlertedPct,
		boolToInt(snapshot.Killed),
		snapshot.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert budget snapshot: %w", err)
	}

	return nil
}

// LoadBudgetSnapshots loads all persisted budget states.
func (s *SQLiteStore) LoadBudgetSnapshots(ctx context.Context) ([]BudgetSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT agent, period, period_started_at, period_resets_at,
			spent_usd, last_alerted_pct, killed, updated_at
		FROM budget_snapshots
		ORDER BY agent ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query budget snapshots: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	snapshots := make([]BudgetSnapshot, 0)
	for rows.Next() {
		var snapshot BudgetSnapshot
		var startedAtRaw string
		var resetsAtRaw string
		var updatedAtRaw string
		var killedInt int
		if err := rows.Scan(
			&snapshot.Agent,
			&snapshot.Period,
			&startedAtRaw,
			&resetsAtRaw,
			&snapshot.SpentUSD,
			&snapshot.LastAlertedPct,
			&killedInt,
			&updatedAtRaw,
		); err != nil {
			return nil, fmt.Errorf("scan budget snapshot row: %w", err)
		}

		startedAt, err := time.Parse(time.RFC3339Nano, startedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse period_started_at: %w", err)
		}
		resetsAt, err := time.Parse(time.RFC3339Nano, resetsAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse period_resets_at: %w", err)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}

		snapshot.PeriodStartedAt = startedAt.UTC()
		snapshot.PeriodResetsAt = resetsAt.UTC()
		snapshot.UpdatedAt = updatedAt.UTC()
		snapshot.Killed = intToBool(killedInt)
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate budget snapshot rows: %w", err)
	}

	return snapshots, nil
}

// GetSetting returns one setting value by key.
func (s *SQLiteStore) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", strings.TrimSpace(key)).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("query setting %q: %w", key, err)
	}
	return value, true, nil
}

// SetSetting upserts one setting value by key.
func (s *SQLiteStore) SetSetting(ctx context.Context, key string, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, strings.TrimSpace(key), value)
	if err != nil {
		return fmt.Errorf("upsert setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes one setting value by key.
func (s *SQLiteStore) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", strings.TrimSpace(key))
	if err != nil {
		return fmt.Errorf("delete setting %q: %w", key, err)
	}
	return nil
}

// CleanupRetention deletes records older than configured retention.
func (s *SQLiteStore) CleanupRetention(ctx context.Context) error {
	if s.retention <= 0 {
		return nil
	}

	cutoff := time.Now().UTC().Add(-s.retention).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, "DELETE FROM cost_records WHERE created_at < ?", cutoff); err != nil {
		return fmt.Errorf("delete expired cost records: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM alerts WHERE created_at < ?", cutoff); err != nil {
		return fmt.Errorf("delete expired alerts: %w", err)
	}
	return nil
}

func (s *SQLiteStore) startRetentionCleanupLoop() {
	s.cleanupWG.Add(1)
	go func() {
		defer s.cleanupWG.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.CleanupRetention(context.Background()); err != nil && s.logger != nil {
					s.logger.Warn("retention cleanup failed", "error", err)
				}
			case <-s.cleanupStop:
				return
			}
		}
	}()
}

func generateID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UTC().UnixNano())
}

type scannerFunc func(dest ...any) error

func scanAgentRecord(scan scannerFunc) (AgentRecord, bool, error) {
	var record AgentRecord
	var downgradeChain string
	var alertThresholds string
	var periodStartedAt string
	var periodResetsAt string
	var firstSeenAt string
	var lastSeenAt string
	err := scan(
		&record.Name,
		&record.Status,
		&record.BudgetLimitUSD,
		&record.BudgetPeriod,
		&record.BudgetSpentUSD,
		&record.ActionOnExceed,
		&downgradeChain,
		&record.DowngradeThresholdPct,
		&alertThresholds,
		&periodStartedAt,
		&periodResetsAt,
		&firstSeenAt,
		&lastSeenAt,
	)
	if err == sql.ErrNoRows {
		return AgentRecord{}, false, nil
	}
	if err != nil {
		return AgentRecord{}, false, err
	}

	record.DowngradeChain = splitCSVStrings(downgradeChain)
	record.AlertThresholdsPct = splitCSVFloat64s(alertThresholds)
	record.PeriodStartedAt = parseOptionalTime(periodStartedAt)
	record.PeriodResetsAt = parseOptionalTime(periodResetsAt)
	record.FirstSeenAt = parseOptionalTime(firstSeenAt)
	record.LastSeenAt = parseOptionalTime(lastSeenAt)
	return record, true, nil
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseOptionalTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func joinStrings(values []string) string {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		clean = append(clean, trimmed)
	}
	return strings.Join(clean, ",")
}

func joinFloat64s(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%g", value))
	}
	return strings.Join(parts, ",")
}

func splitCSVStrings(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func splitCSVFloat64s(raw string) []float64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]float64, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		var value float64
		if _, err := fmt.Sscanf(trimmed, "%f", &value); err != nil {
			continue
		}
		result = append(result, value)
	}
	return result
}

func isSQLiteConstraint(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func intToBool(value int) bool {
	return value != 0
}
