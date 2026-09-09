package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// mockIdentityKey is used for tests - we bypass the middleware auth
const mockIdentityKey = "028d37b941208cd6b8a4c28288eda5f2f16c2b3ab0fcb6d13c18b47fe37b971fc1"

// setupTestServer returns a Server backed by the in-memory fake store. The
// wallet is nil, so payment paths are out of reach here.
func setupTestServer(t *testing.T) *Server {
	t.Helper()
	store := newFakeStore()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Server{Store: store}
}

// The middleware identity extraction can't be mocked, so the HTTP-level tests
// below only reach the unauthenticated paths. Everything the handlers do to the
// store is covered by the conformance suite; the policy layer is covered
// directly.

func TestListMessagesHandler_NoAuth(t *testing.T) {
	srv := setupTestServer(t)

	body, _ := json.Marshal(map[string]string{"messageBox": "inbox"})
	req := httptest.NewRequest("POST", "/listMessages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No auth context -> should return 401

	w := httptest.NewRecorder()
	srv.ListMessages(w, req)

	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAcknowledgeHandler_NoAuth(t *testing.T) {
	srv := setupTestServer(t)

	body, _ := json.Marshal(map[string]any{"messageIds": []string{"msg1"}})
	req := httptest.NewRequest("POST", "/acknowledgeMessage", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	srv.AcknowledgeMessage(w, req)

	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRegisterDeviceHandler_NoAuth(t *testing.T) {
	srv := setupTestServer(t)

	body, _ := json.Marshal(map[string]any{"fcmToken": "tok-1"})
	req := httptest.NewRequest("POST", "/registerDevice", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	srv.RegisterDevice(w, req)

	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestSmartDefaultFee(t *testing.T) {
	if got := smartDefaultFee("notifications"); got != 10 {
		t.Errorf("smartDefaultFee(notifications) = %d, want 10", got)
	}
	if got := smartDefaultFee("inbox"); got != 0 {
		t.Errorf("smartDefaultFee(inbox) = %d, want 0", got)
	}
}

func TestShouldUseFCMDelivery(t *testing.T) {
	if !shouldUseFCMDelivery("notifications") {
		t.Error("shouldUseFCMDelivery(notifications) = false, want true")
	}
	if shouldUseFCMDelivery("inbox") {
		t.Error("shouldUseFCMDelivery(inbox) = true, want false")
	}
}

func TestSortOrder(t *testing.T) {
	if got := sortOrder("asc"); got != storage.SortAsc {
		t.Errorf("sortOrder(asc) = %v, want SortAsc", got)
	}
	for _, param := range []string{"desc", "", "DESC", "nonsense"} {
		if got := sortOrder(param); got != storage.SortDesc {
			t.Errorf("sortOrder(%q) = %v, want SortDesc", param, got)
		}
	}
}

func TestRecipientFee(t *testing.T) {
	const sender = "02sender"
	ctx := context.Background()

	t.Run("PersistsSmartDefaultOnMiss", func(t *testing.T) {
		srv := setupTestServer(t)

		fee, err := srv.recipientFee(ctx, mockIdentityKey, sender, "notifications")
		if err != nil {
			t.Fatal(err)
		}
		if fee != 10 {
			t.Errorf("fee = %d, want 10 (smart default)", fee)
		}

		// The default must be written, as it was when this lived in the SQL layer.
		p, err := srv.Store.GetPermission(ctx, mockIdentityKey, nil, "notifications")
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			t.Fatal("box-wide permission was not persisted")
		}
		if p.RecipientFee != 10 {
			t.Errorf("persisted fee = %d, want 10", p.RecipientFee)
		}
	})

	t.Run("BoxWideBeatsSmartDefault", func(t *testing.T) {
		srv := setupTestServer(t)
		if err := srv.Store.SetPermission(ctx, mockIdentityKey, nil, "notifications", 3); err != nil {
			t.Fatal(err)
		}

		fee, err := srv.recipientFee(ctx, mockIdentityKey, sender, "notifications")
		if err != nil {
			t.Fatal(err)
		}
		if fee != 3 {
			t.Errorf("fee = %d, want 3", fee)
		}
	})

	t.Run("SenderSpecificBeatsBoxWide", func(t *testing.T) {
		srv := setupTestServer(t)
		if err := srv.Store.SetPermission(ctx, mockIdentityKey, nil, "inbox", 5); err != nil {
			t.Fatal(err)
		}
		senderCopy := sender
		if err := srv.Store.SetPermission(ctx, mockIdentityKey, &senderCopy, "inbox", storage.FeeBlocked); err != nil {
			t.Fatal(err)
		}

		fee, err := srv.recipientFee(ctx, mockIdentityKey, sender, "inbox")
		if err != nil {
			t.Fatal(err)
		}
		if fee != storage.FeeBlocked {
			t.Errorf("fee = %d, want %d", fee, storage.FeeBlocked)
		}
	})

	t.Run("EmptySenderSkipsSenderLookup", func(t *testing.T) {
		srv := setupTestServer(t)
		if err := srv.Store.SetPermission(ctx, mockIdentityKey, nil, "inbox", 7); err != nil {
			t.Fatal(err)
		}

		fee, err := srv.recipientFee(ctx, mockIdentityKey, "", "inbox")
		if err != nil {
			t.Fatal(err)
		}
		if fee != 7 {
			t.Errorf("fee = %d, want 7", fee)
		}
	})
}

func TestFormatTime(t *testing.T) {
	// A non-UTC instant must be converted, not just stamped with a Z. The SQL
	// drivers round-trip the server's local offset, so skipping the conversion
	// would mislabel local time as UTC and diverge from the Mongo backend.
	berlin := time.FixedZone("CEST", 2*60*60)
	local := time.Date(2026, 1, 2, 15, 4, 5, 123_000_000, berlin)

	if got, want := formatTime(local), "2026-01-02T13:04:05.123Z"; got != want {
		t.Errorf("formatTime(%v) = %q, want %q", local, got, want)
	}
	if got := formatTime(local.UTC()); got != formatTime(local) {
		t.Errorf("same instant formatted differently: %q vs %q", got, formatTime(local))
	}
}
