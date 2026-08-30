package config

import (
	"fmt"
	"strings"
	"time"
)

// ValidationError aggregates config validation failures.
type ValidationError struct {
	Problems []string
}

// Error formats all validation failures in a single error message.
func (e *ValidationError) Error() string {
	return "config validation failed:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Validate validates the full config.
func Validate(cfg Config) error {
	problems := make([]string, 0)
	problems = append(problems, validateServer(cfg.Server)...)
	problems = append(problems, validateUpstream(cfg.Upstream)...)
	problems = append(problems, validateGate(cfg.Gate)...)
	problems = append(problems, ValidateAlertsConfig(cfg.Alerts)...)
	problems = append(problems, validateTrace(cfg.Trace)...)
	problems = append(problems, validateTest(cfg.Test)...)
	problems = append(problems, validatePricing(cfg.Pricing)...)

	if len(problems) == 0 {
		return nil
	}

	return &ValidationError{Problems: problems}
}

func validateServer(server ServerConfig) []string {
	problems := make([]string, 0)

	if server.Port < 1 || server.Port > 65535 {
		problems = append(problems, fmt.Sprintf("server.port must be between 1 and 65535, got %d", server.Port))
	}
	if strings.TrimSpace(server.Host) == "" {
		problems = append(problems, "server.host must not be empty")
	}
	if !isOneOf(string(server.LogLevel), string(LogLevelDebug), string(LogLevelInfo), string(LogLevelWarn), string(LogLevelError)) {
		problems = append(problems, fmt.Sprintf("server.log_level must be one of debug, info, warn, error, got %q", server.LogLevel))
	}
	if !isOneOf(string(server.LogFormat), string(LogFormatJSON), string(LogFormatText)) {
		problems = append(problems, fmt.Sprintf("server.log_format must be one of json, text, got %q", server.LogFormat))
	}
	if (server.TLSCert == "") != (server.TLSKey == "") {
		problems = append(problems, "server.tls_cert and server.tls_key must both be set or both be empty")
	}

	return problems
}

func validateUpstream(upstream UpstreamConfig) []string {
	problems := make([]string, 0)

	if !isOneOf(string(upstream.DefaultProvider), string(ProviderOpenAI), string(ProviderAnthropic), string(ProviderOllama), string(ProviderCustom)) {
		problems = append(problems, fmt.Sprintf("upstream.default_provider must be one of openai, anthropic, ollama, custom, got %q", upstream.DefaultProvider))
	}
	if err := validatePositiveDuration("upstream.timeout", upstream.Timeout); err != nil {
		problems = append(problems, err.Error())
	}
	if strings.TrimSpace(upstream.OpenAI.BaseURL) == "" {
		problems = append(problems, "upstream.openai.base_url must not be empty")
	}
	if strings.TrimSpace(upstream.Anthropic.BaseURL) == "" {
		problems = append(problems, "upstream.anthropic.base_url must not be empty")
	}
	if strings.TrimSpace(upstream.Ollama.BaseURL) == "" {
		problems = append(problems, "upstream.ollama.base_url must not be empty")
	}
	if upstream.DefaultProvider == ProviderCustom && strings.TrimSpace(upstream.Custom.BaseURL) == "" {
		problems = append(problems, "upstream.custom.base_url must not be empty when upstream.default_provider is custom")
	}

	return problems
}

func validateGate(gate GateConfig) []string {
	problems := make([]string, 0)

	if gate.GlobalBudget.LimitUSD < 0 {
		problems = append(problems, fmt.Sprintf("gate.global_budget.limit_usd must be non-negative, got %v", gate.GlobalBudget.LimitUSD))
	}
	if gate.DefaultBudget.LimitUSD < 0 {
		problems = append(problems, fmt.Sprintf("gate.default_budget.limit_usd must be non-negative, got %v", gate.DefaultBudget.LimitUSD))
	}
	if gate.TaskBudgetUSD < 0 {
		problems = append(problems, fmt.Sprintf("gate.task_budget_usd must be non-negative, got %v", gate.TaskBudgetUSD))
	}
	problems = append(problems, validateBudgetPeriod("gate.global_budget.period", gate.GlobalBudget.Period)...)
	problems = append(problems, validateBudgetPeriod("gate.default_budget.period", gate.DefaultBudget.Period)...)
	problems = append(problems, validateBudgetAction("gate.default_budget.action_on_exceed", gate.DefaultBudget.ActionOnExceed)...)

	if gate.DowngradeThresholdPct < 0 || gate.DowngradeThresholdPct > 100 {
		problems = append(problems, fmt.Sprintf("gate.downgrade_threshold_pct must be between 0 and 100, got %v", gate.DowngradeThresholdPct))
	}
	if len(gate.AlertThresholdsPct) == 0 {
		problems = append(problems, "gate.alert_thresholds_pct must not be empty")
	}
	last := -1.0
	for i, threshold := range gate.AlertThresholdsPct {
		if threshold < 0 || threshold > 100 {
			problems = append(problems, fmt.Sprintf("gate.alert_thresholds_pct[%d] must be between 0 and 100, got %v", i, threshold))
		}
		if threshold <= last {
			problems = append(problems, "gate.alert_thresholds_pct must be strictly increasing")
			break
		}
		last = threshold
	}
	if gate.Runaway.MaxRequests < 1 {
		problems = append(problems, fmt.Sprintf("gate.runaway.max_requests must be at least 1, got %d", gate.Runaway.MaxRequests))
	}
	if gate.Runaway.WindowSeconds < 1 {
		problems = append(problems, fmt.Sprintf("gate.runaway.window_seconds must be at least 1, got %d", gate.Runaway.WindowSeconds))
	}
	if !isOneOf(string(gate.Identification.Method), string(IdentificationMethodHeader), string(IdentificationMethodAPIKey), string(IdentificationMethodSourceIP)) {
		problems = append(problems, fmt.Sprintf("gate.identification.method must be one of header, api_key, source_ip, got %q", gate.Identification.Method))
	}
	if gate.Identification.Method == IdentificationMethodAPIKey && len(gate.APIKeyMap) == 0 {
		problems = append(problems, "gate.api_key_map must not be empty when gate.identification.method is api_key")
	}
	for i, agent := range gate.Agents {
		if strings.TrimSpace(agent.Name) == "" {
			problems = append(problems, fmt.Sprintf("gate.agents[%d].name must not be empty", i))
		}
		if agent.LimitUSD < 0 {
			problems = append(problems, fmt.Sprintf("gate.agents[%d].limit_usd must be non-negative, got %v", i, agent.LimitUSD))
		}
		if agent.TaskBudgetUSD < 0 {
			problems = append(problems, fmt.Sprintf("gate.agents[%d].task_budget_usd must be non-negative, got %v", i, agent.TaskBudgetUSD))
		}
		problems = append(problems, validateBudgetPeriod(fmt.Sprintf("gate.agents[%d].period", i), agent.Period)...)
		problems = append(problems, validateBudgetAction(fmt.Sprintf("gate.agents[%d].action_on_exceed", i), agent.ActionOnExceed)...)

	}
	for i, entry := range gate.APIKeyMap {
		if strings.TrimSpace(entry.APIKeyPrefix) == "" {
			problems = append(problems, fmt.Sprintf("gate.api_key_map[%d].api_key_prefix must not be empty", i))
		}
		if strings.TrimSpace(entry.Agent) == "" {
			problems = append(problems, fmt.Sprintf("gate.api_key_map[%d].agent must not be empty", i))
		}
	}

	return problems
}

func validateBudgetPeriod(field string, period BudgetPeriod) []string {
	if isOneOf(string(period), string(BudgetPeriodHourly), string(BudgetPeriodDaily), string(BudgetPeriodWeekly), string(BudgetPeriodMonthly)) {
		return nil
	}

	return []string{fmt.Sprintf("%s must be one of hourly, daily, weekly, monthly, got %q", field, period)}
}

func validateBudgetAction(field string, action BudgetAction) []string {
	if isOneOf(string(action), string(BudgetActionReject), string(BudgetActionDowngrade), string(BudgetActionAlert), string(BudgetActionKill)) {
		return nil
	}

	return []string{fmt.Sprintf("%s must be one of reject, downgrade, alert, kill, got %q", field, action)}
}

// ValidateAlertsConfig checks alerts delivery settings (webhook, Slack, and
// email). It is shared by full-config validation and by the runtime settings
// API, which validates a candidate before persisting or live-applying it.
func ValidateAlertsConfig(alerts AlertsConfig) []string {
	problems := make([]string, 0)

	if strings.TrimSpace(alerts.WebhookURL) != "" {
		if err := ValidateWebhookURL(alerts.WebhookURL); err != nil {
			problems = append(problems, fmt.Sprintf("alerts.webhook_url %s", err.Error()))
		}
	}
	if strings.TrimSpace(alerts.SlackWebhookURL) != "" {
		if err := ValidateSlackWebhookURL(alerts.SlackWebhookURL); err != nil {
			problems = append(problems, fmt.Sprintf("alerts.slack_webhook_url %s", err.Error()))
		}
	}

	if alerts.Email.Enabled {
		problems = append(problems, ValidateEmailConfig(alerts.Email)...)
	}

	return problems
}

func validateTrace(trace TraceConfig) []string {
	problems := make([]string, 0)

	if trace.MemoryBufferSize < 1 {
		problems = append(problems, fmt.Sprintf("trace.memory_buffer_size must be at least 1, got %d", trace.MemoryBufferSize))
	}
	if err := validatePositiveDuration("trace.retention", trace.Retention); err != nil {
		problems = append(problems, err.Error())
	}
	if !isOneOf(string(trace.Storage), string(TraceStorageMemory), string(TraceStorageSQLite)) {
		problems = append(problems, fmt.Sprintf("trace.storage must be one of memory, sqlite, got %q", trace.Storage))
	}
	if trace.Storage == TraceStorageSQLite && strings.TrimSpace(trace.SQLitePath) == "" {
		problems = append(problems, "trace.sqlite_path must not be empty when trace.storage is sqlite")
	}
	if err := validatePositiveDuration("trace.trace_timeout", trace.TraceTimeout); err != nil {
		problems = append(problems, err.Error())
	}

	return problems
}

func validateTest(testConfig TestConfig) []string {
	problems := make([]string, 0)

	if strings.TrimSpace(testConfig.ScenariosDir) == "" {
		problems = append(problems, "test.scenarios_dir must not be empty")
	}
	if testConfig.Concurrency < 1 {
		problems = append(problems, fmt.Sprintf("test.concurrency must be at least 1, got %d", testConfig.Concurrency))
	}
	if err := validatePositiveDuration("test.timeout", testConfig.Timeout); err != nil {
		problems = append(problems, err.Error())
	}
	if strings.TrimSpace(testConfig.Judge.Model) == "" {
		problems = append(problems, "test.judge.model must not be empty")
	}
	if !isOneOf(string(testConfig.Judge.Provider), string(ProviderOpenAI), string(ProviderAnthropic), string(ProviderGoogle), string(ProviderOllama), string(ProviderCustom)) {
		problems = append(problems, fmt.Sprintf("test.judge.provider must be one of openai, anthropic, google, ollama, custom, got %q", testConfig.Judge.Provider))
	}

	return problems
}

func validatePricing(pricing []PricingEntry) []string {
	problems := make([]string, 0)

	if len(pricing) == 0 {
		problems = append(problems, "pricing must not be empty")
	}
	for i, entry := range pricing {
		if strings.TrimSpace(entry.Model) == "" {
			problems = append(problems, fmt.Sprintf("pricing[%d].model must not be empty", i))
		}
		if !isOneOf(string(entry.Provider), string(ProviderOpenAI), string(ProviderAnthropic), string(ProviderGoogle), string(ProviderOllama), string(ProviderCustom)) {
			problems = append(problems, fmt.Sprintf("pricing[%d].provider must be one of openai, anthropic, google, ollama, custom, got %q", i, entry.Provider))
		}
		if entry.InputPerMillion < 0 {
			problems = append(problems, fmt.Sprintf("pricing[%d].input_per_million must be non-negative, got %v", i, entry.InputPerMillion))
		}
		if entry.OutputPerMillion < 0 {
			problems = append(problems, fmt.Sprintf("pricing[%d].output_per_million must be non-negative, got %v", i, entry.OutputPerMillion))
		}
	}

	return problems
}

func validatePositiveDuration(field string, raw string) error {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s must be a valid duration, got %q", field, raw)
	}
	if duration <= 0 {
		return fmt.Errorf("%s must be greater than 0, got %q", field, raw)
	}
	return nil
}

func isOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
