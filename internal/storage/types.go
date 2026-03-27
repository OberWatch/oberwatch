package storage

import (
	"context"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
)

// Store defines persistence operations used by proxy, budget, and dashboard APIs.
type Store interface {
	SaveCostRecord(context.Context, CostRecord) error
	QueryCosts(context.Context, CostQuery) ([]CostAggregate, error)
	SaveAlert(context.Context, alert.Alert) error
	QueryAlerts(context.Context, AlertQuery) ([]alert.Alert, error)
	SaveBudgetSnapshot(context.Context, BudgetSnapshot) error
	LoadBudgetSnapshots(context.Context) ([]BudgetSnapshot, error)
	GetSetting(context.Context, string) (string, bool, error)
	SetSetting(context.Context, string, string) error
	DeleteSetting(context.Context, string) error
}

// CostRecord captures one persisted proxied request billing event.
//
//nolint:govet // keep persisted record fields grouped by domain semantics.
type CostRecord struct {
	ID            string
	Agent         string
	Model         string
	Provider      string
	TraceID       string
	TaskID        string
	OriginalModel string
	InputTokens   int
	OutputTokens  int
	CostUSD       float64
	CreatedAt     time.Time
	Downgraded    bool
}

// CostQuery defines filters and grouping for cost queries.
//
//nolint:govet // keep query fields grouped by filter semantics.
type CostQuery struct {
	Agent   string
	Model   string
	GroupBy string // "", "agent", "model", "hour", "day"
	From    time.Time
	To      time.Time
}

// CostAggregate is a grouped or raw cost query row.
type CostAggregate struct {
	Agent        string
	Model        string
	Bucket       string
	Requests     int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// AlertQuery defines filters for alert retrieval.
//
//nolint:govet // keep alert query fields grouped by API usage.
type AlertQuery struct {
	Agent string
	Type  alert.Type
	From  time.Time
	To    time.Time
	Limit int
}

// BudgetSnapshot captures budget state for restart restore.
//
//nolint:govet // keep snapshot fields grouped by budget model semantics.
type BudgetSnapshot struct {
	Agent           string
	Period          string
	PeriodStartedAt time.Time
	PeriodResetsAt  time.Time
	SpentUSD        float64
	LastAlertedPct  float64
	Killed          bool
	UpdatedAt       time.Time
}

// CostRecordSink is a non-blocking enqueue target for async cost persistence.
type CostRecordSink interface {
	Enqueue(CostRecord)
}
