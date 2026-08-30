package scripts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/OberWatch/oberwatch/internal/config"
)

func installerPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate installer test source")
	}
	return filepath.Join(filepath.Dir(filename), "install.sh")
}

func installerLibrary(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(installerPath(t))
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	library := strings.TrimSuffix(string(contents), "\nmain \"$@\"\n") + "\n"
	if library == string(contents)+"\n" {
		t.Fatal("installer does not end with expected main invocation")
	}
	path := filepath.Join(t.TempDir(), "install-library.sh")
	if err := os.WriteFile(path, []byte(library), 0o600); err != nil {
		t.Fatalf("write installer library: %v", err)
	}
	return path
}

func runShellSnippet(t *testing.T, snippet string, args ...string) ([]byte, error) {
	t.Helper()
	commandArgs := append([]string{"-c", snippet, "test"}, args...)
	cmd := exec.Command("sh", commandArgs...)
	return cmd.CombinedOutput()
}

func TestInstallerIsPOSIXShell(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile(installerPath(t))
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}

	tests := map[string]string{
		"does not enable pipefail":         `(?m)^set .*pipefail`,
		"does not declare local variables": `(?m)^[[:space:]]*local([[:space:]]|$)`,
	}
	for name, pattern := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			forbidden := regexp.MustCompile(pattern)
			if forbidden.Match(contents) {
				t.Errorf("install.sh contains non-POSIX syntax matching %q", forbidden)
			}
		})
	}
}

func TestInstallerHelpHasNoInstallSideEffects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		executable string
		arguments  []string
		optional   bool
	}{
		{name: "bash", executable: "bash"},
		{name: "dash", executable: "dash", optional: true},
		{name: "sh", executable: "sh"},
		{name: "BusyBox sh", executable: "busybox", arguments: []string{"sh"}, optional: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			shell, lookPathErr := exec.LookPath(tt.executable)
			if lookPathErr != nil {
				if tt.optional {
					t.Skipf("optional shell %q is unavailable: %v", tt.executable, lookPathErr)
				}
				t.Fatalf("required shell %q is unavailable: %v", tt.executable, lookPathErr)
			}

			tempDir := t.TempDir()
			binDir := filepath.Join(tempDir, "bin")
			homeDir := filepath.Join(tempDir, "home")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatalf("create fake bin directory: %v", err)
			}
			if err := os.MkdirAll(homeDir, 0o755); err != nil {
				t.Fatalf("create home directory: %v", err)
			}
			callsPath := filepath.Join(tempDir, "calls")
			commands := []string{"curl", "chmod", "install", "mktemp", "uname", "id", "sudo", "mkdir", "cp", "chown", "tee", "systemctl", "tar", "sed", "head", "seq", "sleep"}
			for _, command := range commands {
				stub := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q >>%q\nexit 99\n", command, callsPath)
				if err := os.WriteFile(filepath.Join(binDir, command), []byte(stub), 0o755); err != nil {
					t.Fatalf("write %s stub: %v", command, err)
				}
			}

			installer, err := os.Open(installerPath(t))
			if err != nil {
				t.Fatalf("open installer: %v", err)
			}
			defer func() {
				if closeErr := installer.Close(); closeErr != nil {
					t.Errorf("close installer: %v", closeErr)
				}
			}()
			arguments := append(append([]string{}, tt.arguments...), "-s", "--", "--help")
			cmd := exec.Command(shell, arguments...)
			cmd.Stdin = installer
			cmd.Env = []string{"HOME=" + homeDir, "PATH=" + binDir}
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s -s -- --help failed: %v\noutput:\n%s", tt.name, err, output)
			}
			if !bytes.Contains(output, []byte("Usage:")) {
				t.Errorf("help output does not contain Usage:\n%s", output)
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("read side-effect calls: %v", err)
			}
			if strings.TrimSpace(string(calls)) != "" {
				t.Errorf("help invoked install command(s):\n%s", calls)
			}
		})
	}
}

func TestInstallerPublicClaimsAreLinuxOnly(t *testing.T) {
	t.Parallel()
	installer, err := os.ReadFile(installerPath(t))
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	readmePath := filepath.Join(filepath.Dir(filepath.Dir(installerPath(t))), "README.md")
	readme, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	changelogPath := filepath.Join(filepath.Dir(filepath.Dir(installerPath(t))), "CHANGELOG.md")
	changelog, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("read CHANGELOG: %v", err)
	}
	tests := []struct {
		name      string
		want      string
		forbidden string
		contents  []byte
	}{
		{
			name:      "installer help identifies Linux as supported",
			contents:  installer,
			want:      "Install or upgrade Oberwatch on Linux.",
			forbidden: "Install or upgrade Oberwatch on Linux or macOS.",
		},
		{
			name:      "README heading identifies Linux as supported",
			contents:  readme,
			want:      "### One-line install (Linux)",
			forbidden: "### One-line install (Linux/macOS)",
		},
		{
			name:      "CHANGELOG identifies Linux as supported",
			contents:  changelog,
			want:      "One-line install script for Linux with systemd service setup",
			forbidden: "One-line install script for Linux and macOS with systemd service setup",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !bytes.Contains(tt.contents, []byte(tt.want)) {
				t.Errorf("public installer documentation does not contain intended Linux-only wording %q", tt.want)
			}
			if bytes.Contains(tt.contents, []byte(tt.forbidden)) {
				t.Errorf("public installer documentation contains outdated macOS wording %q", tt.forbidden)
			}
		})
	}
}

func TestFetchLatestTagFromCanonicalRedirect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		effectiveURL string
		want         string
	}{
		{
			name:         "canonical release tag URL",
			effectiveURL: "https://github.com/OberWatch/oberwatch/releases/tag/v1.2.3",
			want:         "v1.2.3",
		},
		{
			name:         "canonical prerelease tag URL",
			effectiveURL: "https://github.com/OberWatch/oberwatch/releases/tag/v1.2.3-rc.1",
			want:         "v1.2.3-rc.1",
		},
		{
			name:         "hyphenated alphanumeric prerelease identifiers",
			effectiveURL: "https://github.com/OberWatch/oberwatch/releases/tag/v0.10.20-alpha-1.beta2.0",
			want:         "v0.10.20-alpha-1.beta2.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			library := installerLibrary(t)
			snippet := `. "$1"
EFFECTIVE_URL=$2
curl() {
	printf '%s\n' "$EFFECTIVE_URL"
}
fetch_latest_tag
printf '%s\n' "$LATEST_TAG"
`
			output, err := runShellSnippet(t, snippet, library, tt.effectiveURL)
			if err != nil {
				t.Fatalf("fetch_latest_tag failed: %v\n%s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != tt.want {
				t.Fatalf("LATEST_TAG = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFetchLatestTagRejectsUnexpectedEffectiveURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		effectiveURL string
	}{
		{name: "missing URL", effectiveURL: ""},
		{name: "redirect did not happen", effectiveURL: "https://github.com/OberWatch/oberwatch/releases/latest"},
		{name: "unexpected host", effectiveURL: "https://evil.example/OberWatch/oberwatch/releases/tag/v1.2.3"},
		{name: "unexpected repository", effectiveURL: "https://github.com/attacker/oberwatch/releases/tag/v1.2.3"},
		{name: "extra path segment", effectiveURL: "https://github.com/OberWatch/oberwatch/releases/tag/v1.2.3/payload"},
		{name: "query string", effectiveURL: "https://github.com/OberWatch/oberwatch/releases/tag/v1.2.3?payload=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			library := installerLibrary(t)
			snippet := `. "$1"
EFFECTIVE_URL=$2
curl() {
	printf '%s\n' "$EFFECTIVE_URL"
}
fetch_latest_tag
`
			output, err := runShellSnippet(t, snippet, library, tt.effectiveURL)
			if err == nil {
				t.Fatalf("fetch_latest_tag accepted unexpected effective URL; output:\n%s", output)
			}
			if !bytes.Contains(output, []byte("unexpected latest release URL")) {
				t.Fatalf("unexpected URL failure:\n%s", output)
			}
		})
	}
}

func TestFetchLatestTagRejectsUnsafeTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tag  string
	}{
		{name: "shell metacharacter", tag: "v1.2.3;payload"},
		{name: "missing version prefix", tag: "1.2.3"},
		{name: "missing version number", tag: "v"},
		{name: "missing minor and patch", tag: "v1"},
		{name: "missing patch", tag: "v1.2"},
		{name: "non-numeric patch", tag: "v1.2.x"},
		{name: "text appended to major", tag: "v1beta"},
		{name: "underscore in prerelease", tag: "v1.2.3-rc_1"},
		{name: "leading zero numeric prerelease", tag: "v1.2.3-rc.01"},
		{name: "leading zero first numeric prerelease", tag: "v1.2.3-01"},
		{name: "empty prerelease identifier", tag: "v1.2.3-rc..1"},
		{name: "build metadata", tag: "v1.2.3+build.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			library := installerLibrary(t)
			snippet := `. "$1"
EFFECTIVE_URL="https://github.com/OberWatch/oberwatch/releases/tag/$2"
curl() {
	printf '%s\n' "$EFFECTIVE_URL"
}
fetch_latest_tag
`
			output, err := runShellSnippet(t, snippet, library, tt.tag)
			if err == nil {
				t.Fatalf("fetch_latest_tag accepted unsafe release tag; output:\n%s", output)
			}
			if !bytes.Contains(output, []byte("unsafe release tag")) {
				t.Fatalf("unexpected unsafe-tag failure:\n%s", output)
			}
		})
	}
}

func TestPromptYesNoReadsExplicitTTYInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ttyReply   string
		stdinReply string
		wantYes    bool
	}{
		{name: "yes from tty upgrades despite piped stdin", ttyReply: "yes\n", stdinReply: "no\n", wantYes: true},
		{name: "no from tty declines despite piped stdin", ttyReply: "no\n", stdinReply: "yes\n", wantYes: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ttyPath := filepath.Join(t.TempDir(), "tty-input")
			if err := os.WriteFile(ttyPath, []byte(tt.ttyReply), 0o600); err != nil {
				t.Fatalf("write simulated tty input: %v", err)
			}
			environmentOverride := filepath.Join(t.TempDir(), "attacker-controlled-input")
			if err := os.WriteFile(environmentOverride, []byte("yes\n"), 0o600); err != nil {
				t.Fatalf("write environment override input: %v", err)
			}
			library := installerLibrary(t)
			snippet := `. "$1"
PROMPT_INPUT_PATH=$3
if prompt_yes_no "Upgrade? " "$2"; then
	printf 'yes\n'
else
	printf 'no\n'
fi
`
			commandArgs := []string{"-c", snippet, "test", library, ttyPath, environmentOverride}
			cmd := exec.Command("sh", commandArgs...)
			cmd.Stdin = strings.NewReader(tt.stdinReply)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("prompt_yes_no failed: %v\n%s", err, output)
			}
			gotYes := strings.HasSuffix(strings.TrimSpace(string(output)), "yes")
			if gotYes != tt.wantYes {
				t.Fatalf("prompt result output = %q, want yes = %t", output, tt.wantYes)
			}
		})
	}
}

func TestPromptYesNoSafelyRefusesWithoutTTY(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		inputPath string
	}{
		{name: "missing controlling terminal", inputPath: "missing-tty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inputPath := filepath.Join(t.TempDir(), tt.inputPath)
			library := installerLibrary(t)
			snippet := `. "$1"
set +e
prompt_yes_no "Upgrade? " "$2"
status=$?
set -e
printf 'status=%s\n' "$status"
exit "$status"
`
			output, err := runShellSnippet(t, snippet, library, inputPath)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Fatalf("noninteractive refusal status = %v, want exit 2\n%s", err, output)
			}
			if !bytes.Contains(output, []byte("status=2")) {
				t.Fatalf("noninteractive prompt did not return status 2:\n%s", output)
			}
			if !bytes.Contains(output, []byte("cannot confirm upgrade without a terminal")) {
				t.Fatalf("noninteractive prompt did not visibly explain refusal:\n%s", output)
			}
		})
	}
}

func TestMainDistinguishesDeclineFromTerminalFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		promptStatus int
		wantStatus   int
		wantRefusal  bool
	}{
		{name: "explicit no is a successful skip", promptStatus: 1, wantStatus: 0},
		{name: "terminal failure is an error", promptStatus: 2, wantStatus: 2, wantRefusal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			installedPath := filepath.Join(tempDir, "oberwatch")
			if err := os.WriteFile(installedPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
				t.Fatalf("write installed binary: %v", err)
			}
			fetchMarker := filepath.Join(tempDir, "fetched")
			library := installerLibrary(t)
			snippet := `. "$1"
INSTALL_PATH=$2
PROMPT_STATUS=$3
FETCH_MARKER=$4
need_cmd() { :; }
print_banner() { :; }
detect_platform() { RELEASE_OS=linux; RELEASE_ARCH=amd64; }
resolve_user_home() { printf '%s\n' "$HOME"; }
prompt_yes_no() {
	if [ "$#" -ne 1 ]; then
		printf 'main passed a production input override\n' >&2
		return 99
	fi
	if [ "$PROMPT_STATUS" -eq 2 ]; then
		printf 'Refusing upgrade: cannot confirm upgrade without a terminal.\n' >&2
	fi
	return "$PROMPT_STATUS"
}
fetch_latest_tag() { : >"$FETCH_MARKER"; }
main
`
			cmd := exec.Command("sh", "-c", snippet, "test", library, installedPath, fmt.Sprint(tt.promptStatus), fetchMarker)
			cmd.Env = append(os.Environ(), "HOME="+tempDir, "PROMPT_INPUT_PATH="+filepath.Join(tempDir, "inherited-override"))
			output, err := cmd.CombinedOutput()
			gotStatus := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("run main: %v\n%s", err, output)
				}
				gotStatus = exitErr.ExitCode()
			}
			if gotStatus != tt.wantStatus {
				t.Fatalf("main status = %d, want %d\n%s", gotStatus, tt.wantStatus, output)
			}
			if gotRefusal := bytes.Contains(output, []byte("cannot confirm upgrade without a terminal")); gotRefusal != tt.wantRefusal {
				t.Fatalf("main refusal visibility = %t, want %t\n%s", gotRefusal, tt.wantRefusal, output)
			}
			if _, statErr := os.Stat(fetchMarker); !os.IsNotExist(statErr) {
				t.Fatalf("main continued into release fetch after non-yes prompt; stat error = %v", statErr)
			}
		})
	}
}

func TestWriteSystemdServiceEnvironmentIsAcceptedConfigOverride(t *testing.T) {
	tempDir := t.TempDir()
	linuxStateDir := filepath.Join(tempDir, "state")
	unitPath := filepath.Join(tempDir, "unit")

	library := installerLibrary(t)
	snippet := `. "$1"
SERVICE_NAME=oberwatch
LINUX_SERVICE_USER=oberwatch
INSTALL_PATH=/usr/local/bin/oberwatch
LINUX_STATE_DIR=$2
UNIT_PATH=$3
sudo_cmd() { "$@"; }
tee() { cat >"$UNIT_PATH"; }
write_systemd_service
`
	output, err := runShellSnippet(t, snippet, library, linuxStateDir, unitPath)
	if err != nil {
		t.Fatalf("write_systemd_service failed: %v\n%s", err, output)
	}

	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read generated unit: %v", err)
	}

	environmentLine := regexp.MustCompile(`(?m)^Environment=(OBERWATCH_\S+)$`).FindSubmatch(unit)
	if environmentLine == nil {
		t.Fatalf("generated unit has no Environment=OBERWATCH_* line:\n%s", unit)
	}
	key, value, ok := strings.Cut(string(environmentLine[1]), "=")
	if !ok {
		t.Fatalf("generated Environment line is not KEY=VALUE: %q", environmentLine[1])
	}

	const wantKey = "OBERWATCH_TRACE__SQLITE_PATH"
	if key != wantKey {
		t.Fatalf("generated Environment key = %q, want %q", key, wantKey)
	}

	wantValue := filepath.Join(linuxStateDir, "data", "oberwatch.db")
	if value != wantValue {
		t.Fatalf("Environment %s = %q, want %q", key, value, wantValue)
	}

	if bytes.Contains(unit, []byte("OBERWATCH_DATA_DIR")) {
		t.Fatalf("generated unit still sets OBERWATCH_DATA_DIR, want only %s:\n%s", wantKey, unit)
	}

	configPath := filepath.Join(tempDir, "oberwatch.toml")
	if writeErr := os.WriteFile(configPath, nil, 0o644); writeErr != nil {
		t.Fatalf("write empty config: %v", writeErr)
	}

	t.Setenv(key, value)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load rejected the installer's Environment override %s=%s: %v", key, value, err)
	}
	if cfg.Trace.Storage == config.TraceStorageSQLite && !strings.HasPrefix(cfg.Trace.SQLitePath, linuxStateDir) {
		t.Fatalf("trace.sqlite_path = %q, want a path under the service-owned state directory %q", cfg.Trace.SQLitePath, linuxStateDir)
	}
}

func TestPrintSuccessLinuxReportsServiceOwnedPaths(t *testing.T) {
	t.Parallel()
	library := installerLibrary(t)
	snippet := `. "$1"
USER_HOME=/root
USER_STATE_DIR=/root/.oberwatch
USER_CONFIG_PATH=/root/.oberwatch/oberwatch.toml
LINUX_SERVICE_USER=oberwatch
LINUX_SERVICE_HOME=/home/oberwatch
LINUX_STATE_DIR=/home/oberwatch/.oberwatch
SERVICE_NAME=oberwatch
print_success_linux
`
	output, err := runShellSnippet(t, snippet, library)
	if err != nil {
		t.Fatalf("print_success_linux failed: %v\n%s", err, output)
	}

	wantConfig := "/home/oberwatch/.oberwatch/oberwatch.toml"
	wantData := "/home/oberwatch/.oberwatch/data/"
	unwantedConfig := "/root/.oberwatch/oberwatch.toml"
	unwantedData := "/root/.oberwatch/data/"

	if !bytes.Contains(output, []byte(wantConfig)) {
		t.Errorf("print_success_linux output does not report the service-owned config path %q:\n%s", wantConfig, output)
	}
	if !bytes.Contains(output, []byte(wantData)) {
		t.Errorf("print_success_linux output does not report the service-owned data path %q:\n%s", wantData, output)
	}
	if bytes.Contains(output, []byte(unwantedConfig)) {
		t.Errorf("print_success_linux output reports the invoking user's config path %q instead of the service-owned path:\n%s", unwantedConfig, output)
	}
	if bytes.Contains(output, []byte(unwantedData)) {
		t.Errorf("print_success_linux output reports the invoking user's data path %q instead of the service-owned path:\n%s", unwantedData, output)
	}
}

func TestSyncLinuxServiceStatePreservesCopyArguments(t *testing.T) {
	t.Parallel()
	tests := map[string]func(string) string{
		"spaces": func(_ string) string { return "user state with spaces" },
		"apostrophe injection": func(marker string) string {
			return "user-state'; touch '" + marker + "'; echo '"
		},
	}
	for name, stateName := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tempDir := t.TempDir()
			marker := filepath.Join(tempDir, "injected")
			userStateDir := filepath.Join(tempDir, stateName(marker))
			linuxStateDir := filepath.Join(tempDir, "linux state")
			if err := os.MkdirAll(filepath.Join(userStateDir, "data"), 0o755); err != nil {
				t.Fatalf("create user data directory: %v", err)
			}
			binDir := filepath.Join(tempDir, "bin")
			if err := os.Mkdir(binDir, 0o755); err != nil {
				t.Fatalf("create fake bin directory: %v", err)
			}
			callsPath := filepath.Join(tempDir, "copy-args")
			cpStub := fmt.Sprintf("#!/bin/sh\nprintf 'argc=%%s\\n' \"$#\" >>%q\nfor arg do printf '<%%s>\\n' \"$arg\" >>%q; done\nexit 1\n", callsPath, callsPath)
			if err := os.WriteFile(filepath.Join(binDir, "cp"), []byte(cpStub), 0o755); err != nil {
				t.Fatalf("write cp stub: %v", err)
			}

			library := installerLibrary(t)
			snippet := `. "$1"
USER_STATE_DIR=$2
USER_CONFIG_PATH=$2/oberwatch.toml
LINUX_STATE_DIR=$3
LINUX_SERVICE_HOME=$3
LINUX_SERVICE_USER=oberwatch
PATH=$4:$PATH
sudo_cmd() {
  case "$1" in
    sh) "$@" ;;
    *) return 0 ;;
  esac
}
sync_linux_service_state
`
			output, err := runShellSnippet(t, snippet, library, userStateDir, linuxStateDir, binDir)
			if err != nil {
				t.Fatalf("sync_linux_service_state failed: %v\n%s", err, output)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("HOME-derived state path executed injected command; marker stat error = %v", statErr)
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatalf("read copy arguments: %v", err)
			}
			want := fmt.Sprintf("argc=3\n<-R>\n<%s/data/.>\n<%s/data/>\n", userStateDir, linuxStateDir)
			if string(calls) != want {
				t.Fatalf("best-effort copy arguments =\n%s\nwant:\n%s", calls, want)
			}
		})
	}
}

// installerDefaultConfig runs the installer's own write_default_config helper and
// returns exactly what a fresh install would land on disk.
func installerDefaultConfig(t *testing.T) string {
	t.Helper()
	library := installerLibrary(t)
	target := filepath.Join(t.TempDir(), "oberwatch.toml")
	snippet := `. "$1"
write_default_config "$2"
`
	if output, err := runShellSnippet(t, snippet, library, target); err != nil {
		t.Fatalf("write_default_config failed: %v\n%s", err, output)
	}

	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	return string(contents)
}

func TestInstallerDefaultConfigHasNoInventedAgents(t *testing.T) {
	t.Parallel()

	contents := installerDefaultConfig(t)

	cfg := config.DefaultConfig()
	if _, err := toml.Decode(contents, &cfg); err != nil {
		t.Fatalf("toml.Decode() error = %v", err)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("config.Validate() error = %v", err)
	}
	if len(cfg.Gate.APIKeyMap) != 0 {
		t.Fatalf("installer default config declares %d gate.api_key_map entries, want 0", len(cfg.Gate.APIKeyMap))
	}

	for _, name := range []string{"email-agent", "finance-agent"} {
		if strings.Contains(contents, name) {
			t.Fatalf("installer default config still mentions the invented agent %q", name)
		}
	}
}
