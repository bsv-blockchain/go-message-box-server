package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handles"
	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// LookupConfig switches on the paymail profile lookup routes.
type LookupConfig struct {
	Domain    string        // paymail domain served, lowercase
	Host      string        // public base URL, no trailing slash
	Cooldown  time.Duration // before another key may claim a released handle
	AdminKeys []string      // identity keys allowed to release on a user's behalf
}

const (
	searchLimit      = 10
	searchMinLength  = 2
	substringMinimum = 3
	maxQueryLength   = 32
)

var (
	pubkeyRE = regexp.MustCompile(`^0[23][0-9a-f]{64}$`)
	queryRE  = regexp.MustCompile(`^[a-z0-9._-]+$`)
)

// AvailabilityResponse is the body of GET /api/handle/available/{handle}.
type AvailabilityResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // taken | too_similar | reserved | invalid | cooldown | stale
}

// EnableLookup turns the lookup feature on. The handle registry is passed
// separately because it is not part of storage.Store: only the Mongo backend
// implements it.
func (s *Server) EnableLookup(c LookupConfig, hs storage.HandleStore) {
	s.lookup, s.handles = &c, hs
}

// clock is the current time at the precision every backend stores.
func (s *Server) clock() time.Time {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	return now().UTC().Truncate(time.Millisecond)
}

// LookupRoutes returns the unauthenticated lookup routes. EnableLookup must
// have been called: every handler below reads the config and the registry, so
// wiring the routes without them would panic on the first public request
// instead of at startup.
func (s *Server) LookupRoutes() *http.ServeMux {
	if s.lookup == nil || s.handles == nil {
		panic("handlers: LookupRoutes called before EnableLookup")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/bsvalias", s.WellKnown)
	mux.HandleFunc("PUT /api/handle", s.PutHandle)
	mux.HandleFunc("GET /api/handle/available/{handle}", s.HandleAvailable)
	mux.HandleFunc("GET /api/handle/available", availableWithoutHandle)
	mux.HandleFunc("GET /api/handle/available/{$}", availableWithoutHandle)
	mux.HandleFunc("GET /api/handle/{query}", s.SearchHandles)
	mux.HandleFunc("GET /api/identityKey/{pubkey}", s.LookupIdentityKey)
	return mux
}

// availableWithoutHandle answers /api/handle/available with the handle left
// off. Both patterns are more specific than /api/handle/{query}, so without
// them the mistake is served as a search for the literal handle "available" —
// reserved, so never registered, so always an empty array and nothing to
// diagnose.
func availableWithoutHandle(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusBadRequest, "ERR_INVALID_LOOKUP", "Expected /api/handle/available/{handle}.")
}

// WellKnown serves the paymail capability document.
// @Summary Paymail capability discovery
// @Tags lookup
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /.well-known/bsvalias [get]
func (s *Server) WellKnown(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"bsvalias": "1.0",
		"capabilities": map[string]string{
			handles.BRFCLookup:        s.lookup.Host + "/api/handle/{query}",
			handles.BRFCReverseLookup: s.lookup.Host + "/api/identityKey/{pubkey}",
		},
	})
}

// writeCertificates writes stored certificate JSON verbatim. No matches is an
// empty array, never null: make gives a non-nil slice.
func writeCertificates(w http.ResponseWriter, certs []string) {
	raw := make([]json.RawMessage, len(certs))
	for i, c := range certs {
		raw[i] = json.RawMessage(c)
	}
	writeJSON(w, http.StatusOK, raw)
}

// claimErrors maps the registry's conflicts onto the wire. Every one of them is
// a 409: the request was well formed and the registry refused it.
var claimErrors = []struct {
	err  error
	code string
	text string
}{
	{storage.ErrHandleTaken, "ERR_HANDLE_TAKEN", "That handle belongs to another identity key."},
	{storage.ErrHandleTooSimilar, "ERR_HANDLE_TOO_SIMILAR", "That handle looks too much like an existing one."},
	{storage.ErrKeyHasHandle, "ERR_KEY_HAS_HANDLE", "This identity key already has a handle; release it first."},
	{storage.ErrHandleCooldown, "ERR_HANDLE_COOLDOWN", "That handle was released recently and is not yet available."},
	{storage.ErrStaleCertificate, "ERR_STALE_CERTIFICATE", "issuedAt and serialNumber must be newer than the stored certificate."},
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrHandleNotFound) {
		writeError(w, http.StatusNotFound, "ERR_HANDLE_NOT_FOUND", "No active handle.")
		return
	}
	for _, e := range claimErrors {
		if errors.Is(err, e.err) {
			writeError(w, http.StatusConflict, e.code, e.text)
			return
		}
	}
	logger.Error("handle store failure", "error", err)
	writeError(w, http.StatusInternalServerError, "ERR_INTERNAL", "Storage failure; safe to retry.")
}

// PutHandle registers, updates or releases a handle. The certificate is the
// authorisation: it is signed by the identity key and names the handle.
// @Summary Register, update or release a handle
// @Tags lookup
// @Accept json
// @Produce json
// @Param certificate body map[string]interface{} true "Self-signed profile certificate"
// @Success 200 {object} map[string]interface{}
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /api/handle [put]
func (s *Server) PutHandle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, profilecert.MaxBody))
	if err != nil {
		// Only the cap is a 413; a body that stops arriving part-way through
		// is a request the client failed to make.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE", "Certificate larger than 16 KB.")
			return
		}
		writeError(w, http.StatusBadRequest, "ERR_INVALID_CERTIFICATE", "Could not read the request body.")
		return
	}
	now := s.clock()
	p, err := profilecert.Parse(r.Context(), body, s.lookup.Domain, now)
	if errors.Is(err, profilecert.ErrWrongDomain) {
		writeError(w, http.StatusBadRequest, "ERR_WRONG_DOMAIN", "paymail field must end with @"+s.lookup.Domain)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_CERTIFICATE", err.Error())
		return
	}
	if p.Released {
		until := now.Add(s.lookup.Cooldown)
		err := s.handles.ReleaseHandle(r.Context(), storage.HandleRelease{
			Handle: p.Handle, Owner: &p.IdentityKey, IssuedAt: &p.IssuedAt,
			ReleasedBy: storage.ReleasedByOwner, CooldownUntil: &until, Now: now,
		})
		// The registry reports "you are not the owner" as ErrHandleTaken, which
		// on the claim path reads as "someone beat you to it". Same status and
		// code (the wire contract has no second one), different sentence.
		if errors.Is(err, storage.ErrHandleTaken) {
			writeError(w, http.StatusConflict, "ERR_HANDLE_TAKEN", "That handle belongs to a different identity key; only its owner can release it.")
			return
		}
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, json.RawMessage(p.JSON))
		return
	}

	// Validate is a claim-time gate: it must not block giving back a handle
	// that was valid when claimed but would fail the check today (e.g. the
	// reserved list grew since).
	switch err := handles.Validate(p.Handle); {
	case errors.Is(err, handles.ErrReservedHandle):
		writeError(w, http.StatusConflict, "ERR_HANDLE_RESERVED", "That handle is reserved.")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, "ERR_INVALID_HANDLE", "Handles are 3-32 characters of a-z 0-9 . _ - and start and end with a letter or digit.")
		return
	}

	res, err := s.handles.ClaimHandle(r.Context(), storage.HandleClaim{
		Handle: p.Handle, Skeleton: handles.Skeleton(p.Handle), IdentityKey: p.IdentityKey,
		Certificate: p.JSON, SerialNumber: p.SerialNumber, IssuedAt: p.IssuedAt, Now: now,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	status := http.StatusOK
	if res == storage.ClaimCreated {
		status = http.StatusCreated
	}
	writeJSON(w, status, json.RawMessage(p.JSON))
}

// normaliseQuery lowercases q and strips an @domain suffix. ok is false when
// the query can match nothing on this server.
func (s *Server) normaliseQuery(q string) (string, bool) {
	q = strings.ToLower(strings.TrimSpace(q))
	if local, dom, found := strings.Cut(q, "@"); found {
		// A partially typed domain is fine; a different one is not.
		if !strings.HasPrefix(s.lookup.Domain, dom) {
			return "", false
		}
		q = local
	}
	if len(q) < searchMinLength || len(q) > maxQueryLength || !queryRE.MatchString(q) {
		return "", false
	}
	return q, true
}

// searchTiers lists the matches to try, best first.
func searchTiers(q string) []storage.HandleMatch {
	tiers := []storage.HandleMatch{{Field: storage.HandleFieldHandle, Mode: storage.HandleMatchPrefix, Value: q}}
	if sk := handles.Skeleton(q); len(sk) >= searchMinLength {
		tiers = append(tiers, storage.HandleMatch{Field: storage.HandleFieldSkeleton, Mode: storage.HandleMatchPrefix, Value: sk})
	}
	if len(q) >= substringMinimum {
		tiers = append(tiers, storage.HandleMatch{Field: storage.HandleFieldHandle, Mode: storage.HandleMatchContains, Value: q})
	}
	return tiers
}

func (s *Server) search(ctx context.Context, q string) ([]string, error) {
	certs := []string{}
	seen := map[string]bool{}
	add := func(r storage.HandleRecord) {
		if !seen[r.Handle] && r.Certificate != nil && len(certs) < searchLimit {
			seen[r.Handle] = true
			certs = append(certs, *r.Certificate)
		}
	}
	// The exact handle leads even when ten other handles share the prefix.
	exact, err := s.handles.GetHandle(ctx, q)
	if err != nil {
		return nil, err
	}
	if exact.Active() {
		add(*exact)
	}
	for _, m := range searchTiers(q) {
		if len(certs) >= searchLimit {
			break
		}
		m.Limit = searchLimit
		recs, err := s.handles.FindHandles(ctx, m)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			add(r)
		}
	}
	return certs, nil
}

// SearchHandles returns profile certificates whose handle matches the query.
// @Summary Search handles (capability 0ace65da5987)
// @Tags lookup
// @Produce json
// @Param query path string true "Partial or full handle, optionally @domain"
// @Success 200 {array} map[string]interface{}
// @Router /api/handle/{query} [get]
func (s *Server) SearchHandles(w http.ResponseWriter, r *http.Request) {
	q, ok := s.normaliseQuery(r.PathValue("query"))
	if !ok {
		writeCertificates(w, nil)
		return
	}
	certs, err := s.search(r.Context(), q)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeCertificates(w, certs)
}

// LookupIdentityKey returns the profile certificate of an identity key.
// @Summary Reverse lookup (capability 43dcf83ddc5f)
// @Tags lookup
// @Produce json
// @Param pubkey path string true "Compressed public key, hex"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} ErrorResponse
// @Router /api/identityKey/{pubkey} [get]
func (s *Server) LookupIdentityKey(w http.ResponseWriter, r *http.Request) {
	key := strings.ToLower(r.PathValue("pubkey"))
	if !pubkeyRE.MatchString(key) {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_LOOKUP", "Expected a 33-byte compressed public key in hex.")
		return
	}
	rec, err := s.handles.GetHandleByIdentityKey(r.Context(), key)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if rec == nil || rec.Certificate == nil {
		s.writeStoreError(w, storage.ErrHandleNotFound)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(*rec.Certificate))
}

// HandleAvailable is an advisory pre-check; PutHandle is the source of truth.
// @Summary Check whether a handle can be registered
// @Tags lookup
// @Produce json
// @Param handle path string true "Handle"
// @Success 200 {object} AvailabilityResponse
// @Router /api/handle/available/{handle} [get]
func (s *Server) HandleAvailable(w http.ResponseWriter, r *http.Request) {
	handle := strings.ToLower(r.PathValue("handle"))
	reason, err := s.unavailableReason(r.Context(), handle)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, AvailabilityResponse{Available: reason == "", Reason: reason})
}

// unavailableReason names the rule that would refuse handle, or "" when nothing
// stored refuses it. The registry decides, so the answer is advisory: it may be
// stale by the time a claim arrives.
func (s *Server) unavailableReason(ctx context.Context, handle string) (string, error) {
	switch err := handles.Validate(handle); {
	case errors.Is(err, handles.ErrReservedHandle):
		return "reserved", nil
	case err != nil:
		return "invalid", nil
	}
	rec, err := s.handles.GetHandleBySkeleton(ctx, handles.Skeleton(handle))
	if err != nil || rec == nil {
		return "", err
	}
	now := s.clock()
	switch {
	case rec.Handle != handle:
		return "too_similar", nil
	case rec.Active():
		return "taken", nil
	case rec.CooldownUntil != nil && rec.CooldownUntil.After(now):
		return "cooldown", nil
	// A claim must beat the released row's issuedAt, so while that value is not
	// yet in the past a certificate dated now cannot take the handle. Saying it
	// is free would be advice no client can act on until the clock catches up.
	case !rec.IssuedAt.Before(now):
		return "stale", nil
	}
	return "", nil
}
