package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config path sources reported by ValidateFile.
const (
	// SourceFlag means the path came from --config.
	SourceFlag = "--config flag"
	// SourceSearch means the path was found in the documented search order.
	SourceSearch = "search order"
)

// NotFoundError reports that no config file exists at the requested location.
type NotFoundError struct {
	// Path is the path that was checked, or empty when the search order found nothing.
	Path string
	// Source says how Path was chosen. Defaults to SourceFlag when unset.
	Source string
}

// Error describes the missing file and where it was looked for.
func (e *NotFoundError) Error() string {
	if e.Path == "" {
		return "no config file found; checked --config, " + strings.Join(configSearchCandidates(), ", ")
	}

	source := e.Source
	if source == "" {
		source = SourceFlag
	}
	return fmt.Sprintf("config file %q not found (source: %s)", e.Path, source)
}

// FileReport describes a config file that loaded and validated successfully.
type FileReport struct {
	// Path is the file that was validated, exactly as it will be shown to the user.
	Path string
	// Source says how Path was chosen.
	Source string
	// Warnings are non-fatal onboarding notes about the config.
	Warnings []string
	Config   Config
}

// ValidateFile resolves, parses, and validates a config file for `oberwatch validate`.
//
// A missing file yields a *NotFoundError. A malformed file yields an error that
// names the file and the TOML line. A semantically invalid file yields an error
// wrapping *ValidationError with one line per offending key.
func ValidateFile(path string) (FileReport, error) {
	resolvedPath, source, err := resolveConfigPathWithSource(path)
	if err != nil {
		return FileReport{}, err
	}

	cfg, err := loadResolved(resolvedPath, source)
	if err != nil {
		return FileReport{}, err
	}

	return FileReport{
		Path:     resolvedPath,
		Source:   source,
		Warnings: onboardingWarnings(cfg),
		Config:   cfg,
	}, nil
}

// loadResolved parses, overrides, and validates an already-resolved path.
// source is carried through so a file that disappears after the search order
// picked it is not reported as if it came from --config.
func loadResolved(resolvedPath, source string) (Config, error) {
	info, statErr := os.Stat(resolvedPath)
	switch {
	case statErr != nil && errors.Is(statErr, os.ErrNotExist):
		return Config{}, &NotFoundError{Path: resolvedPath, Source: source}
	case statErr != nil:
		return Config{}, fmt.Errorf("read config %q: %w", resolvedPath, statErr)
	case info.IsDir():
		return Config{}, fmt.Errorf("config path %q is a directory, want a TOML file", resolvedPath)
	}

	cfg := DefaultConfig()
	meta, err := toml.DecodeFile(resolvedPath, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", resolvedPath, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		return Config{}, fmt.Errorf("parse config %q: unknown key(s): %s", resolvedPath, strings.Join(keys, ", "))
	}

	if err := applyEnvOverrides(&cfg, os.Environ()); err != nil {
		return Config{}, err
	}

	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", resolvedPath, err)
	}

	return cfg, nil
}

// onboardingWarnings returns notes about settings that no longer affect
// management auth. Management API and dashboard access is session-based: the
// first visit to the dashboard creates the admin account.
func onboardingWarnings(cfg Config) []string {
	warnings := make([]string, 0, 2)
	if strings.TrimSpace(cfg.Server.AdminToken) != "" {
		warnings = append(warnings,
			"server.admin_token is set but not used: management API and dashboard auth is session-based; complete first-run setup in the dashboard instead")
	}
	if !cfg.Server.Dashboard {
		warnings = append(warnings,
			"server.dashboard is false: first-run setup is still available by POSTing to /_oberwatch/api/v1/setup")
	}
	return warnings
}

func resolveConfigPathWithSource(path string) (string, string, error) {
	if path != "" {
		return path, SourceFlag, nil
	}

	found := FindConfigFile()
	if found == "" {
		return "", "", &NotFoundError{}
	}

	return found, SourceSearch, nil
}

func configSearchCandidates() []string {
	candidates := []string{"./oberwatch.toml"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".config", "oberwatch", "oberwatch.toml"))
	}
	return append(candidates, "/etc/oberwatch/oberwatch.toml")
}
