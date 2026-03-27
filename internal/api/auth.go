package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	adminPasswordHashKey = "admin_password_hash"
	adminUsernameKey     = "admin_username"
	sessionCookieName    = "oberwatch_session"
	sessionExpiresAtKey  = "session_expires_at"
	sessionTokenKey      = "session_token"
	setupCompleteKey     = "setup_complete"
	sessionTTL           = 24 * time.Hour
)

type authStatusResponse struct {
	SetupComplete bool `json:"setup_complete"`
	Authenticated bool `json:"authenticated"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

type setupRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *Server) authorized(r *http.Request) bool {
	valid, _ := s.isAuthenticated(r.Context(), r)
	return valid
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w)
		return
	}

	setupComplete, err := s.setupComplete(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	authenticated, err := s.isAuthenticated(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	writeJSON(w, http.StatusOK, authStatusResponse{
		SetupComplete: setupComplete,
		Authenticated: authenticated,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}

	setupComplete, err := s.setupComplete(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	if setupComplete {
		writeError(w, http.StatusConflict, "setup_complete", "setup has already been completed", "", 0, 0)
		return
	}

	var payload setupRequest
	decodeErr := decodeRequestBody(r, &payload)
	if decodeErr != nil {
		writeError(w, http.StatusBadRequest, "config_error", decodeErr.Error(), "", 0, 0)
		return
	}
	validateErr := validatePasswordInputs(strings.TrimSpace(payload.Username), payload.Password, payload.ConfirmPassword)
	if validateErr != nil {
		writeError(w, http.StatusBadRequest, "config_error", validateErr.Error(), "", 0, 0)
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(payload.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", fmt.Sprintf("hash password: %v", err), "", 0, 0)
		return
	}

	setErr := s.store.SetSetting(r.Context(), adminUsernameKey, strings.TrimSpace(payload.Username))
	if setErr != nil {
		writeError(w, http.StatusInternalServerError, "config_error", setErr.Error(), "", 0, 0)
		return
	}
	setErr = s.store.SetSetting(r.Context(), adminPasswordHashKey, string(passwordHash))
	if setErr != nil {
		writeError(w, http.StatusInternalServerError, "config_error", setErr.Error(), "", 0, 0)
		return
	}
	setErr = s.store.SetSetting(r.Context(), setupCompleteKey, "true")
	if setErr != nil {
		writeError(w, http.StatusInternalServerError, "config_error", setErr.Error(), "", 0, 0)
		return
	}

	token, expiresAt, err := s.createSession(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	setSessionCookie(w, token, expiresAt)

	writeJSON(w, http.StatusOK, map[string]any{
		"setup_complete": true,
		"authenticated":  true,
		"session_token":  token,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}

	setupComplete, err := s.setupComplete(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	if !setupComplete {
		writeError(w, http.StatusConflict, "setup_required", "setup must be completed before login", "", 0, 0)
		return
	}

	var payload loginRequest
	decodeErr := decodeRequestBody(r, &payload)
	if decodeErr != nil {
		writeError(w, http.StatusBadRequest, "config_error", decodeErr.Error(), "", 0, 0)
		return
	}

	username, _, err := s.store.GetSetting(r.Context(), adminUsernameKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	passwordHash, _, err := s.store.GetSetting(r.Context(), adminPasswordHashKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	if strings.TrimSpace(payload.Username) != username || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(payload.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "auth_required", "invalid username or password", "", 0, 0)
		return
	}

	token, expiresAt, err := s.createSession(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	setSessionCookie(w, token, expiresAt)

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w)
		return
	}

	if err := s.clearSession(r.Context(), w); err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeMethodNotAllowed(w)
		return
	}

	var payload passwordChangeRequest
	if err := decodeRequestBody(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "config_error", err.Error(), "", 0, 0)
		return
	}
	if err := validatePasswordInputs("placeholder", payload.NewPassword, payload.ConfirmPassword); err != nil {
		if strings.Contains(err.Error(), "username") {
			writeError(w, http.StatusBadRequest, "config_error", "new password must not be empty", "", 0, 0)
			return
		}
		writeError(w, http.StatusBadRequest, "config_error", err.Error(), "", 0, 0)
		return
	}

	currentHash, _, err := s.store.GetSetting(r.Context(), adminPasswordHashKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(payload.CurrentPassword)) != nil {
		writeError(w, http.StatusUnauthorized, "auth_required", "current password is incorrect", "", 0, 0)
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(payload.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", fmt.Sprintf("hash password: %v", err), "", 0, 0)
		return
	}
	if err := s.store.SetSetting(r.Context(), adminPasswordHashKey, string(newHash)); err != nil {
		writeError(w, http.StatusInternalServerError, "config_error", err.Error(), "", 0, 0)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) clearSession(ctx context.Context, w http.ResponseWriter) error {
	if err := s.store.DeleteSetting(ctx, sessionTokenKey); err != nil {
		return err
	}
	if err := s.store.DeleteSetting(ctx, sessionExpiresAtKey); err != nil {
		return err
	}
	clearSessionCookie(w)
	return nil
}

func (s *Server) createSession(ctx context.Context) (string, time.Time, error) {
	token, err := generateSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(sessionTTL)
	if err := s.store.SetSetting(ctx, sessionTokenKey, token); err != nil {
		return "", time.Time{}, err
	}
	if err := s.store.SetSetting(ctx, sessionExpiresAtKey, expiresAt.Format(time.RFC3339Nano)); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func decodeRequestBody(r *http.Request, target any) error {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		return fmt.Errorf("decode request body: %w", err)
	}
	return nil
}

func validatePasswordInputs(username string, password string, confirmPassword string) error {
	if strings.TrimSpace(username) == "" {
		return fmt.Errorf("username must not be empty")
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("password must not be empty")
	}
	if password != confirmPassword {
		return fmt.Errorf("password confirmation does not match")
	}
	return nil
}

func generateSessionToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func (s *Server) isAuthenticated(ctx context.Context, r *http.Request) (bool, error) {
	if s.store == nil {
		return false, nil
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err == http.ErrNoCookie {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read session cookie: %w", err)
	}

	storedToken, found, err := s.store.GetSetting(ctx, sessionTokenKey)
	if err != nil || !found {
		return false, err
	}
	if cookie.Value != storedToken || strings.TrimSpace(cookie.Value) == "" {
		return false, nil
	}

	expiresRaw, found, err := s.store.GetSetting(ctx, sessionExpiresAtKey)
	if err != nil || !found {
		return false, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresRaw)
	if err != nil {
		return false, fmt.Errorf("parse session expiry: %w", err)
	}
	if !expiresAt.After(time.Now().UTC()) {
		return false, s.clearExpiredSession(ctx)
	}
	return true, nil
}

func (s *Server) clearExpiredSession(ctx context.Context) error {
	if err := s.store.DeleteSetting(ctx, sessionTokenKey); err != nil {
		return err
	}
	if err := s.store.DeleteSetting(ctx, sessionExpiresAtKey); err != nil {
		return err
	}
	return nil
}

func (s *Server) setupComplete(ctx context.Context) (bool, error) {
	value, found, err := s.store.GetSetting(ctx, setupCompleteKey)
	if err != nil || !found {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(value), "true"), nil
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		HttpOnly: true,
		MaxAge:   -1,
		Path:     "/",
		SameSite: http.SameSiteStrictMode,
	})
}

func setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		HttpOnly: true,
		MaxAge:   86400,
		Path:     "/",
		Expires:  expiresAt.UTC(),
		SameSite: http.SameSiteStrictMode,
	})
}
