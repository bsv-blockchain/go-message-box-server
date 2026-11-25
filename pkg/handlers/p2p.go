package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"

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
	mux.HandleFunc("POST /api/bsvalias/p2p-payment-destination/{paymail}", s.PaymailP2PDestination)
	mux.HandleFunc("POST /api/bsvalias/beef/{paymail}", s.PaymailBEEF)
	mux.HandleFunc("POST /api/bsvalias/receive-transaction/{paymail}", s.PaymailReceiveTransaction)
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

// maxDestinationBody bounds the destination request. The only field is the
// amount, so anything larger is not a paymail sender.
const maxDestinationBody = 4 << 10

// parseSatoshis reads the amount strictly: a JSON integer from 1 to limit. A
// string, a fraction, an exponent, a sign or an overflow is refused, because
// a sloppy parse here would let a sender pick an amount we did not mean to
// allow.
func parseSatoshis(raw json.RawMessage, limit uint64) (uint64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil || n == 0 || n > limit {
		return 0, false
	}
	return n, true
}

// PaymailP2PDestination answers the P2P payment destination capability. It
// derives a one-use BRC-29 output for the handle's owner and keeps the record
// so the later payment can be matched to it.
// @Summary Paymail P2P payment destination (capability 2a40af698840)
// @Tags paymail
// @Accept json
// @Produce json
// @Param paymail path string true "alias@domain"
// @Success 200 {object} map[string]any
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 413 {object} ErrorResponse
// @Router /api/bsvalias/p2p-payment-destination/{paymail} [post]
func (s *Server) PaymailP2PDestination(w http.ResponseWriter, r *http.Request) {
	rec, status, err := s.activeHandleFor(r.Context(), r.PathValue("paymail"))
	if err != nil {
		writePaymailError(w, status, err)
		return
	}
	var body struct {
		Satoshis json.RawMessage `json:"satoshis"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDestinationBody))
	if err = dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE", "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "ERR_INVALID_BODY", "body must be JSON")
		return
	}
	sats, ok := parseSatoshis(body.Satoshis, s.p2p.MaxSatoshis)
	if !ok {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_SATOSHIS", "satoshis must be a positive integer within the limit")
		return
	}
	// activeHandleFor only returns active records, which carry a key. Copy it
	// now: the stored destination keeps this key even if the handle is released.
	identityKey := *rec.IdentityKey
	d, err := p2p.Derive(identityKey, rand.Reader)
	if err != nil {
		logger.Error("p2p derive failed", "error", err)
		writeError(w, http.StatusInternalServerError, "ERR_INTERNAL", "could not derive a destination; safe to retry")
		return
	}
	ref := make([]byte, 16)
	if _, err := rand.Read(ref); err != nil {
		logger.Error("p2p reference failed", "error", err)
		writeError(w, http.StatusInternalServerError, "ERR_INTERNAL", "could not issue a reference; safe to retry")
		return
	}
	reference := hex.EncodeToString(ref)
	now := s.clock()
	if err := s.destinations.CreateDestination(r.Context(), storage.P2PDestination{
		Reference: reference, Handle: rec.Handle, IdentityKey: identityKey,
		Script: d.Script, Satoshis: sats,
		DerivationPrefix: d.Prefix, DerivationSuffix: d.Suffix,
		CreatedAt: now, AcceptUntil: now.Add(s.p2p.DestinationTTL),
		// There is deliberately no per-handle cap on open destinations: issuing
		// is unauthenticated and Traefik hides client IPs, so a cap would let
		// anyone deny payments to a handle with a few anonymous POSTs. Storage
		// growth is bounded by the P2P route rate limiter (P2P_RATE_PER_MIN):
		// 120/min for 7 days is about 1.2M records.
		PurgeAt: now.Add(storage.P2PUnpaidRetention),
	}); err != nil {
		logger.Error("p2p destination store failed", "error", err)
		writeError(w, http.StatusInternalServerError, "ERR_INTERNAL", "storage failure; safe to retry")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"outputs":   []map[string]any{{"script": d.Script, "satoshis": sats}},
		"reference": reference,
	})
}

// maxBeefBody bounds a BEEF submission. A payment with a deep unconfirmed
// ancestry is a few hundred KB; this leaves room and still refuses abuse.
const maxBeefBody = 4 << 20

// Bounds on what an unauthenticated raw transaction may cost before any
// signature is checked. Each distinct parent costs at least one request on the
// shared WhatsOnChain limiter and up to 4 MiB of response, and the body limit
// alone would allow thousands of inputs. A payment spends a handful of outputs;
// a sender with more should use the BEEF route, which carries its own proofs.
const (
	maxRawParents = 10  // distinct parent txids
	maxRawInputs  = 100 // inputs in all, whatever their parents
	// One call's whole outside spend: requests and bytes across every parent
	// and every unmined ancestor, and the time they may take.
	rawFetchRequests = 30
	rawFetchBytes    = 8 << 20
	rawFetchTimeout  = 30 * time.Second
)

// p2pMetadata is the optional paymail sender metadata. Only the note and the
// sender's paymail are kept; the rest of the spec's fields are ignored.
type p2pMetadata struct {
	Note   string `json:"note"`
	Sender string `json:"sender"`
}

// aliasFor returns the handle part of a paymail address on this server's
// domain. Unlike activeHandleFor it does not look the handle up: a payment is
// bound to the destination's stored identity key, so a handle released since
// the destination was issued must not stop the payment.
func (s *Server) aliasFor(paymail string) (string, bool) {
	paymail = strings.ToLower(strings.TrimSpace(paymail))
	i := strings.LastIndex(paymail, "@")
	if i < 0 || paymail[i+1:] != s.lookup.Domain {
		return "", false
	}
	return paymail[:i], true
}

// validReference reports whether ref has the shape of an issued reference.
func validReference(ref string) bool {
	if len(ref) != 32 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// destinationFor loads and checks the destination a payment refers to, writing
// the error response itself when it cannot.
func (s *Server) destinationFor(w http.ResponseWriter, r *http.Request, paymail, reference string) *storage.P2PDestination {
	alias, ok := s.aliasFor(paymail)
	if !ok {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_PAYMAIL", "expected alias@"+s.lookup.Domain)
		return nil
	}
	// Issued references are 32 lowercase hex characters. Anything else cannot
	// exist, so it never reaches the store.
	if !validReference(reference) {
		writeError(w, http.StatusNotFound, "ERR_UNKNOWN_REFERENCE", "unknown or expired reference")
		return nil
	}
	dest, err := s.destinations.GetDestination(r.Context(), reference, s.clock())
	if errors.Is(err, storage.ErrDestinationNotFound) {
		writeError(w, http.StatusNotFound, "ERR_UNKNOWN_REFERENCE", "unknown or expired reference")
		return nil
	}
	if err != nil {
		logger.Error("p2p destination lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "ERR_INTERNAL", "storage failure; safe to retry")
		return nil
	}
	if dest.Handle != alias {
		writeError(w, http.StatusBadRequest, "ERR_REFERENCE_MISMATCH", "reference was issued for another handle")
		return nil
	}
	return dest
}

// writeSettleError answers with what settleP2P decided.
func writeSettleError(w http.ResponseWriter, status int, err error) {
	var pe *p2pError
	if errors.As(err, &pe) {
		writeError(w, status, pe.code, pe.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "ERR_INTERNAL", "internal error; safe to retry")
}

// decodeReceiveBody reads a receive route's JSON body within maxBeefBody,
// writing the error response itself when it cannot.
func decodeReceiveBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBeefBody)).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "ERR_BODY_TOO_LARGE", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "ERR_INVALID_BODY", "body must be JSON")
		return false
	}
	return true
}

// PaymailBEEF receives a payment as BEEF (capability 5c55a7fdb7bb).
// @Summary Paymail BEEF receive (capability 5c55a7fdb7bb)
// @Tags paymail
// @Accept json
// @Produce json
// @Param paymail path string true "alias@domain"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 413 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/bsvalias/beef/{paymail} [post]
func (s *Server) PaymailBEEF(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Beef      string      `json:"beef"`
		Reference string      `json:"reference"`
		Metadata  p2pMetadata `json:"metadata"`
	}
	if !decodeReceiveBody(w, r, &body) {
		return
	}
	dest := s.destinationFor(w, r, r.PathValue("paymail"), body.Reference)
	if dest == nil {
		return
	}
	raw, err := hex.DecodeString(body.Beef)
	if err != nil || len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_BEEF", "beef must be hex")
		return
	}
	beef, err := transaction.NewBeefFromBytes(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_BEEF", "beef could not be parsed")
		return
	}
	tx, perr := findPayingTx(beef, dest)
	if perr != nil {
		writeError(w, http.StatusBadRequest, perr.code, perr.Error())
		return
	}
	txid, status, err := s.settleP2P(r.Context(), dest, tx, body.Metadata)
	if err != nil {
		writeSettleError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"txid": txid, "note": ""})
}

// PaymailReceiveTransaction receives a payment as a raw transaction
// (capability 5f1323cddf31). The sender gives no proofs, so the parents are
// fetched here, which lets settleP2P verify and package the payment.
// @Summary Paymail raw transaction receive (capability 5f1323cddf31)
// @Tags paymail
// @Accept json
// @Produce json
// @Param paymail path string true "alias@domain"
// @Success 200 {object} map[string]string
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 413 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/bsvalias/receive-transaction/{paymail} [post]
func (s *Server) PaymailReceiveTransaction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hex       string      `json:"hex"`
		Reference string      `json:"reference"`
		Metadata  p2pMetadata `json:"metadata"`
	}
	if !decodeReceiveBody(w, r, &body) {
		return
	}
	dest := s.destinationFor(w, r, r.PathValue("paymail"), body.Reference)
	if dest == nil {
		return
	}
	tx, err := transaction.NewTransactionFromHex(body.Hex)
	if err != nil || len(tx.Inputs) == 0 || len(tx.Outputs) == 0 {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_TX", "hex must be a raw transaction")
		return
	}
	// Check the destination before spending any outside request on parents.
	if matchOutput(tx, dest) < 0 {
		writeError(w, http.StatusBadRequest, "ERR_NO_MATCHING_OUTPUT", "no output pays the destination script and amount")
		return
	}
	if len(tx.Inputs) > maxRawInputs {
		writeError(w, http.StatusBadRequest, "ERR_TOO_MANY_INPUTS", "transaction has too many inputs")
		return
	}
	distinct := map[string]struct{}{}
	for _, in := range tx.Inputs {
		distinct[in.SourceTXID.String()] = struct{}{}
	}
	if len(distinct) > maxRawParents {
		writeError(w, http.StatusBadRequest, "ERR_TOO_MANY_INPUTS", "transaction spends too many distinct parents")
		return
	}
	// The fetches share one budget and one deadline: this is the unauthenticated
	// part of the request, and it must not be able to hold WhatsOnChain or this
	// connection for as long as it likes.
	fctx, cancel := context.WithTimeout(r.Context(), rawFetchTimeout)
	defer cancel()
	fctx = p2p.WithBudget(fctx, p2p.NewBudget(rawFetchRequests, rawFetchBytes))
	// One fetch per distinct parent, however many of its outputs are spent.
	fetched := map[string]*transaction.Transaction{}
	for _, in := range tx.Inputs {
		id := in.SourceTXID.String()
		parent, ok := fetched[id]
		if !ok {
			if parent, err = s.p2p.Parents.Parent(fctx, id); err != nil {
				if errors.Is(err, p2p.ErrBudgetExceeded) {
					writeError(w, http.StatusBadRequest, "ERR_TOO_MANY_INPUTS", "the inputs' ancestry is too large to verify here; send BEEF instead")
					return
				}
				if errors.Is(err, p2p.ErrParentNotFound) {
					// The input does not exist, so a retry cannot help.
					writeError(w, http.StatusBadRequest, "ERR_INVALID_TX", "an input transaction does not exist")
					return
				}
				logger.Error("p2p parent fetch failed", "parent", id, "error", err)
				writeError(w, http.StatusServiceUnavailable, "ERR_PARENT_UNAVAILABLE", "could not fetch an input transaction; safe to retry")
				return
			}
			fetched[id] = parent
		}
		in.SourceTransaction = parent
	}
	txid, status, err := s.settleP2P(r.Context(), dest, tx, body.Metadata)
	if err != nil {
		writeSettleError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"txid": txid, "note": ""})
}
