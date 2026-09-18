package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// AdminReleaseRequest is the body of POST /admin/handle/release.
type AdminReleaseRequest struct {
	Handle string `json:"handle"`
	// SkipCooldown defaults to true: the usual case is a user who lost their
	// key and, after out-of-band verification, re-registers with a new one.
	// Only an active handle can be released; a handle already tombstoned by its
	// owner answers 404, and its cooldown cannot be lifted from here (the
	// previous owner may reclaim it inside the cooldown regardless).
	SkipCooldown *bool `json:"skipCooldown,omitempty"`
}

// AdminReleaseHandle releases a handle on the operator's authority.
// @Summary Operator release of a handle (lost keys)
// @Tags lookup
// @Accept json
// @Produce json
// @Param request body AdminReleaseRequest true "Handle to release"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BSVAuth
// @Router /admin/handle/release [post]
func (s *Server) AdminReleaseHandle(w http.ResponseWriter, r *http.Request) {
	s.adminReleaseHandle(w, r, getIdentityKey(r))
}

// adminReleaseHandle is the testable core: the auth middleware's identity
// cannot be faked in tests, so caller is passed in directly.
func (s *Server) adminReleaseHandle(w http.ResponseWriter, r *http.Request, caller string) {
	if caller == "" {
		writeError(w, http.StatusUnauthorized, "ERR_AUTH_REQUIRED", "Authentication required.")
		return
	}
	// Canonicalised once: the allowlist is lowercase (config normalises it), and
	// the same spelling is what lands in the row's releasedBy audit field.
	caller = strings.ToLower(caller)
	if s.lookup == nil || s.handles == nil || !slices.Contains(s.lookup.AdminKeys, caller) {
		// A key reaching for a handle it may not have is the audit trail's most
		// interesting event; the body is not decoded yet, so log the caller alone.
		slog.Warn("handle release refused", "caller", caller)
		writeError(w, http.StatusForbidden, "ERR_NOT_ADMIN", "This identity key may not release handles.")
		return
	}
	var req AdminReleaseRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Handle == "" {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_REQUEST", "Expected {\"handle\": \"...\"}.")
		return
	}
	handle := strings.ToLower(strings.TrimSpace(req.Handle))

	prev, err := s.handles.GetHandle(r.Context(), handle)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	now := s.clock()
	rel := storage.HandleRelease{Handle: handle, ReleasedBy: caller, Now: now}
	if req.SkipCooldown != nil && !*req.SkipCooldown {
		until := now.Add(s.lookup.Cooldown)
		rel.CooldownUntil = &until
	}
	if err := s.handles.ReleaseHandle(r.Context(), rel); err != nil {
		s.writeStoreError(w, err)
		return
	}

	previousKey := ""
	if prev != nil && prev.IdentityKey != nil {
		previousKey = *prev.IdentityKey
	}
	// Always logged, not only in development: this is the audit trail.
	slog.Warn("admin released handle", "handle", handle, "admin", caller, "previousIdentityKey", previousKey, "cooldown", rel.CooldownUntil != nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success", "handle": handle})
}
