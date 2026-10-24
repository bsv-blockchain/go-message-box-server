package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/handles"
	"github.com/bsv-blockchain/go-message-box-server/pkg/purchases"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// PurchaseConfig switches on paid handles: every handle that is not one the
// app generates for free needs a store subscription the store itself vouches
// for, of the tier the handle's length puts it in.
type PurchaseConfig struct {
	// Verifiers by store name (storage.StoreAppStore, storage.StoreGooglePlay).
	// A store without one is refused, so a paid handle can never be granted on
	// a store this server cannot check.
	Verifiers map[string]purchases.Verifier
	// Grace is how long after a subscription lapses its handle is kept.
	Grace time.Duration
	// GooglePackage is the Play package whose notifications are processed.
	GooglePackage string
	// PushAuth authenticates Pub/Sub pushes; nil accepts any. A forged push can
	// only make this server ask Google again, so it is hardening, not trust.
	PushAuth PushAuthenticator
}

// PushAuthenticator checks a Pub/Sub push request's OIDC token.
type PushAuthenticator interface {
	Authenticate(r *http.Request) error
}

// ReleasedBySubscription is the ReleasedBy value of a handle released because
// its subscription lapsed.
const ReleasedBySubscription = "subscription-lapsed"

const (
	// maxPutBody bounds PUT /api/handle with a purchase: a StoreKit 2 signed
	// transaction is several kilobytes on top of the certificate.
	maxPutBody = 64 << 10
	// maxNotificationBody bounds a store notification.
	maxNotificationBody = 1 << 20
	// sweepBatch is how many lapsed subscriptions one sweep looks at.
	sweepBatch = 100
)

// PutHandleRequest is the body of PUT /api/handle when a purchase comes with
// the certificate. A bare certificate is still accepted, for free handles and
// for updates to a handle the key already holds.
type PutHandleRequest struct {
	Certificate json.RawMessage     `json:"certificate" swaggertype:"object"`
	Purchase    *purchases.Purchase `json:"purchase,omitempty"`
}

// EnablePurchases turns paid handles on. Like EnableLookup it takes the store
// separately because only the Mongo backend implements it.
func (s *Server) EnablePurchases(c PurchaseConfig, subs storage.SubscriptionStore) {
	s.purchase, s.subs = &c, subs
}

// PurchaseRoutes returns the store notification routes. They are public — the
// stores call them — but are not rate limited per client address like the
// lookup routes: Pub/Sub pushes from a handful of Google addresses in bursts,
// and every notification only makes this server ask the store again.
// EnablePurchases must have been called.
func (s *Server) PurchaseRoutes() *http.ServeMux {
	if s.purchase == nil || s.subs == nil || s.handles == nil {
		panic("handlers: PurchaseRoutes called before EnableLookup and EnablePurchases")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/store/apple/notifications", s.AppleNotification)
	mux.HandleFunc("POST /api/store/google/notifications", s.GoogleNotification)
	return mux
}

// splitPutBody separates the certificate from a purchase sent with it. A body
// whose top-level object has a "certificate" member is the envelope; anything
// else is the certificate itself.
func splitPutBody(body []byte) ([]byte, *purchases.Purchase) {
	var env PutHandleRequest
	if json.Unmarshal(body, &env) == nil && len(env.Certificate) > 0 && string(env.Certificate) != "null" {
		return env.Certificate, env.Purchase
	}
	return body, nil
}

// errPurchaseUsed says the subscription already pays for a handle somebody
// holds.
var errPurchaseUsed = errors.New("purchase already pays for another handle")

// requirePurchase decides whether claiming handle for identityKey needs a
// purchase and, if so, verifies and binds it. It writes the error response and
// returns false when the claim must not go ahead.
func (s *Server) requirePurchase(w http.ResponseWriter, r *http.Request, handle, identityKey string, p *purchases.Purchase, now time.Time) bool {
	if s.purchase == nil || handles.IsGenerated(handle) {
		return true
	}
	// The key already holds the handle, so this is a certificate update: the
	// subscription on file pays for it and the store's notifications govern it.
	rec, err := s.handles.GetHandle(r.Context(), handle)
	if err != nil {
		s.writeStoreError(w, err)
		return false
	}
	if rec.Active() && *rec.IdentityKey == identityKey {
		return true
	}
	if p == nil {
		writeError(w, http.StatusPaymentRequired, "ERR_PURCHASE_REQUIRED", "This handle needs a subscription; send it as {certificate, purchase}.")
		return false
	}
	verifier := s.purchase.Verifiers[p.Store]
	if verifier == nil {
		writeError(w, http.StatusBadRequest, "ERR_PURCHASE_INVALID", "Purchases from "+p.Store+" are not accepted here.")
		return false
	}
	v, err := verifier.Verify(r.Context(), *p)
	switch {
	case errors.Is(err, purchases.ErrInvalid):
		writeError(w, http.StatusBadRequest, "ERR_PURCHASE_INVALID", err.Error())
		return false
	case err != nil:
		slog.Warn("store verification failed", "store", p.Store, "error", err)
		writeError(w, http.StatusBadGateway, "ERR_STORE_UNAVAILABLE", "The store could not confirm the purchase; safe to retry.")
		return false
	}
	if v.Tier != handles.Tier(handle) {
		writeError(w, http.StatusConflict, "ERR_PURCHASE_TIER_MISMATCH", "That subscription is for a different handle length.")
		return false
	}
	if !purchases.Entitled(v.SubscriptionStatus, now) {
		writeError(w, http.StatusPaymentRequired, "ERR_PURCHASE_INACTIVE", "That subscription is not active.")
		return false
	}
	switch err := s.bindSubscription(r.Context(), p.Store, v, handle, identityKey, now); {
	case errors.Is(err, errPurchaseUsed):
		writeError(w, http.StatusConflict, "ERR_PURCHASE_USED", "That subscription already pays for another handle.")
		return false
	case err != nil:
		s.writeStoreError(w, err)
		return false
	}
	return true
}

// bindSubscription points a verified subscription at handle. A subscription
// pays for one handle at a time: it may move to another only once the handle
// it was bound to is no longer held by the key it was bound for — released by
// its owner (a handle change), by an operator or by a lapse.
//
// It binds before the claim is written. If the claim then fails the row points
// at a handle its key does not hold, which is exactly the state that lets the
// next attempt take it, so nothing needs undoing.
func (s *Server) bindSubscription(ctx context.Context, store string, v *purchases.Verified, handle, identityKey string, now time.Time) error {
	sub := storage.Subscription{
		ID: storage.SubscriptionID(store, v.StoreID), Store: store, StoreID: v.StoreID,
		Handle: handle, IdentityKey: identityKey, SubscriptionStatus: v.SubscriptionStatus,
		CreatedAt: now, UpdatedAt: now,
	}
	for range 3 {
		cur, err := s.subs.GetSubscription(ctx, sub.ID)
		if err != nil {
			return err
		}
		if cur != nil && (cur.Handle != handle || cur.IdentityKey != identityKey) {
			held, err := s.handles.GetHandle(ctx, cur.Handle)
			if err != nil {
				return err
			}
			if held.Active() && *held.IdentityKey == cur.IdentityKey {
				return errPurchaseUsed
			}
		}
		err = s.subs.BindSubscription(ctx, sub, cur)
		if !errors.Is(err, storage.ErrSubscriptionRaced) {
			return err
		}
	}
	return storage.ErrSubscriptionRaced
}

// applyStatus records what a store now says about sub. A renewal into a tier
// that does not fit the handle (a crossgrade) does not extend it: the handle
// stays paid only until the last period that did fit.
func (s *Server) applyStatus(ctx context.Context, sub *storage.Subscription, v *purchases.Verified, now time.Time) error {
	st := v.SubscriptionStatus
	if st.Tier != handles.Tier(sub.Handle) {
		st.State = storage.SubWrongTier
		if st.ExpiresAt.After(sub.ExpiresAt) {
			st.ExpiresAt = sub.ExpiresAt
		}
	}
	if err := s.subs.UpdateSubscriptionStatus(ctx, sub.ID, st, now); err != nil {
		return err
	}
	sub.SubscriptionStatus, sub.UpdatedAt = st, now
	return nil
}

// refresh asks the store about sub again and records the answer. The store's
// word is the only one taken: notifications only say whom to ask.
func (s *Server) refresh(ctx context.Context, sub *storage.Subscription) error {
	verifier := s.purchase.Verifiers[sub.Store]
	if verifier == nil {
		return purchases.ErrUnavailable
	}
	v, err := verifier.Refresh(ctx, *sub)
	if err != nil {
		return err
	}
	return s.applyStatus(ctx, sub, v, s.clock())
}

// writeRefreshError answers a store notification whose refresh failed. A store
// outage is a 503 so the notification is redelivered; a subscription the store
// no longer knows is acknowledged, since redelivery cannot change that.
func writeRefreshError(w http.ResponseWriter, store, id string, err error) {
	if errors.Is(err, purchases.ErrInvalid) {
		slog.Warn("store notification for a subscription the store rejects", "store", store, "id", id, "error", err)
		w.WriteHeader(http.StatusOK)
		return
	}
	slog.Error("store notification refresh failed", "store", store, "id", id, "error", err)
	writeError(w, http.StatusServiceUnavailable, "ERR_STORE_UNAVAILABLE", "Could not refresh the subscription; retry.")
}

// AppleNotification receives App Store Server Notifications V2.
// @Summary App Store Server Notifications V2
// @Description Apple posts {signedPayload}. The subscription it names is looked up again with the App Store Server API; nothing in the payload is trusted.
// @Tags purchases
// @Accept json
// @Success 200
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/store/apple/notifications [post]
func (s *Server) AppleNotification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SignedPayload string `json:"signedPayload"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody)).Decode(&body); err != nil || body.SignedPayload == "" {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_NOTIFICATION", "Expected {\"signedPayload\": \"...\"}.")
		return
	}
	id, err := purchases.OriginalTransactionID(body.SignedPayload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_NOTIFICATION", "signedPayload is not a notification.")
		return
	}
	if id == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	s.refreshByID(w, r, storage.StoreAppStore, id)
}

// refreshByID refreshes the subscription a notification names.
func (s *Server) refreshByID(w http.ResponseWriter, r *http.Request, store, storeID string) {
	ctx := r.Context()
	sub, err := s.subs.GetSubscription(ctx, storage.SubscriptionID(store, storeID))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if sub == nil {
		// Not bound to any handle (yet): the claim that binds it reads the
		// store itself, so there is nothing to record.
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := s.refresh(ctx, sub); err != nil {
		writeRefreshError(w, store, storeID, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// pubsubPush is the body Pub/Sub POSTs to a push endpoint.
type pubsubPush struct {
	Message struct {
		Data      string `json:"data"`
		MessageID string `json:"messageId"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

// developerNotification is a Play real-time developer notification.
type developerNotification struct {
	PackageName              string `json:"packageName"`
	SubscriptionNotification *struct {
		NotificationType int    `json:"notificationType"`
		PurchaseToken    string `json:"purchaseToken"`
	} `json:"subscriptionNotification"`
}

// GoogleNotification receives Play real-time developer notifications through
// a Pub/Sub push subscription.
// @Summary Google Play real-time developer notifications (Pub/Sub push)
// @Description The purchase token it names is looked up again with purchases.subscriptionsv2.get; nothing in the message is trusted.
// @Tags purchases
// @Accept json
// @Success 200
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/store/google/notifications [post]
func (s *Server) GoogleNotification(w http.ResponseWriter, r *http.Request) {
	if s.purchase.PushAuth != nil {
		if err := s.purchase.PushAuth.Authenticate(r); err != nil {
			slog.Warn("pub/sub push refused", "error", err)
			writeError(w, http.StatusUnauthorized, "ERR_UNAUTHORIZED", "Push token rejected.")
			return
		}
	}
	var push pubsubPush
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody)).Decode(&push); err != nil {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_NOTIFICATION", "Expected a Pub/Sub push message.")
		return
	}
	data, err := base64.StdEncoding.DecodeString(push.Message.Data)
	var n developerNotification
	if err == nil {
		err = json.Unmarshal(data, &n)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "ERR_INVALID_NOTIFICATION", "message.data is not a developer notification.")
		return
	}
	// Test notifications, one-time products and other apps: acknowledged so
	// Pub/Sub stops redelivering them, and otherwise ignored.
	if n.SubscriptionNotification == nil || n.SubscriptionNotification.PurchaseToken == "" || n.PackageName != s.purchase.GooglePackage {
		w.WriteHeader(http.StatusOK)
		return
	}
	token := n.SubscriptionNotification.PurchaseToken
	ctx := r.Context()
	sub, err := s.subs.GetSubscription(ctx, storage.SubscriptionID(storage.StoreGooglePlay, token))
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if sub != nil {
		if err := s.refresh(ctx, sub); err != nil {
			writeRefreshError(w, storage.StoreGooglePlay, token, err)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := s.adoptLinkedToken(ctx, token); err != nil {
		writeRefreshError(w, storage.StoreGooglePlay, token, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// adoptLinkedToken binds a new Play purchase token to the handle its
// predecessor pays for, when Google says it replaces one that does.
func (s *Server) adoptLinkedToken(ctx context.Context, token string) error {
	verifier := s.purchase.Verifiers[storage.StoreGooglePlay]
	if verifier == nil {
		return nil
	}
	v, err := verifier.Refresh(ctx, storage.Subscription{Store: storage.StoreGooglePlay, StoreID: token})
	if err != nil || v.LinkedStoreID == "" {
		return err
	}
	old, err := s.subs.GetSubscription(ctx, storage.SubscriptionID(storage.StoreGooglePlay, v.LinkedStoreID))
	if err != nil || old == nil {
		return err
	}
	now := s.clock()
	sub := storage.Subscription{
		ID: storage.SubscriptionID(storage.StoreGooglePlay, token), Store: storage.StoreGooglePlay, StoreID: token,
		Handle: old.Handle, IdentityKey: old.IdentityKey, SubscriptionStatus: v.SubscriptionStatus,
		CreatedAt: now, UpdatedAt: now,
	}
	if sub.Tier != handles.Tier(sub.Handle) {
		sub.State = storage.SubWrongTier
		sub.ExpiresAt = old.ExpiresAt
	}
	err = s.subs.BindSubscription(ctx, sub, nil)
	if errors.Is(err, storage.ErrSubscriptionRaced) {
		return nil // a concurrent delivery or claim bound it first
	}
	return err
}

// SweepLapsedSubscriptions releases the handles whose subscriptions lapsed
// more than the grace period ago. Each candidate is asked about again first,
// so a missed renewal notification never costs anybody their handle; a store
// that cannot be reached leaves the candidate for the next sweep. It returns
// how many handles it released.
func (s *Server) SweepLapsedSubscriptions(ctx context.Context) (int, error) {
	if s.purchase == nil {
		return 0, nil
	}
	now := s.clock()
	cutoff := now.Add(-s.purchase.Grace)
	lapsed, err := s.subs.ListLapsedSubscriptions(ctx, cutoff, sweepBatch)
	if err != nil {
		return 0, err
	}
	released := 0
	for i := range lapsed {
		sub := &lapsed[i]
		switch err := s.refresh(ctx, sub); {
		case errors.Is(err, purchases.ErrInvalid):
			// The store has forgotten it (Play drops tokens some time after
			// expiry): what is on file is the last word.
		case err != nil:
			slog.Warn("lapse sweep could not refresh a subscription", "id", sub.ID, "error", err)
			continue
		}
		if purchases.Entitled(sub.SubscriptionStatus, now) || !sub.ExpiresAt.Before(cutoff) {
			continue
		}
		done, err := s.releaseLapsed(ctx, sub, now)
		if err != nil {
			slog.Error("lapse sweep failed", "id", sub.ID, "handle", sub.Handle, "error", err)
			continue
		}
		if done {
			released++
		}
	}
	return released, nil
}

// releaseLapsed releases sub's handle unless its key no longer holds it or
// another subscription of the same key still pays for it, then marks sub done.
func (s *Server) releaseLapsed(ctx context.Context, sub *storage.Subscription, now time.Time) (bool, error) {
	others, err := s.subs.ListSubscriptionsByHandle(ctx, sub.Handle)
	if err != nil {
		return false, err
	}
	paid := false
	for _, o := range others {
		if o.ID != sub.ID && o.IdentityKey == sub.IdentityKey && o.Tier == handles.Tier(o.Handle) && purchases.Entitled(o.SubscriptionStatus, now) {
			paid = true
		}
	}
	rec, err := s.handles.GetHandle(ctx, sub.Handle)
	if err != nil {
		return false, err
	}
	released := false
	if !paid && rec.Active() && *rec.IdentityKey == sub.IdentityKey {
		// No cooldown: the lapsed subscriber may resubscribe and claim it back
		// straight away, and so may anybody else.
		err := s.handles.ReleaseHandle(ctx, storage.HandleRelease{Handle: sub.Handle, ReleasedBy: ReleasedBySubscription, Now: now})
		if err != nil && !errors.Is(err, storage.ErrHandleNotFound) {
			return false, err
		}
		released = err == nil
		if released {
			slog.Warn("released handle: subscription lapsed", "handle", sub.Handle, "identityKey", sub.IdentityKey,
				"subscription", sub.ID, "state", sub.State, "expiresAt", sub.ExpiresAt)
		}
	}
	return released, s.subs.MarkSubscriptionReleased(ctx, sub.ID, now)
}

// RunSubscriptionSweeper sweeps every interval until ctx ends.
func (s *Server) RunSubscriptionSweeper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := s.SweepLapsedSubscriptions(ctx); err != nil {
			slog.Error("lapse sweep failed", "error", err)
		} else if n > 0 {
			slog.Info("lapse sweep released handles", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
