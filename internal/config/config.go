package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const envPrefix = "OBERWATCH_"

// Config is the top-level Oberwatch configuration.
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Upstream UpstreamConfig `toml:"upstream"`
	Alerts   AlertsConfig   `toml:"alerts"`
	Trace    TraceConfig    `toml:"trace"`
	Test     TestConfig     `toml:"test"`
	Pricing  []PricingEntry `toml:"pricing"`
	Gate     GateConfig     `toml:"gate"`
}

// ServerConfig defines proxy server settings.
type ServerConfig struct {
	Host       string    `toml:"host"`
	AdminToken string    `toml:"admin_token"`
	LogLevel   LogLevel  `toml:"log_level"`
	LogFormat  LogFormat `toml:"log_format"`
	TLSCert    string    `toml:"tls_cert"`
	TLSKey     string    `toml:"tls_key"`
	Port       int       `toml:"port"`
	Dashboard  bool      `toml:"dashboard"`
}

// LogLevel is a server log verbosity.
type LogLevel string

// LogLevel constants.
const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LogFormat is a server log encoding.
type LogFormat string

// LogFormat constants.
const (
	LogFormatJSON LogFormat = "json"
	LogFormatText LogFormat = "text"
)

// UpstreamConfig defines upstream provider settings.
type UpstreamConfig struct {
	DefaultProvider ProviderConfigName `toml:"default_provider"`
	OpenAI          ProviderEndpoint   `toml:"openai"`
	Anthropic       ProviderEndpoint   `toml:"anthropic"`
	Ollama          ProviderEndpoint   `toml:"ollama"`
	Custom          ProviderEndpoint   `toml:"custom"`
	Timeout         string             `toml:"timeout"`
}

// ProviderConfigName identifies a configured provider.
type ProviderConfigName string

// ProviderConfigName constants.
const (
	ProviderOpenAI    ProviderConfigName = "openai"
	ProviderAnthropic ProviderConfigName = "anthropic"
	ProviderGoogle    ProviderConfigName = "google"
	ProviderOllama    ProviderConfigName = "ollama"
	ProviderCustom    ProviderConfigName = "custom"
)

// ProviderEndpoint defines an upstream base URL.
type ProviderEndpoint struct {
	BaseURL string `toml:"base_url"`
}

// GateConfig defines cost governor settings.
type GateConfig struct {
	DefaultDowngradeChain []string             `toml:"default_downgrade_chain"`
	AlertThresholdsPct    []float64            `toml:"alert_thresholds_pct"`
	GlobalBudget          BudgetLimitConfig    `toml:"global_budget"`
	DefaultBudget         BudgetPolicyConfig   `toml:"default_budget"`
	Agents                []AgentBudgetConfig  `toml:"agents"`
	Identification        IdentificationConfig `toml:"identification"`
	APIKeyMap             []APIKeyMapEntry     `toml:"api_key_map"`
	Runaway               RunawayConfig        `toml:"runaway"`
	DowngradeThresholdPct float64              `toml:"downgrade_threshold_pct"`
	Enabled               bool                 `toml:"enabled"`
}

// BudgetLimitConfig defines a budget limit and reset period.
type BudgetLimitConfig struct {
	Period   BudgetPeriod `toml:"period"`
	LimitUSD float64      `toml:"limit_usd"`
}

// BudgetPolicyConfig defines a budget limit with an enforcement action.
type BudgetPolicyConfig struct {
	Period         BudgetPeriod `toml:"period"`
	ActionOnExceed BudgetAction `toml:"action_on_exceed"`
	LimitUSD       float64      `toml:"limit_usd"`
}

// AgentBudgetConfig defines a per-agent override.
type AgentBudgetConfig struct {
	Name           string       `toml:"name"`
	Period         BudgetPeriod `toml:"period"`
	ActionOnExceed BudgetAction `toml:"action_on_exceed"`
	DowngradeChain []string     `toml:"downgrade_chain"`
	LimitUSD       float64      `toml:"limit_usd"`
}

// BudgetPeriod is a budget reset window.
type BudgetPeriod string

// BudgetPeriod constants.
const (
	BudgetPeriodHourly  BudgetPeriod = "hourly"
	BudgetPeriodDaily   BudgetPeriod = "daily"
	BudgetPeriodWeekly  BudgetPeriod = "weekly"
	BudgetPeriodMonthly BudgetPeriod = "monthly"
)

// BudgetAction is the action taken on budget exceed.
type BudgetAction string

// BudgetAction constants.
const (
	BudgetActionReject    BudgetAction = "reject"
	BudgetActionDowngrade BudgetAction = "downgrade"
	BudgetActionAlert     BudgetAction = "alert"
	BudgetActionKill      BudgetAction = "kill"
)

// RunawayConfig defines runaway request detection settings.
type RunawayConfig struct {
	Enabled       bool `toml:"enabled"`
	MaxRequests   int  `toml:"max_requests"`
	WindowSeconds int  `toml:"window_seconds"`
}

// IdentificationConfig defines how agents are identified.
type IdentificationConfig struct {
	Method IdentificationMethod `toml:"method"`
}

// IdentificationMethod identifies agents from incoming traffic.
type IdentificationMethod string

// IdentificationMethod constants.
const (
	IdentificationMethodHeader   IdentificationMethod = "header"
	IdentificationMethodAPIKey   IdentificationMethod = "api_key"
	IdentificationMethodSourceIP IdentificationMethod = "source_ip"
)

// APIKeyMapEntry maps an API key prefix to an agent.
type APIKeyMapEntry struct {
	APIKeyPrefix string `toml:"api_key_prefix"`
	Agent        string `toml:"agent"`
}

// AlertsConfig defines alert delivery settings.
type AlertsConfig struct {
	WebhookURL      string      `toml:"webhook_url"`
	SlackWebhookURL string      `toml:"slack_webhook_url"`
	Email           EmailConfig `toml:"email"`
}

// EmailConfig defines SMTP alert settings.
type EmailConfig struct {
	SMTPHost     string   `toml:"smtp_host"`
	SMTPUser     string   `toml:"smtp_user"`
	SMTPPassword string   `toml:"smtp_password"`
	From         string   `toml:"from"`
	To           []string `toml:"to"`
	SMTPPort     int      `toml:"smtp_port"`
	Enabled      bool     `toml:"enabled"`
}

// TraceConfig defines trace collection settings.
type TraceConfig struct {
	Retention        string       `toml:"retention"`
	Storage          TraceStorage `toml:"storage"`
	SQLitePath       string       `toml:"sqlite_path"`
	TraceTimeout     string       `toml:"trace_timeout"`
	MemoryBufferSize int          `toml:"memory_buffer_size"`
	Enabled          bool         `toml:"enabled"`
	CaptureContent   bool         `toml:"capture_content"`
}

// TraceStorage is the trace persistence backend.
type TraceStorage string

// TraceStorage constants.
const (
	TraceStorageMemory TraceStorage = "memory"
	TraceStorageSQLite TraceStorage = "sqlite"
)

// TestConfig defines the behavioral test harness settings.
type TestConfig struct {
	ScenariosDir string      `toml:"scenarios_dir"`
	Timeout      string      `toml:"timeout"`
	Judge        JudgeConfig `toml:"judge"`
	Concurrency  int         `toml:"concurrency"`
}

// JudgeConfig defines LLM-as-judge settings.
type JudgeConfig struct {
	Model    string             `toml:"model"`
	Provider ProviderConfigName `toml:"provider"`
	APIKey   string             `toml:"api_key"`
}

// PricingEntry defines per-model pricing.
type PricingEntry struct {
	Model            string             `toml:"model"`
	Provider         ProviderConfigName `toml:"provider"`
	InputPerMillion  float64            `toml:"input_per_million"`
	OutputPerMillion float64            `toml:"output_per_million"`
}

// DefaultConfig returns the default Oberwatch configuration.
func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Port:      8080,
			Host:      "0.0.0.0",
			Dashboard: true,
			LogLevel:  LogLevelInfo,
			LogFormat: LogFormatText,
		},
		Upstream: UpstreamConfig{
			DefaultProvider: ProviderOpenAI,
			OpenAI:          ProviderEndpoint{BaseURL: "https://api.openai.com"},
			Anthropic:       ProviderEndpoint{BaseURL: "https://api.anthropic.com"},
			Ollama:          ProviderEndpoint{BaseURL: "http://localhost:11434"},
			Custom:          ProviderEndpoint{},
			Timeout:         "120s",
		},
		Gate: GateConfig{
			Enabled:               true,
			DefaultDowngradeChain: []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"},
			DowngradeThresholdPct: 80,
			AlertThresholdsPct:    []float64{50, 80, 100},
			GlobalBudget: BudgetLimitConfig{
				LimitUSD: 0,
				Period:   BudgetPeriodMonthly,
			},
			DefaultBudget: BudgetPolicyConfig{
				LimitUSD:       0,
				Period:         BudgetPeriodDaily,
				ActionOnExceed: BudgetActionAlert,
			},
			Runaway: RunawayConfig{
				Enabled:       true,
				MaxRequests:   100,
				WindowSeconds: 60,
			},
			Identification: IdentificationConfig{
				Method: IdentificationMethodHeader,
			},
		},
		Alerts: AlertsConfig{
			Email: EmailConfig{
				SMTPPort: 587,
				To:       []string{},
			},
		},
		Trace: TraceConfig{
			Enabled:          true,
			CaptureContent:   false,
			MemoryBufferSize: 1000,
			Retention:        "168h",
			Storage:          TraceStorageSQLite,
			SQLitePath:       "./oberwatch.db",
			TraceTimeout:     "30s",
		},
		Test: TestConfig{
			ScenariosDir: "./scenarios",
			Concurrency:  4,
			Timeout:      "30s",
			Judge: JudgeConfig{
				Model:    "claude-haiku-4-5",
				Provider: ProviderAnthropic,
			},
		},
		Pricing: defaultPricing(),
	}
}

func defaultPricing() []PricingEntry {
	return []PricingEntry{
		{Model: "gpt-4o", Provider: ProviderOpenAI, InputPerMillion: 2.50, OutputPerMillion: 10.00},
		{Model: "gpt-4o-mini", Provider: ProviderOpenAI, InputPerMillion: 0.15, OutputPerMillion: 0.60},
		{Model: "gpt-4.1", Provider: ProviderOpenAI, InputPerMillion: 2.00, OutputPerMillion: 8.00},
		{Model: "gpt-4.1-mini", Provider: ProviderOpenAI, InputPerMillion: 0.40, OutputPerMillion: 1.60},
		{Model: "claude-opus-4-6", Provider: ProviderAnthropic, InputPerMillion: 15.00, OutputPerMillion: 75.00},
		{Model: "claude-sonnet-4-6", Provider: ProviderAnthropic, InputPerMillion: 3.00, OutputPerMillion: 15.00},
		{Model: "claude-haiku-4-5", Provider: ProviderAnthropic, InputPerMillion: 0.80, OutputPerMillion: 4.00},
		{Model: "gemini-2.5-pro", Provider: ProviderGoogle, InputPerMillion: 1.25, OutputPerMillion: 10.00},
		{Model: "gemini-2.5-flash", Provider: ProviderGoogle, InputPerMillion: 0.15, OutputPerMillion: 0.60},
	}
}

// Load loads, overrides, and validates a configuration file.
func Load(path string) (Config, error) {
	resolvedPath, err := resolveConfigPath(path)
	if err != nil {
		return Config{}, err
	}

	cfg := DefaultConfig()
	if _, err := toml.DecodeFile(resolvedPath, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", resolvedPath, err)
	}

	if err := applyEnvOverrides(&cfg, os.Environ()); err != nil {
		return Config{}, err
	}

	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", resolvedPath, err)
	}

	return cfg, nil
}

func resolveConfigPath(path string) (string, error) {
	if path != "" {
		return path, nil
	}

	found := FindConfigFile()
	if found == "" {
		return "", fmt.Errorf("no config file found; checked --config, ./oberwatch.toml, $HOME/.config/oberwatch/oberwatch.toml, and /etc/oberwatch/oberwatch.toml")
	}

	return found, nil
}

// FindConfigFile returns the first config file found in the documented search order.
func FindConfigFile() string {
	candidates := []string{"./oberwatch.toml"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".config", "oberwatch", "oberwatch.toml"))
	}
	candidates = append(candidates, "/etc/oberwatch/oberwatch.toml")

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}

	return ""
}

func applyEnvOverrides(cfg *Config, environ []string) error {
	root := reflect.ValueOf(cfg).Elem()

	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, envPrefix) {
			continue
		}

		path := strings.Split(strings.TrimPrefix(key, envPrefix), "__")
		if err := setEnvOverride(root, path, value); err != nil {
			return fmt.Errorf("apply %s: %w", key, err)
		}
	}

	return nil
}

func setEnvOverride(value reflect.Value, path []string, raw string) error {
	current := value

	for i, segment := range path {
		fieldValue, err := findFieldByTOMLTag(current, segment)
		if err != nil {
			return err
		}

		if i == len(path)-1 {
			return setValueFromString(fieldValue, raw)
		}

		if fieldValue.Kind() != reflect.Struct {
			return fmt.Errorf("segment %q does not refer to a nested table", segment)
		}
		current = fieldValue
	}

	return nil
}

func findFieldByTOMLTag(value reflect.Value, segment string) (reflect.Value, error) {
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}

	valueType := value.Type()
	for i := 0; i < valueType.NumField(); i++ {
		field := valueType.Field(i)
		tag := field.Tag.Get("toml")
		if strings.EqualFold(tag, strings.ToLower(segment)) {
			return value.Field(i), nil
		}
	}

	return reflect.Value{}, fmt.Errorf("unknown config key segment %q", segment)
}

func setValueFromString(field reflect.Value, raw string) error {
	if field.Kind() == reflect.Pointer {
		field = field.Elem()
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(raw)
		return nil
	case reflect.Bool:
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("parse bool %q: %w", raw, err)
		}
		field.SetBool(parsed)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("parse int %q: %w", raw, err)
		}
		field.SetInt(parsed)
		return nil
	case reflect.Float32, reflect.Float64:
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("parse float %q: %w", raw, err)
		}
		field.SetFloat(parsed)
		return nil
	case reflect.Slice:
		return setSliceValueFromString(field, raw)
	default:
		return fmt.Errorf("unsupported override target kind %s", field.Kind())
	}
}

func setSliceValueFromString(field reflect.Value, raw string) error {
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		snippet := "value = " + raw
		switch field.Type().Elem().Kind() {
		case reflect.String:
			var decoded struct {
				Value []string `toml:"value"`
			}
			if _, err := toml.Decode(snippet, &decoded); err != nil {
				return fmt.Errorf("parse string slice %q: %w", raw, err)
			}
			field.Set(reflect.ValueOf(decoded.Value))
			return nil
		case reflect.Float64:
			var decoded struct {
				Value []float64 `toml:"value"`
			}
			if _, err := toml.Decode(snippet, &decoded); err != nil {
				return fmt.Errorf("parse float slice %q: %w", raw, err)
			}
			field.Set(reflect.ValueOf(decoded.Value))
			return nil
		}
	}

	parts := splitCommaSeparated(raw)
	result := reflect.MakeSlice(field.Type(), 0, len(parts))
	for _, part := range parts {
		item := reflect.New(field.Type().Elem()).Elem()
		if err := setValueFromString(item, part); err != nil {
			return err
		}
		result = reflect.Append(result, item)
	}
	field.Set(result)
	return nil
}

func splitCommaSeparated(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}

	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
