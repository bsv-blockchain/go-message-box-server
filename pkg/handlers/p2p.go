package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/handles"
	"github.com/bsv-blockchain/go-message-box-server/pkg/p2p"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// Paymail capability ids published when the P2P receive rail is on, besides the
// two lookup ones. The BRFC ids are fixed by the paymail spec, not derived here.
const (
	brfcPublicProfile = "f12f968c92d6"
	brfcP2PDest       = "2a40af698840"
	brfcP2PReceive    = "5f1323cddf31"
	brfcBEEF          = "5c55a7fdb7bb"
	brfcVerifyPubKey  = "6745385c3fc0"
)

// P2PConfig switches on the paymail receive routes.
type P2PConfig struct {
	DestinationTTL time.Duration // accept window for an issued destination
	MaxSatoshis    uint64
	AvatarURL      string
	Broadcaster    p2p.Broadcaster
	Verifier       p2p.Verifier
	Parents        p2p.ParentFetcher
}

// EnableP2P turns the paymail receive routes on. EnableLookup must also have
// been called: every route resolves the handle through the registry.
func (s *Server) EnableP2P(c P2PConfig, store storage.P2PDestinationStore) {
	s.p2p, s.destinations = &c, store
}

// P2PRoutes returns the /api/bsvalias/ routes. EnableP2P and EnableLookup must
// have been called, for the same reason LookupRoutes insists on its config.
func (s *Server) P2PRoutes() *http.ServeMux {
	if s.p2p == nil || s.destinations == nil || s.lookup == nil || s.handles == nil {
		panic("handlers: P2PRoutes called before EnableP2P and EnableLookup")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bsvalias/id/{paymail}", s.PaymailPKI)
	mux.HandleFunc("GET /api/bsvalias/public-profile/{paymail}", s.PaymailPublicProfile)
	return mux
}

// p2pCapabilities are the capability entries added to the well-known document.
func (s *Server) p2pCapabilities() map[string]any {
	base := s.lookup.Host + "/api/bsvalias/"
	const addr = "/{alias}@{domain.tld}"
	return map[string]any{
		"pki":             base + "id" + addr,
		brfcPublicProfile: base + "public-profile" + addr,
		brfcP2PDest:       base + "p2p-payment-destination" + addr,
		brfcP2PReceive:    base + "receive-transaction" + addr,
		brfcBEEF:          base + "beef" + addr,
		// The sender-validation capability is advertised as off: we sign nothing.
		brfcVerifyPubKey: false,
	}
}

// activeHandleFor resolves a paymail address to the active handle record. The
// status is 200 on success; otherwise it is the HTTP status to answer with and
// err says why: 400 for a malformed address or another domain, 404 for a
// handle that is missing or released, 500 for a storage failure.
func (s *Server) activeHandleFor(ctx context.Context, paymail string) (*storage.HandleRecord, int, error) {
	paymail = strings.ToLower(strings.TrimSpace(paymail))
	i := strings.LastIndex(paymail, "@")
	if i < 0 {
		return nil, http.StatusBadRequest, errors.New("expected alias@domain")
	}
	alias, domain := paymail[:i], paymail[i+1:]
	if domain != s.lookup.Domain {
		return nil, http.StatusBadRequest, errors.New("this server does not serve " + domain)
	}
	// Reserved handles are well formed and never registered: a 404 like any
	// other unknown handle, not a 400.
	if err := handles.Validate(alias); err != nil && !errors.Is(err, handles.ErrReservedHandle) {
		return nil, http.StatusBadRequest, errors.New("not a valid handle")
	}
	rec, err := s.handles.GetHandle(ctx, alias)
	if err != nil {
		logger.Error("handle lookup failed", "error", err)
		return nil, http.StatusInternalServerError, errors.New("storage failure; safe to retry")
	}
	if !rec.Active() {
		return nil, http.StatusNotFound, errors.New("no active handle")
	}
	return rec, http.StatusOK, nil
}

// writePaymailError answers with the status activeHandleFor chose.
func writePaymailError(w http.ResponseWriter, status int, err error) {
	code := "ERR_HANDLE_NOT_FOUND"
	switch status {
	case http.StatusBadRequest:
		code = "ERR_INVALID_PAYMAIL"
	case http.StatusInternalServerError:
		code = "ERR_INTERNAL"
	}
	writeError(w, status, code, err.Error())
}

// PaymailPKI answers the paymail PKI capability. Like the reference bridge it
// returns the anyone key, which is the sender key in every BRC-29 derivation;
// the recipient's identity key stays out of the paymail routes.
// @Summary Paymail PKI (capability pki)
// @Tags paymail
// @Produce json
// @Param paymail path string true "alias@domain"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/bsvalias/id/{paymail} [get]
func (s *Server) PaymailPKI(w http.ResponseWriter, r *http.Request) {
	rec, status, err := s.activeHandleFor(r.Context(), r.PathValue("paymail"))
	if err != nil {
		writePaymailError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"bsvalias": "1.0",
		"handle":   rec.Handle + "@" + s.lookup.Domain,
		"pubkey":   p2p.AnyonePublicKeyHex(),
	})
}

// PaymailPublicProfile answers the paymail public profile capability.
// @Summary Paymail public profile (capability f12f968c92d6)
// @Tags paymail
// @Produce json
// @Param paymail path string true "alias@domain"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/bsvalias/public-profile/{paymail} [get]
func (s *Server) PaymailPublicProfile(w http.ResponseWriter, r *http.Request) {
	rec, status, err := s.activeHandleFor(r.Context(), r.PathValue("paymail"))
	if err != nil {
		writePaymailError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": rec.Handle, "avatar": s.p2p.AvatarURL})
}
