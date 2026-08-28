package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/provider"
	"github.com/OberWatch/oberwatch/internal/storage"
)

const (
	basePath = "/_oberwatch/api/v1"

	// providerStatusTTL is how long a provider status snapshot is served
	// before it is refreshed. Every served row also carries observed_at, so a
	// reader never has to trust this window on its own. A refresh is started in
	// the background when a request finds the snapshot older than this; the
	// request itself is answered from the snapshot it already has.
	providerStatusTTL = 60 * time.Second
)

var validAgentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Server serves management API endpoints.
//
//nolint:govet // Keep fields grouped by dependency role and lifecycle.
type Server struct {
	budget *budget.BudgetManager
	store  storage.Store
	mux    *http.ServeMux

	startedAt       time.Time
	version         string
	storageBackend  string
	pricing         []config.PricingEntry
	broker          *broker
	providerChecker providerStatusChecker
	ollamaBaseURL   string

	// now is the clock used for provider status freshness. Tests replace it.
	now func() time.Time

	providerMu         sync.RWMutex
	providerRows       []provider.StatusRow
	providerObservedAt time.Time
	providerRefreshing bool
}

// providerStatusChecker probes public provider availability. provider.Checker
// satisfies this; tests may substitute a fake to avoid real network calls.
type providerStatusChecker interface {
	CheckOpenAI(ctx context.Context) provider.StatusRow
	CheckAnthropic(ctx context.Context) provider.StatusRow
	CheckOllama(ctx context.Context, baseURL string) (provider.StatusRow, bool)
}

//nolint:govet // Keep broker fields grouped for lock+client ownership clarity.
type broker struct {
	mu      sync.RWMutex
	clients map[chan sseEvent]struct{}
}

//nolint:govet // Keep event fields grouped by SSE payload semantics.
type sseEvent struct {
	name string
	data map[string]any
}

// New builds a management API server.
func New(cfg config.Config, budgetManager *budget.BudgetManager, store storage.Store, version string) *Server {
	if strings.TrimSpace(version) == "" {
		version = "0.1.3"
	}

	server := &Server{
		budget:          budgetManager,
		store:           store,
		mux:             http.NewServeMux(),
		startedAt:       time.Now().UTC(),
		version:         version,
		storageBackend:  string(cfg.Trace.Storage),
		pricing:         append([]config.PricingEntry(nil), cfg.Pricing...),
		providerChecker: provider.NewChecker(),
		ollamaBaseURL:   cfg.Upstream.Ollama.BaseURL,
		now:             func() time.Time { return time.Now().UTC() },
		providerRows:    initialProviderRows(cfg.Upstream.Ollama.BaseURL),
		broker: &broker{
			clients: make(map[chan sseEvent]struct{}),
		},
	}
	server.registerRoutes()

	// Warm the snapshot so the first dashboard load sees real data. Startup
	// never waits on this.
	go server.refreshProviderStatus(context.Background())

	return server
}

// initialProviderRows builds the placeholder rows served before the first
// probe completes. The set of rows is fixed by configuration, not by probe
// outcomes: the two public feeds are always present, and the local Ollama row
// is present exactly when a loopback base URL is configured. The same set is
// produced by every later refresh, so a card never appears or disappears while
// a session is open; only its status changes.
func initialProviderRows(ollamaBaseURL string) []provider.StatusRow {
	rows := []provider.StatusRow{
		pendingProviderRow("openai", "OpenAI"),
		pendingProviderRow("anthropic", "Anthropic"),
	}
	if provider.OllamaConfigured(ollamaBaseURL) {
		rows = append(rows, pendingOllamaRow())
	}
	return rows
}

// pendingProviderRow is the placeholder shown before the first background
// probe completes. It must never claim a status we have not yet verified, and
// it carries no observed_at because nothing has been observed yet.
func pendingProviderRow(providerName, label string) provider.StatusRow {
	return provider.StatusRow{
		Provider: providerName,
		Label:    label,
		Status:   provider.StatusUnavailable,
		Public:   true,
		Detail:   "Waiting for the first public status feed check.",
	}
}

// pendingOllamaRow is the placeholder for a configured local Ollama server
// before its first reachability probe. It reports unreachable rather than a
// status it has not verified, and carries no observed_at.
func pendingOllamaRow() provider.StatusRow {
	return provider.StatusRow{
		Provider: "ollama",
		Label:    provider.OllamaLabel,
		Status:   provider.StatusUnreachable,
		Public:   false,
		Detail:   "Waiting for the first local reachability check.",
	}
}

// refreshProviderStatus probes every provider once and replaces the cached
// rows. All probes run in parallel under a single provider.ProbeTimeout
// budget, so one refresh is bounded by that timeout however many providers
// there are. It returns false when another refresh already holds the slot:
// probes never overlap.
func (s *Server) refreshProviderStatus(ctx context.Context) bool {
	if !s.beginProviderRefresh() {
		return false
	}
	defer s.endProviderRefresh()

	ctx, cancel := context.WithTimeout(ctx, provider.ProbeTimeout)
	defer cancel()

	var (
		waitGroup    sync.WaitGroup
		openAIRow    provider.StatusRow
		anthropicRow provider.StatusRow
		ollamaRow    provider.StatusRow
		ollamaOK     bool
	)

	waitGroup.Add(3)
	go func() {
		defer waitGroup.Done()
		openAIRow = s.providerChecker.CheckOpenAI(ctx)
	}()
	go func() {
		defer waitGroup.Done()
		anthropicRow = s.providerChecker.CheckAnthropic(ctx)
	}()
	go func() {
		defer waitGroup.Done()
		ollamaRow, ollamaOK = s.providerChecker.CheckOllama(ctx, s.ollamaBaseURL)
	}()
	waitGroup.Wait()

	observedAt := s.nowUTC()

	// Fixed row order: the public feeds first, then the local server. Probe
	// completion order must not reorder the table the user reads. The public
	// rows are always present, whatever their status. The Ollama row is present
	// whenever a loopback base URL is configured (ollamaOK), including when the
	// server did not answer; it is never present without one.
	rows := make([]provider.StatusRow, 0, 3)
	rows = append(rows, stampObservedAt(openAIRow, observedAt), stampObservedAt(anthropicRow, observedAt))
	if ollamaOK {
		rows = append(rows, stampObservedAt(ollamaRow, observedAt))
	}

	s.providerMu.Lock()
	s.providerRows = rows
	s.providerObservedAt = observedAt
	s.providerMu.Unlock()

	return true
}

// stampObservedAt records when a row was observed, so a reader can tell a
// current status from one that has gone stale.
func stampObservedAt(row provider.StatusRow, observedAt time.Time) provider.StatusRow {
	stamped := observedAt
	row.ObservedAt = &stamped
	return row
}

// beginProviderRefresh claims the single refresh slot.
func (s *Server) beginProviderRefresh() bool {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()

	if s.providerRefreshing {
		return false
	}
	s.providerRefreshing = true
	return true
}

func (s *Server) endProviderRefresh() {
	s.providerMu.Lock()
	s.providerRefreshing = false
	s.providerMu.Unlock()
}

// providerStatusStale reports whether the cached rows have aged past the TTL.
func (s *Server) providerStatusStale() bool {
	s.providerMu.RLock()
	observedAt := s.providerObservedAt
	s.providerMu.RUnlock()

	if observedAt.IsZero() {
		return true
	}
	return s.nowUTC().Sub(observedAt) >= providerStatusTTL
}

// ensureFreshProviderStatus starts a refresh when the snapshot has aged past
// the TTL. It always returns immediately: the caller answers from the rows it
// already has rather than waiting on the network.
func (s *Server) ensureFreshProviderStatus() {
	if !s.providerStatusStale() {
		return
	}
	go s.refreshProviderStatus(context.Background())
}

// providerStatusSnapshot returns a defensive copy of the current provider
// status rows for serving over the API.
func (s *Server) providerStatusSnapshot() []provider.StatusRow {
	s.providerMu.RLock()
	defer s.providerMu.RUnlock()

	rows := make([]provider.StatusRow, len(s.providerRows))
	copy(rows, s.providerRows)
	return rows
}

// providerStatusObservedAt returns when the cached rows were observed, or the
// zero time before the first probe finished.
func (s *Server) providerStatusObservedAt() time.Time {
	s.providerMu.RLock()
	defer s.providerMu.RUnlock()
	return s.providerObservedAt
}

func (s *Server) nowUTC() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

// ServeHTTP routes requests through auth and endpoint handlers.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isPublicEndpoint(r.URL.Path) {
		if !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "auth_required", "Missing or invalid session", "", 0, 0)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func isPublicEndpoint(path string) bool {
	switch path {
	case basePath + "/health",
		basePath + "/auth/status",
		basePath + "/setup",
		basePath + "/login":
		return true
	default:
		return false
	}
}

// PublishCostUpdate broadcasts a cost_update SSE event.
func (s *Server) PublishCostUpdate(record storage.CostRecord) {
	spentUSD := 0.0
	if s.budget != nil {
		spentUSD = s.budget.GetBudget(record.Agent).SpentUSD
	}

	s.publish("cost_update", map[string]any{
		"agent":            record.Agent,
		"spent_usd":        spentUSD,
		"request_cost_usd": record.CostUSD,
	})
}

// PublishAlert broadcasts budget_alert or agent_killed SSE events.
func (s *Server) PublishAlert(entry alert.Alert) {
	if s.store != nil {
		_ = s.store.SaveAlert(context.Background(), entry)
	}

	if entry.Type == alert.TypeAgentKilled {
		reason := "agent_killed"
		if reasonValue, ok := entry.Data["reason"]; ok {
			if reasonText, ok := reasonValue.(string); ok && strings.TrimSpace(reasonText) != "" {
				reason = reasonText
			}
		}
		s.publish("agent_killed", map[string]any{
			"agent":  entry.Agent,
			"reason": reason,
		})
		return
	}

	s.publish("budget_alert", map[string]any{
		"agent":         entry.Agent,
		"threshold_pct": entry.ThresholdPct,
		"spent_usd":     entry.SpentUSD,
		"limit_usd":     entry.LimitUSD,
	})
}

// WrapCostSink returns a sink that publishes SSE events and then delegates.
func (s *Server) WrapCostSink(next storage.CostRecordSink) storage.CostRecordSink {
	if next == nil {
		return sinkFunc(func(record storage.CostRecord) {
			s.PublishCostUpdate(record)
		})
	}

	return sinkFunc(func(record storage.CostRecord) {
		s.PublishCostUpdate(record)
		next.Enqueue(record)
	})
}

// WrapDispatcher returns a dispatcher that publishes SSE events and then delegates.
func (s *Server) WrapDispatcher(next budget.Dispatcher) budget.Dispatcher {
	if next == nil {
		return dispatcherFunc(func(ctx context.Context, entry alert.Alert) {
			s.PublishAlert(entry)
		})
	}

	return dispatcherFunc(func(ctx context.Context, entry alert.Alert) {
		s.PublishAlert(entry)
		next.Dispatch(ctx, entry)
	})
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc(basePath+"/health", s.handleHealth)
	s.mux.HandleFunc(basePath+"/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc(basePath+"/setup", s.handleSetup)
	s.mux.HandleFunc(basePath+"/login", s.handleLogin)
	s.mux.HandleFunc(basePath+"/logout", s.handleLogout)
	s.mux.HandleFunc(basePath+"/settings/password", s.handlePasswordChange)
	s.mux.HandleFunc(basePath+"/alerts", s.handleAlerts)
	s.mux.HandleFunc(basePath+"/pricing", s.handlePricing)
	s.mux.HandleFunc(basePath+"/budgets", s.handleBudgets)
	s.mux.HandleFunc(basePath+"/budgets/", s.handleBudgetByAgent)
	s.mux.HandleFunc(basePath+"/kill-all", s.handleKillAll)
	s.mux.HandleFunc(basePath+"/resume", s.handleResume)
	s.mux.HandleFunc(basePath+"/costs", s.handleCosts)
	s.mux.HandleFunc(basePath+"/costs/export", s.handleCostsExport)
	s.mux.HandleFunc(basePath+"/tasks", s.handleTasks)
	s.mux.HandleFunc(basePath+"/tasks/", s.handleTaskByID)
	s.mux.HandleFunc(basePath+"/agents", s.handleAgents)
	s.mux.HandleFunc(basePath+"/agents/", s.handleAgentByName)
	s.mux.HandleFunc(basePath+"/stream", s.handleStream)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	// Refresh in the background when the snapshot has aged out. This request is
	// answered from the current snapshot either way.
	s.ensureFreshProviderStatus()

	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"version":         s.version,
		"uptime_seconds":  int(time.Since(s.startedAt).Seconds()),
		"storage_backend": s.storageBackend,
		"emergency_stop":  s.budget != nil && s.budget.EmergencyStop(),
		"providers":       s.providerStatusSnapshot(),
	})
}

func (s *Server) handlePricing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	rows := make([]map[string]any, 0, len(s.pricing))
	for _, entry := range s.pricing {
		rows = append(rows, map[string]any{
			"model":              entry.Model,
			"provider":           entry.Provider,
			"input_per_million":  entry.InputPerMillion,
			"output_per_million": entry.OutputPerMillion,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		modelI := fmt.Sprintf("%v", rows[i]["model"])
		modelJ := fmt.Sprintf("%v", rows[j]["model"])
		return modelI < modelJ
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"pricing": rows,
	})
}

func (s *Server) handleBudgets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.budget == nil || s.store == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager or storage is not configured", "", 0, 0)
		return
	}

	records, err := s.store.ListAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		view := s.budget.GetBudget(record.Name)
		items = append(items, encodeBudgetRecord(record, view))
	}

	gv := s.budget.GetGlobalBudget()
	writeJSON(w, http.StatusOK, map[string]any{
		"budgets": items,
		"global": map[string]any{
			"period":           string(gv.Period),
			"period_starts_at": gv.PeriodStartsAt,
			"period_resets_at": gv.PeriodResetsAt,
			"limit_usd":        gv.LimitUSD,
			"spent_usd":        gv.SpentUSD,
			"remaining_usd":    gv.RemainingUSD,
			"percentage_used":  gv.PercentageUsed,
		},
	})
}

func (s *Server) handleBudgetByAgent(w http.ResponseWriter, r *http.Request) {
	agent, action, ok := parseBudgetPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "config_error", "budget agent not found", "", 0, 0)
		return
	}

	if strings.TrimSpace(action) == "" {
		s.handleBudgetAgentCRUD(w, r, agent)
		return
	}
	s.handleBudgetAgentAction(w, r, agent, action)
}

func (s *Server) handleBudgetAgentCRUD(w http.ResponseWriter, r *http.Request, agent string) {
	if s.budget == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager is not configured", agent, 0, 0)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if s.store == nil {
			writeError(w, http.StatusInternalServerError, "config_error", "storage is not configured", agent, 0, 0)
			return
		}
		record, found, err := s.store.GetAgent(r.Context(), agent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "config_error", err.Error(), agent, 0, 0)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "config_error", "budget agent not found", agent, 0, 0)
			return
		}
		writeJSON(w, http.StatusOK, encodeBudgetRecord(record, s.budget.GetBudget(agent)))
	case http.MethodPut:
		//nolint:govet // Keep payload fields grouped to mirror API contract order.
		var payload struct {
			LimitUSD              float64             `json:"limit_usd"`
			Period                config.BudgetPeriod `json:"period"`
			ActionOnExceed        config.BudgetAction `json:"action_on_exceed"`
			DowngradeChain        []string            `json:"downgrade_chain"`
			DowngradeThresholdPct float64             `json:"downgrade_threshold_pct"`
			AlertThresholdsPct    []float64           `json:"alert_thresholds_pct"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, "config_error", fmt.Sprintf("invalid budget payload: %v", err), agent, 0, 0)
			return
		}

		if err := s.budget.UpdateBudget(agent, budget.BudgetUpdate{
			LimitUSD:              payload.LimitUSD,
			Period:                payload.Period,
			ActionOnExceed:        payload.ActionOnExceed,
			DowngradeChain:        payload.DowngradeChain,
			DowngradeThresholdPct: payload.DowngradeThresholdPct,
			AlertThresholdsPct:    payload.AlertThresholdsPct,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "config_error", err.Error(), agent, 0, 0)
			return
		}

		record, _, err := s.store.GetAgent(r.Context(), agent)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "config_error", err.Error(), agent, 0, 0)
			return
		}
		writeJSON(w, http.StatusOK, encodeBudgetRecord(record, s.budget.GetBudget(agent)))
	default:
		writeMethodNotAllowed(w)
	}
}

func (s *Server) handleBudgetAgentAction(w http.ResponseWriter, r *http.Request, agent string, action string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.budget == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager is not configured", agent, 0, 0)
		return
	}

	switch action {
	case "reset":
		s.budget.ResetBudget(agent)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "kill":
		s.budget.KillAgent(agent)
		s.publish("agent_killed", map[string]any{"agent": agent, "reason": "manual_kill"})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "enable":
		s.budget.EnableAgent(agent)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	default:
		writeError(w, http.StatusNotFound, "config_error", "unknown budget action", agent, 0, 0)
	}
}

func (s *Server) handleKillAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.budget == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager is not configured", "", 0, 0)
		return
	}

	s.budget.SetEmergencyStop(true)
	if s.store != nil {
		if err := s.store.SetSetting(r.Context(), "emergency_stop", "true"); err != nil {
			writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
			return
		}
	}
	s.publish("emergency_stop", map[string]any{"enabled": true})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.budget == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager is not configured", "", 0, 0)
		return
	}

	s.budget.SetEmergencyStop(false)
	if s.store != nil {
		if err := s.store.SetSetting(r.Context(), "emergency_stop", "false"); err != nil {
			writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
			return
		}
	}
	s.publish("emergency_stop", map[string]any{"enabled": false})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "storage is not configured", "", 0, 0)
		return
	}

	query, err := parseCostQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_error", err.Error(), "", 0, 0)
		return
	}
	if query.GroupBy == "" {
		query.GroupBy = "agent"
	}

	rows, err := s.store.QueryCosts(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	totalUSD := 0.0
	totalRequests := 0
	totalInputTokens := 0
	totalOutputTokens := 0
	breakdown := make([]map[string]any, 0, len(rows))

	for _, row := range rows {
		totalUSD += row.CostUSD
		totalRequests += row.Requests
		totalInputTokens += row.InputTokens
		totalOutputTokens += row.OutputTokens
		breakdown = append(breakdown, map[string]any{
			"agent":         row.Agent,
			"model":         row.Model,
			"bucket":        row.Bucket,
			"requests":      row.Requests,
			"input_tokens":  row.InputTokens,
			"output_tokens": row.OutputTokens,
			"cost_usd":      row.CostUSD,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total_usd":           totalUSD,
		"total_requests":      totalRequests,
		"total_input_tokens":  totalInputTokens,
		"total_output_tokens": totalOutputTokens,
		"breakdown":           breakdown,
	})
}

func (s *Server) handleCostsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "storage is not configured", "", 0, 0)
		return
	}

	query, err := parseCostQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_error", err.Error(), "", 0, 0)
		return
	}
	if query.GroupBy == "" {
		query.GroupBy = "agent"
	}

	rows, err := s.store.QueryCosts(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	csvData, err := storage.FormatCostAggregatesCSV(rows)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="costs.csv"`)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(csvData)); err != nil {
		return
	}
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.budget == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager is not configured", "", 0, 0)
		return
	}

	views := s.budget.ListTasks()
	tasks := make([]map[string]any, 0, len(views))
	for _, view := range views {
		tasks = append(tasks, taskViewPayload(view))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	if s.budget == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager is not configured", "", 0, 0)
		return
	}

	taskID, action, ok := parseTaskPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "task path not found", "", 0, 0)
		return
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		view, found := s.budget.GetTask(taskID)
		if !found {
			writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("task %q not found", taskID), "", 0, 0)
			return
		}
		writeJSON(w, http.StatusOK, taskViewPayload(view))
	case action == "reset" && r.Method == http.MethodPost:
		if err := s.budget.ResetTask(taskID); err != nil {
			if errors.Is(err, storage.ErrTaskNotFound) {
				writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("task %q not found", taskID), "", 0, 0)
				return
			}
			writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
			return
		}
		view, _ := s.budget.GetTask(taskID)
		s.publish("task_reset", map[string]any{"task_id": taskID})
		writeJSON(w, http.StatusOK, taskViewPayload(view))
	case action == "":
		writeMethodNotAllowed(w)
	case action == "reset":
		writeMethodNotAllowed(w)
	default:
		writeError(w, http.StatusNotFound, "not_found", "task action not found", "", 0, 0)
	}
}

func parseTaskPath(path string) (string, string, bool) {
	trimmed := strings.TrimPrefix(path, basePath+"/tasks/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	taskID := strings.TrimSpace(parts[0])
	if taskID == "" {
		return "", "", false
	}
	switch len(parts) {
	case 1:
		return taskID, "", true
	case 2:
		return taskID, parts[1], true
	default:
		return "", "", false
	}
}

func taskViewPayload(view budget.TaskView) map[string]any {
	return map[string]any{
		"task_id":         view.TaskID,
		"status":          view.Status,
		"last_agent":      view.LastAgent,
		"limit_usd":       view.LimitUSD,
		"spent_usd":       view.SpentUSD,
		"reserved_usd":    view.ReservedUSD,
		"remaining_usd":   view.RemainingUSD,
		"percentage_used": view.PercentageUsed,
		"request_count":   view.RequestCount,
		"in_flight":       view.InFlight,
		"first_seen_at":   view.FirstSeenAt.UTC().Format(time.RFC3339),
		"last_seen_at":    view.LastSeenAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.budget == nil || s.store == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager or storage is not configured", "", 0, 0)
		return
	}

	records, err := s.store.ListAgents(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	rawCosts, err := s.store.QueryCosts(r.Context(), storage.CostQuery{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	requestsByAgent := make(map[string]int, len(rawCosts))
	costByAgent := make(map[string]float64, len(rawCosts))
	lastSeenByAgent := make(map[string]time.Time, len(rawCosts))
	modelsByAgent := make(map[string][]string)
	seenModelsByAgent := make(map[string]map[string]struct{})

	//nolint:govet // Keep summary fields grouped to mirror /agents response.
	type agentSummary struct {
		Name          string
		Status        string
		LastModel     string
		ModelsUsed    []string
		TotalRequests int
		TotalCostUSD  float64
		LastSeenAt    time.Time
		BudgetStatus  string
	}

	for _, row := range rawCosts {
		requestsByAgent[row.Agent] += row.Requests
		costByAgent[row.Agent] += row.CostUSD
		if parsed, err := time.Parse(time.RFC3339Nano, row.Bucket); err == nil {
			if parsed.After(lastSeenByAgent[row.Agent]) {
				lastSeenByAgent[row.Agent] = parsed
			}
		}
	}
	for index := len(rawCosts) - 1; index >= 0; index-- {
		row := rawCosts[index]
		model := strings.TrimSpace(row.Model)
		if model == "" {
			continue
		}
		seen := seenModelsByAgent[row.Agent]
		if seen == nil {
			seen = make(map[string]struct{})
			seenModelsByAgent[row.Agent] = seen
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		modelsByAgent[row.Agent] = append(modelsByAgent[row.Agent], model)
	}

	summaries := make([]agentSummary, 0, len(records))
	for _, record := range records {
		view := s.budget.GetBudget(record.Name)
		budgetStatus := "under_limit"
		if view.Status == "killed" {
			budgetStatus = "killed"
		} else if view.PercentageUsed >= 100 {
			budgetStatus = "over_limit"
		}
		lastSeen := record.LastSeenAt
		if seen := lastSeenByAgent[record.Name]; seen.After(lastSeen) {
			lastSeen = seen
		}
		modelsUsed := modelsByAgent[record.Name]
		if modelsUsed == nil {
			modelsUsed = make([]string, 0)
		}
		lastModel := ""
		if len(modelsUsed) > 0 {
			lastModel = modelsUsed[0]
		}
		summaries = append(summaries, agentSummary{
			Name:          record.Name,
			Status:        view.Status,
			LastModel:     lastModel,
			ModelsUsed:    modelsUsed,
			TotalRequests: requestsByAgent[record.Name],
			TotalCostUSD:  costByAgent[record.Name],
			LastSeenAt:    lastSeen,
			BudgetStatus:  budgetStatus,
		})
	}

	agents := make([]map[string]any, 0, len(summaries))
	for _, entry := range summaries {
		lastSeen := ""
		if !entry.LastSeenAt.IsZero() {
			lastSeen = entry.LastSeenAt.UTC().Format(time.RFC3339)
		}
		agents = append(agents, map[string]any{
			"name":           entry.Name,
			"status":         entry.Status,
			"last_model":     entry.LastModel,
			"models_used":    entry.ModelsUsed,
			"total_requests": entry.TotalRequests,
			"total_cost_usd": entry.TotalCostUSD,
			"last_seen_at":   lastSeen,
			"budget_status":  entry.BudgetStatus,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleAgentByName(w http.ResponseWriter, r *http.Request) {
	agent, action, ok := parseAgentPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "config_error", "agent not found", "", 0, 0)
		return
	}
	if action != "rename" {
		writeError(w, http.StatusNotFound, "config_error", "unknown agent action", agent, 0, 0)
		return
	}
	if r.Method != http.MethodPut {
		writeMethodNotAllowed(w)
		return
	}
	if s.budget == nil || s.store == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "budget manager or storage is not configured", agent, 0, 0)
		return
	}

	var payload struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "config_error", fmt.Sprintf("invalid rename payload: %v", err), agent, 0, 0)
		return
	}

	newName := strings.TrimSpace(payload.NewName)
	if !validAgentName(newName) {
		writeError(w, http.StatusBadRequest, "config_error", "new_name must contain only letters, numbers, hyphens, or underscores", agent, 0, 0)
		return
	}

	if err := s.budget.RenameAgent(r.Context(), agent, newName); err != nil {
		switch err {
		case storage.ErrAgentExists:
			writeError(w, http.StatusConflict, "config_error", "agent with that name already exists", agent, 0, 0)
		case storage.ErrAgentNotFound:
			writeError(w, http.StatusNotFound, "config_error", "agent not found", agent, 0, 0)
		default:
			writeError(w, http.StatusInternalServerError, "config_error", err.Error(), agent, 0, 0)
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"old_name": agent,
		"new_name": newName,
	})
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.store == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "storage is not configured", "", 0, 0)
		return
	}

	query, err := parseAlertQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_error", err.Error(), "", 0, 0)
		return
	}

	items, err := s.store.QueryAlerts(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	alerts := make([]map[string]any, 0, len(items))
	for _, item := range items {
		alerts = append(alerts, map[string]any{
			"id":            item.ID,
			"type":          item.Type,
			"agent":         item.Agent,
			"message":       item.Message,
			"severity":      item.Severity,
			"timestamp":     item.Timestamp.UTC().Format(time.RFC3339),
			"threshold_pct": item.ThresholdPct,
			"spent_usd":     item.SpentUSD,
			"limit_usd":     item.LimitUSD,
			"action":        item.Action,
			"data":          item.Data,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "config_error", "streaming unsupported", "", 0, 0)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	channel := s.subscribe()
	defer s.unsubscribe(channel)

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-channel:
			payload, err := json.Marshal(event.data)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\n", event.name); err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) publish(name string, data map[string]any) {
	s.broker.mu.RLock()
	defer s.broker.mu.RUnlock()

	for client := range s.broker.clients {
		select {
		case client <- sseEvent{name: name, data: data}:
		default:
		}
	}
}

func (s *Server) subscribe() chan sseEvent {
	channel := make(chan sseEvent, 16)
	s.broker.mu.Lock()
	s.broker.clients[channel] = struct{}{}
	s.broker.mu.Unlock()
	return channel
}

func (s *Server) unsubscribe(channel chan sseEvent) {
	s.broker.mu.Lock()
	delete(s.broker.clients, channel)
	close(channel)
	s.broker.mu.Unlock()
}

func parseBudgetPath(path string) (string, string, bool) {
	relative := strings.TrimPrefix(path, basePath+"/budgets/")
	segments := strings.Split(strings.Trim(relative, "/"), "/")
	if len(segments) == 0 || strings.TrimSpace(segments[0]) == "" {
		return "", "", false
	}

	agent := strings.TrimSpace(segments[0])
	action := ""
	if len(segments) > 1 {
		action = strings.TrimSpace(segments[1])
	}
	return agent, action, true
}

func parseAgentPath(path string) (string, string, bool) {
	relative := strings.TrimPrefix(path, basePath+"/agents/")
	segments := strings.Split(strings.Trim(relative, "/"), "/")
	if len(segments) == 0 || strings.TrimSpace(segments[0]) == "" {
		return "", "", false
	}

	agent := strings.TrimSpace(segments[0])
	action := ""
	if len(segments) > 1 {
		action = strings.TrimSpace(segments[1])
	}
	return agent, action, true
}

func validAgentName(name string) bool {
	return validAgentNamePattern.MatchString(strings.TrimSpace(name))
}

func encodeBudgetRecord(record storage.AgentRecord, view budget.BudgetView) map[string]any {
	return map[string]any{
		"agent":                   view.Agent,
		"period":                  view.Period,
		"limit_usd":               view.LimitUSD,
		"spent_usd":               view.SpentUSD,
		"remaining_usd":           view.RemainingUSD,
		"percentage_used":         view.PercentageUsed,
		"status":                  view.Status,
		"action_on_exceed":        view.ActionOnExceed,
		"downgrade_chain":         record.DowngradeChain,
		"downgrade_threshold_pct": record.DowngradeThresholdPct,
		"alert_thresholds_pct":    record.AlertThresholdsPct,
		"period_resets_at":        view.PeriodResetsAt.UTC().Format(time.RFC3339),
	}
}

func parseCostQuery(r *http.Request) (storage.CostQuery, error) {
	query := storage.CostQuery{
		Agent:   strings.TrimSpace(r.URL.Query().Get("agent")),
		Model:   strings.TrimSpace(r.URL.Query().Get("model")),
		Task:    strings.TrimSpace(r.URL.Query().Get("task")),
		GroupBy: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("group_by"))),
	}
	if query.GroupBy != "" {
		switch query.GroupBy {
		case "agent", "model", "hour", "day", "agent_hour", "none":
		default:
			return storage.CostQuery{}, fmt.Errorf("unsupported group_by query param %q", query.GroupBy)
		}
	}

	if rawFrom := strings.TrimSpace(r.URL.Query().Get("from")); rawFrom != "" {
		parsedFrom, err := time.Parse(time.RFC3339, rawFrom)
		if err != nil {
			return storage.CostQuery{}, fmt.Errorf("invalid from query param: %w", err)
		}
		query.From = parsedFrom
	}
	if rawTo := strings.TrimSpace(r.URL.Query().Get("to")); rawTo != "" {
		parsedTo, err := time.Parse(time.RFC3339, rawTo)
		if err != nil {
			return storage.CostQuery{}, fmt.Errorf("invalid to query param: %w", err)
		}
		query.To = parsedTo
	}
	if !query.From.IsZero() && !query.To.IsZero() && query.From.After(query.To) {
		return storage.CostQuery{}, fmt.Errorf("from query param must not be after to query param")
	}

	return query, nil
}

func parseAlertQuery(r *http.Request) (storage.AlertQuery, error) {
	query := storage.AlertQuery{
		Agent: strings.TrimSpace(r.URL.Query().Get("agent")),
		Limit: 0,
	}

	if rawType := strings.TrimSpace(r.URL.Query().Get("type")); rawType != "" {
		query.Type = alert.Type(rawType)
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil {
			return storage.AlertQuery{}, fmt.Errorf("invalid limit query param: %w", err)
		}
		query.Limit = parsedLimit
	}
	if rawFrom := strings.TrimSpace(r.URL.Query().Get("from")); rawFrom != "" {
		parsedFrom, err := time.Parse(time.RFC3339, rawFrom)
		if err != nil {
			return storage.AlertQuery{}, fmt.Errorf("invalid from query param: %w", err)
		}
		query.From = parsedFrom
	}
	if rawTo := strings.TrimSpace(r.URL.Query().Get("to")); rawTo != "" {
		parsedTo, err := time.Parse(time.RFC3339, rawTo)
		if err != nil {
			return storage.AlertQuery{}, fmt.Errorf("invalid to query param: %w", err)
		}
		query.To = parsedTo
	}

	return query, nil
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", fmt.Sprintf("encode response: %v", err), "", 0, 0)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if _, err := w.Write(encoded); err != nil {
		return
	}
}

func writeMethodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "config_error", "method not allowed", "", 0, 0)
}

func writeError(w http.ResponseWriter, statusCode int, code string, message string, agent string, limitUSD float64, spentUSD float64) {
	errorBody := map[string]any{
		"code":    code,
		"message": message,
	}
	if strings.TrimSpace(agent) != "" {
		errorBody["agent"] = agent
		errorBody["budget_limit_usd"] = limitUSD
		errorBody["budget_spent_usd"] = spentUSD
	}

	encoded, err := json.Marshal(map[string]any{"error": errorBody})
	if err != nil {
		http.Error(w, message, statusCode)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if _, err := w.Write(encoded); err != nil {
		return
	}
}

type sinkFunc func(storage.CostRecord)

// Enqueue implements storage.CostRecordSink.
func (f sinkFunc) Enqueue(record storage.CostRecord) {
	f(record)
}

type dispatcherFunc func(context.Context, alert.Alert)

// Dispatch implements budget.Dispatcher.
func (f dispatcherFunc) Dispatch(ctx context.Context, entry alert.Alert) {
	f(ctx, entry)
}
