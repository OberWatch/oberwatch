package upgrade

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	// maxHandoffBytes bounds a handoff file. Both files hold a handful of short
	// fields, so anything larger is not one of ours.
	maxHandoffBytes = 4 << 10

	// maxMessageRunes bounds the operator-facing message carried in a result.
	maxMessageRunes = 512
)

// ErrNoRequest means there is no upgrade request waiting. It is the normal
// state, not a failure.
var ErrNoRequest = errors.New("no upgrade request is waiting")

// ResultStatus is the outcome the privileged applier recorded.
type ResultStatus string

// Result statuses.
const (
	// ResultSucceeded means the new binary is installed and the service was
	// restarted onto it.
	ResultSucceeded ResultStatus = "succeeded"

	// ResultRestartRequired means the new binary is installed but the service
	// was not restarted, so the old version is still running.
	ResultRestartRequired ResultStatus = "restart_required"

	// ResultFailed means nothing was installed.
	ResultFailed ResultStatus = "failed"
)

// Valid reports whether a status is one this package writes.
func (s ResultStatus) Valid() bool {
	switch s {
	case ResultSucceeded, ResultRestartRequired, ResultFailed:
		return true
	default:
		return false
	}
}

// Request is the handoff record the unprivileged server writes and the
// privileged applier reads.
//
// It names a version and nothing else. Every path, URL and command the applier
// uses is derived from that version and from package constants, so this file
// cannot point the applier at another host, archive, directory or program even
// if something manages to write it.
type Request struct {
	// Tag is the release tag to install, in "vMAJOR.MINOR.PATCH" form.
	Tag string `json:"tag"`

	// From is the version that was running when the request was written. It is
	// recorded for the result and is never used to decide anything.
	From string `json:"from"`

	// RequestedAt is when the request was written, in RFC3339.
	RequestedAt string `json:"requested_at"`
}

// Result is what the privileged applier leaves behind. The server reads it
// after the restart, which is how the dashboard reports an outcome that
// outlives the process that asked for it.
type Result struct {
	// Status is the recorded outcome.
	Status ResultStatus `json:"status"`

	// Tag is the release that was being installed.
	Tag string `json:"tag"`

	// From is the version that was replaced.
	From string `json:"from"`

	// Message explains the outcome, including what to do next after a failure
	// or when a restart is still needed.
	Message string `json:"message"`

	// FinishedAt is when the applier finished, in RFC3339.
	FinishedAt string `json:"finished_at"`
}

// WriteRequest writes an upgrade request atomically.
//
// The file is written under a temporary name and renamed into place, so the
// privileged applier — which is started by the request file appearing — never
// observes a half-written request.
func WriteRequest(stateDir string, request Request) error {
	version, err := ParseReleaseTag(request.Tag)
	if err != nil {
		return fmt.Errorf("upgrade request tag: %w", err)
	}
	if !version.IsStable() {
		return fmt.Errorf("%w: upgrade request tag %s is a prerelease", ErrInvalidVersion, request.Tag)
	}
	request.Tag = version.Tag()

	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode upgrade request: %w", err)
	}
	return writeFileAtomic(RequestPath(stateDir), encoded, 0o600)
}

// ReadRequest reads and validates the waiting upgrade request.
//
// It returns ErrNoRequest when there is none. Every other unexpected condition
// — a symlink, an oversized file, an unknown field, a tag that is not a stable
// release tag — is an error, because the privileged applier must refuse a
// request it does not fully understand rather than act on part of it.
func ReadRequest(stateDir string) (Request, Version, error) {
	path := RequestPath(stateDir)

	raw, err := readHandoffFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Request{}, Version{}, ErrNoRequest
		}
		return Request{}, Version{}, fmt.Errorf("read upgrade request: %w", err)
	}

	var request Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&request); decodeErr != nil {
		return Request{}, Version{}, fmt.Errorf("decode upgrade request: %w", decodeErr)
	}
	if decoder.More() {
		return Request{}, Version{}, errors.New("decode upgrade request: trailing content after the request object")
	}

	version, err := ParseReleaseTag(strings.TrimSpace(request.Tag))
	if err != nil {
		return Request{}, Version{}, fmt.Errorf("upgrade request tag: %w", err)
	}
	if !version.IsStable() {
		return Request{}, Version{}, fmt.Errorf("%w: upgrade request tag %s is a prerelease", ErrInvalidVersion, request.Tag)
	}
	if trimmed := strings.TrimSpace(request.From); trimmed != "" {
		if _, err := ParseVersion(trimmed); err != nil {
			return Request{}, Version{}, fmt.Errorf("upgrade request from: %w", err)
		}
	}
	if trimmed := strings.TrimSpace(request.RequestedAt); trimmed != "" {
		if _, err := time.Parse(time.RFC3339, trimmed); err != nil {
			return Request{}, Version{}, fmt.Errorf("upgrade request requested_at: %w", err)
		}
	}

	request.Tag = version.Tag()
	return request, version, nil
}

// RemoveRequest deletes the waiting request. It is not an error when there is
// none.
func RemoveRequest(stateDir string) error {
	if err := os.Remove(RequestPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove upgrade request: %w", err)
	}
	return nil
}

// WriteResult records an outcome atomically. The file is world-readable because
// the privileged applier writes it and the unprivileged service reads it.
func WriteResult(stateDir string, result Result) error {
	if !result.Status.Valid() {
		return fmt.Errorf("upgrade result status %q is not a known status", result.Status)
	}
	result.Message = sanitizeMessage(result.Message)

	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode upgrade result: %w", err)
	}
	return writeFileAtomic(ResultPath(stateDir), encoded, 0o644)
}

// RemoveResult deletes a recorded outcome, so a new attempt does not show the
// previous one as its own. It is not an error when there is none.
func RemoveResult(stateDir string) error {
	if err := os.Remove(ResultPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove upgrade result: %w", err)
	}
	return nil
}

// ReadResult reads the recorded outcome, if any.
//
// The second return value is false when there is no result to report, which
// includes a result file that does not validate: showing a half-understood
// outcome in the dashboard would be worse than showing none.
func ReadResult(stateDir string) (Result, bool, error) {
	path := ResultPath(stateDir)

	raw, err := readHandoffFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, false, nil
		}
		return Result{}, false, fmt.Errorf("read upgrade result: %w", err)
	}

	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, false, fmt.Errorf("decode upgrade result: %w", err)
	}
	if !result.Status.Valid() {
		return Result{}, false, fmt.Errorf("upgrade result status %q is not a known status", result.Status)
	}
	if _, err := ParseReleaseTag(strings.TrimSpace(result.Tag)); err != nil {
		return Result{}, false, fmt.Errorf("upgrade result tag: %w", err)
	}

	result.Message = sanitizeMessage(result.Message)
	return result, true, nil
}

// readHandoffFile reads one handoff file, bounded.
//
// The file is opened without following a symlink and read through that one
// handle, and the read itself is bounded rather than the size being stat'ed
// first: a file that grew, or was replaced, between a stat and a read would
// otherwise be read in full.
func readHandoffFile(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (mode %s)", path, info.Mode())
	}

	raw, err := io.ReadAll(io.LimitReader(file, maxHandoffBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > maxHandoffBytes {
		return nil, fmt.Errorf("%s is over the %d byte bound", path, maxHandoffBytes)
	}
	return raw, nil
}

// sanitizeMessage bounds a message and drops control characters, so a recorded
// message is always a short single-line string wherever it is displayed.
func sanitizeMessage(message string) string {
	var builder strings.Builder
	runes := 0
	for _, r := range message {
		if runes >= maxMessageRunes {
			builder.WriteString("…")
			break
		}
		switch {
		case r == '\n', r == '\t', r == '\r':
			builder.WriteRune(' ')
		case unicode.IsControl(r):
			continue
		default:
			builder.WriteRune(r)
		}
		runes++
	}
	return strings.TrimSpace(strings.Join(strings.Fields(builder.String()), " "))
}

// writeFileAtomic writes data to a temporary file in the same directory and
// renames it into place.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".handoff-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tempPath := temp.Name()

	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}

	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", tempPath, err)
	}
	if err := temp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", tempPath, err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("flush %s: %w", tempPath, err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("rename %s to %s: %w", tempPath, path, err)
	}
	return nil
}
