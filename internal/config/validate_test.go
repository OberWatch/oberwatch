package config

import (
	"strings"
	"testing"
)

func TestValidate_DefaultConfigIsValid(t *testing.T) {
	t.Parallel()

	if err := Validate(DefaultConfig()); err != nil {
		t.Fatalf("Validate(DefaultConfig()) error = %v", err)
	}
}

func TestValidate_InvalidConfigurations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*Config)
		wantSubstr string
	}{
		{
			name: "invalid server log level",
			mutate: func(cfg *Config) {
				cfg.Server.LogLevel = LogLevel("verbose")
			},
			wantSubstr: "server.log_level",
		},
		{
			name: "missing paired tls key",
			mutate: func(cfg *Config) {
				cfg.Server.TLSCert = "cert.pem"
			},
			wantSubstr: "server.tls_cert and server.tls_key",
		},
		{
			name: "custom upstream requires base url",
			mutate: func(cfg *Config) {
				cfg.Upstream.DefaultProvider = ProviderCustom
				cfg.Upstream.Custom.BaseURL = ""
			},
			wantSubstr: "upstream.custom.base_url",
		},

		{
			name: "alert thresholds must increase",
			mutate: func(cfg *Config) {
				cfg.Gate.AlertThresholdsPct = []float64{80, 50}
			},
			wantSubstr: "gate.alert_thresholds_pct must be strictly increasing",
		},
		{
			name: "api key identification requires mapping",
			mutate: func(cfg *Config) {
				cfg.Gate.Identification.Method = IdentificationMethodAPIKey
				cfg.Gate.APIKeyMap = nil
			},
			wantSubstr: "gate.api_key_map must not be empty",
		},
		{
			name: "email alerts require recipients",
			mutate: func(cfg *Config) {
				cfg.Alerts.Email.Enabled = true
				cfg.Alerts.Email.SMTPHost = "smtp.example.com"
				cfg.Alerts.Email.From = "from@example.com"
				cfg.Alerts.Email.To = nil
			},
			wantSubstr: "alerts.email.to",
		},
		{
			name: "trace storage sqlite requires path",
			mutate: func(cfg *Config) {
				cfg.Trace.SQLitePath = ""
			},
			wantSubstr: "trace.sqlite_path",
		},
		{
			name: "test timeout must be valid duration",
			mutate: func(cfg *Config) {
				cfg.Test.Timeout = "nope"
			},
			wantSubstr: "test.timeout",
		},
		{
			name: "pricing provider must be valid",
			mutate: func(cfg *Config) {
				cfg.Pricing[0].Provider = ProviderConfigName("bad")
			},
			wantSubstr: "pricing[0].provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultConfig()
			tt.mutate(&cfg)

			err := Validate(cfg)
			if err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestValidate_AggregatesMultipleProblems(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Server.Port = 0
	cfg.Server.LogFormat = LogFormat("yaml")

	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}

	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("Validate() error type = %T, want *ValidationError", err)
	}
	if len(validationErr.Problems) < 2 {
		t.Fatalf("len(Problems) = %d, want at least 2", len(validationErr.Problems))
	}
}

func TestValidatePositiveDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		value      string
		wantErrSub string
	}{
		{name: "valid", value: "5s"},
		{name: "invalid syntax", value: "later", wantErrSub: "must be a valid duration"},
		{name: "non-positive", value: "0s", wantErrSub: "must be greater than 0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validatePositiveDuration("field", tt.value)
			if tt.wantErrSub == "" {
				if err != nil {
					t.Fatalf("validatePositiveDuration() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("validatePositiveDuration() error = %v, want substring %q", err, tt.wantErrSub)
			}
		})
	}
}

func TestValidate_HelperBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*Config)
		wantSubstr string
	}{
		{
			name: "api key map entry requires values",
			mutate: func(cfg *Config) {
				cfg.Gate.APIKeyMap = []APIKeyMapEntry{{}}
			},
			wantSubstr: "gate.api_key_map[0].api_key_prefix",
		},
		{
			name: "trace storage must be valid",
			mutate: func(cfg *Config) {
				cfg.Trace.Storage = TraceStorage("disk")
			},
			wantSubstr: "trace.storage",
		},
		{
			name: "test judge provider must be valid",
			mutate: func(cfg *Config) {
				cfg.Test.Judge.Provider = ProviderConfigName("invalid")
			},
			wantSubstr: "test.judge.provider",
		},
		{
			name: "pricing model must not be empty",
			mutate: func(cfg *Config) {
				cfg.Pricing[0].Model = ""
			},
			wantSubstr: "pricing[0].model",
		},
		{
			name: "upstream base url must not be empty",
			mutate: func(cfg *Config) {
				cfg.Upstream.OpenAI.BaseURL = ""
			},
			wantSubstr: "upstream.openai.base_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultConfig()
			tt.mutate(&cfg)

			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.wantSubstr)
			}
		})
	}
}
