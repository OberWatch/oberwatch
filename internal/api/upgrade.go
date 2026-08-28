package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/OberWatch/oberwatch/internal/upgrade"
)

// upgradeManager is the upgrade behaviour the API needs.
//
// upgrade.Manager satisfies it. Note that Prepare takes no target: what gets
// installed is decided by the server's own release check, never by the request,
// so there is no field for a tag, URL or path to arrive in.
type upgradeManager interface {
	Status(ctx context.Context) upgrade.Status
	Prepare(ctx context.Context) (upgrade.Version, error)
}

// upgradeStatusResponse is the authenticated upgrade status the dashboard
// reads. Optional fields are omitted rather than sent empty, so the dashboard
// can tell "not known" from "known to be empty".
//
//nolint:govet // Keep fields grouped to mirror the API contract order.
type upgradeStatusResponse struct {
	CurrentVersion    string                 `json:"current_version"`
	LatestVersion     string                 `json:"latest_version,omitempty"`
	UpdateAvailable   bool                   `json:"update_available"`
	CheckedAt         string                 `json:"checked_at,omitempty"`
	CheckError        string                 `json:"check_error,omitempty"`
	Supported         bool                   `json:"supported"`
	UnsupportedReason string                 `json:"unsupported_reason,omitempty"`
	Fallback          string                 `json:"fallback,omitempty"`
	InProgress        bool                   `json:"in_progress"`
	LastResult        *upgradeResultResponse `json:"last_result,omitempty"`
}

// upgradeResultResponse is the recorded outcome of the most recent apply.
//
//nolint:govet // Keep fields grouped to mirror the API contract order.
type upgradeResultResponse struct {
	Status     string `json:"status"`
	Tag        string `json:"tag"`
	From       string `json:"from,omitempty"`
	Message    string `json:"message"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// upgradeStartResponse is returned once an upgrade has been verified and handed
// to the privileged applier.
//
//nolint:govet // Keep fields grouped to mirror the API contract order.
type upgradeStartResponse struct {
	Status  string `json:"status"`
	Tag     string `json:"tag"`
	Message string `json:"message"`
}

// upgradeHandoffMessage states plainly what happens next. The dashboard shows
// this, and it has to be true: the archive is verified, the swap and restart are
// done by the privileged applier moments later, and nothing touches config or
// data.
const upgradeHandoffMessage = "Release archive verified against the published checksum and handed to the privileged installer. " +
	"The service restarts within a few seconds, so the dashboard and proxy are briefly unavailable. " +
	"Configuration and data are not changed, and the previous binary is kept for rollback."

func (s *Server) handleUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}
	if s.upgrader == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "upgrade manager is not configured", "", 0, 0)
		return
	}

	writeJSON(w, http.StatusOK, encodeUpgradeStatus(s.upgrader.Status(r.Context())))
}

// handleUpgrade starts an upgrade to the newest stable release.
//
// The request body is deliberately not read. There is no supported way to name
// a version, a tag, a URL, a path or a command from a request: the target comes
// from the server's own release check and everything else is derived from it. A
// body naming one is therefore ignored rather than validated, which is the only
// way to be sure it cannot take effect.
func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}
	if s.upgrader == nil {
		writeError(w, http.StatusInternalServerError, "config_error", "upgrade manager is not configured", "", 0, 0)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), upgrade.DownloadTimeout)
	defer cancel()

	target, err := s.upgrader.Prepare(ctx)
	if err != nil {
		status, code := upgradeErrorResponse(err)
		writeError(w, status, code, err.Error(), "", 0, 0)
		return
	}

	writeJSON(w, http.StatusAccepted, upgradeStartResponse{
		Status:  "applying",
		Tag:     target.Tag(),
		Message: upgradeHandoffMessage,
	})
}

// upgradeErrorResponse maps a prepare failure onto a status and a stable error
// code the dashboard can branch on.
func upgradeErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, upgrade.ErrUnsupported):
		return http.StatusConflict, "upgrade_unsupported"
	case errors.Is(err, upgrade.ErrNoUpdate):
		return http.StatusConflict, "upgrade_not_available"
	case errors.Is(err, upgrade.ErrInProgress):
		return http.StatusConflict, "upgrade_in_progress"
	case errors.Is(err, upgrade.ErrChecksumMismatch), errors.Is(err, upgrade.ErrChecksumMissing):
		return http.StatusBadGateway, "upgrade_verification_failed"
	case errors.Is(err, upgrade.ErrReleaseUnavailable), errors.Is(err, upgrade.ErrArtifactUnavailable), errors.Is(err, upgrade.ErrArtifactTooLarge):
		return http.StatusServiceUnavailable, "upgrade_source_unavailable"
	default:
		return http.StatusInternalServerError, "upgrade_failed"
	}
}

func encodeUpgradeStatus(status upgrade.Status) upgradeStatusResponse {
	response := upgradeStatusResponse{
		CurrentVersion:    status.CurrentVersion,
		LatestVersion:     status.LatestVersion,
		UpdateAvailable:   status.UpdateAvailable,
		CheckError:        status.CheckError,
		Supported:         status.Supported,
		UnsupportedReason: status.UnsupportedReason,
		Fallback:          status.Fallback,
		InProgress:        status.InProgress,
	}
	if status.CheckedAt != nil {
		response.CheckedAt = status.CheckedAt.UTC().Format(time.RFC3339)
	}
	if status.LastResult != nil {
		response.LastResult = &upgradeResultResponse{
			Status:     string(status.LastResult.Status),
			Tag:        status.LastResult.Tag,
			From:       status.LastResult.From,
			Message:    status.LastResult.Message,
			FinishedAt: status.LastResult.FinishedAt,
		}
	}
	return response
}
