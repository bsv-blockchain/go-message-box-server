// Package storagetest holds the conformance suite that every storage.Store
// implementation must pass. It is a normal (non-_test.go) package so that
// backend packages and test fakes alike can run it.
package storagetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// NewStoreFunc returns a clean, empty store. It is called once per subtest.
type NewStoreFunc func(t *testing.T) storage.Store

const (
	alice = "02alice"
	bob   = "02bob"
	carol = "02carol"
)

// RunStoreTests runs the full storage contract against a backend.
func RunStoreTests(t *testing.T, newStore NewStoreFunc) {
	t.Helper()

	t.Run("Lifecycle", func(t *testing.T) { testLifecycle(t, newStore) })
	t.Run("Messages", func(t *testing.T) { testMessages(t, newStore) })
	t.Run("Permissions", func(t *testing.T) { testPermissions(t, newStore) })
	t.Run("Devices", func(t *testing.T) { testDevices(t, newStore) })
	t.Run("Fees", func(t *testing.T) { testFees(t, newStore) })
}

func testLifecycle(t *testing.T, newStore NewStoreFunc) {
	t.Run("EnsureSchemaIsIdempotent", func(t *testing.T) {
		s := newStore(t)
		if err := s.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("second EnsureSchema: %v", err)
		}
		if err := s.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("third EnsureSchema: %v", err)
		}
	})
}

// --- messages ---------------------------------------------------------------

func msg(id, recipient, box, sender, body string) storage.NewMessage {
	return storage.NewMessage{
		MessageID:  id,
		Recipient:  recipient,
		MessageBox: box,
		Sender:     sender,
		Body:       body,
	}
}

func insert(t *testing.T, s storage.Store, m storage.NewMessage) {
	t.Helper()
	if err := s.InsertMessage(context.Background(), m); err != nil {
		t.Fatalf("InsertMessage(%s): %v", m.MessageID, err)
	}
}

func list(t *testing.T, s storage.Store, recipient, box string) []storage.Message {
	t.Helper()
	got, err := s.ListMessages(context.Background(), recipient, box)
	if err != nil {
		t.Fatalf("ListMessages(%s, %s): %v", recipient, box, err)
	}
	return got
}

func messageIDs(msgs []storage.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.MessageID
	}
	return ids
}

func testMessages(t *testing.T, newStore NewStoreFunc) {
	ctx := context.Background()

	t.Run("RoundTrip", func(t *testing.T) {
		s := newStore(t)
		body := `{"message":{"text":"hi \"there\""},"payment":null}`
		insert(t, s, msg("m1", alice, "inbox", bob, body))

		got := list(t, s, alice, "inbox")
		if len(got) != 1 {
			t.Fatalf("got %d messages, want 1", len(got))
		}
		if got[0].MessageID != "m1" {
			t.Errorf("MessageID = %q, want m1", got[0].MessageID)
		}
		if got[0].Sender != bob {
			t.Errorf("Sender = %q, want %q", got[0].Sender, bob)
		}
		if got[0].Body != body {
			t.Errorf("Body = %q, want %q", got[0].Body, body)
		}
		if got[0].CreatedAt.IsZero() {
			t.Error("CreatedAt is zero")
		}
		if got[0].UpdatedAt.IsZero() {
			t.Error("UpdatedAt is zero")
		}
	})

	t.Run("DuplicateMessageID", func(t *testing.T) {
		s := newStore(t)
		insert(t, s, msg("dup", alice, "inbox", bob, "first"))

		err := s.InsertMessage(ctx, msg("dup", alice, "inbox", carol, "second"))
		if !errors.Is(err, storage.ErrDuplicateMessage) {
			t.Fatalf("second insert error = %v, want ErrDuplicateMessage", err)
		}

		got := list(t, s, alice, "inbox")
		if len(got) != 1 {
			t.Fatalf("got %d messages, want 1 (no second record)", len(got))
		}
		if got[0].Body != "first" {
			t.Errorf("Body = %q, want the original %q", got[0].Body, "first")
		}
	})

	t.Run("UnknownBoxAndRecipient", func(t *testing.T) {
		s := newStore(t)
		insert(t, s, msg("m1", alice, "inbox", bob, "b"))

		if got := list(t, s, alice, "nosuchbox"); len(got) != 0 {
			t.Errorf("unknown box returned %d messages, want 0", len(got))
		}
		if got := list(t, s, "02nobody", "inbox"); len(got) != 0 {
			t.Errorf("unknown recipient returned %d messages, want 0", len(got))
		}
	})

	t.Run("BoxIsolation", func(t *testing.T) {
		s := newStore(t)
		insert(t, s, msg("in1", alice, "inbox", bob, "b"))
		insert(t, s, msg("pay1", alice, "payment_inbox", bob, "b"))

		if got := messageIDs(list(t, s, alice, "inbox")); len(got) != 1 || got[0] != "in1" {
			t.Errorf("inbox = %v, want [in1]", got)
		}
		if got := messageIDs(list(t, s, alice, "payment_inbox")); len(got) != 1 || got[0] != "pay1" {
			t.Errorf("payment_inbox = %v, want [pay1]", got)
		}
	})

	t.Run("RecipientIsolation", func(t *testing.T) {
		s := newStore(t)
		insert(t, s, msg("a1", alice, "inbox", carol, "b"))
		insert(t, s, msg("b1", bob, "inbox", carol, "b"))

		if got := messageIDs(list(t, s, alice, "inbox")); len(got) != 1 || got[0] != "a1" {
			t.Errorf("alice inbox = %v, want [a1]", got)
		}
		if got := messageIDs(list(t, s, bob, "inbox")); len(got) != 1 || got[0] != "b1" {
			t.Errorf("bob inbox = %v, want [b1]", got)
		}
	})

	t.Run("DeterministicOrder", func(t *testing.T) {
		s := newStore(t)
		for _, id := range []string{"m1", "m2", "m3"} {
			insert(t, s, msg(id, alice, "inbox", bob, id))
		}
		got := messageIDs(list(t, s, alice, "inbox"))
		want := []string{"m1", "m2", "m3"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	})

	t.Run("AcknowledgeSubset", func(t *testing.T) {
		s := newStore(t)
		for _, id := range []string{"m1", "m2", "m3"} {
			insert(t, s, msg(id, alice, "inbox", bob, id))
		}

		n, err := s.AcknowledgeMessages(ctx, alice, []string{"m2"})
		if err != nil {
			t.Fatalf("AcknowledgeMessages: %v", err)
		}
		if n != 1 {
			t.Errorf("deleted = %d, want 1", n)
		}
		got := messageIDs(list(t, s, alice, "inbox"))
		if len(got) != 2 || got[0] != "m1" || got[1] != "m3" {
			t.Errorf("remaining = %v, want [m1 m3]", got)
		}
	})

	t.Run("AcknowledgeUnknownAndEmpty", func(t *testing.T) {
		s := newStore(t)
		insert(t, s, msg("m1", alice, "inbox", bob, "b"))

		n, err := s.AcknowledgeMessages(ctx, alice, []string{"nosuch"})
		if err != nil {
			t.Fatalf("AcknowledgeMessages(unknown): %v", err)
		}
		if n != 0 {
			t.Errorf("deleted = %d, want 0", n)
		}

		n, err = s.AcknowledgeMessages(ctx, alice, nil)
		if err != nil {
			t.Fatalf("AcknowledgeMessages(empty): %v", err)
		}
		if n != 0 {
			t.Errorf("deleted = %d, want 0", n)
		}
		if got := list(t, s, alice, "inbox"); len(got) != 1 {
			t.Errorf("got %d messages, want 1 still present", len(got))
		}
	})

	t.Run("AcknowledgeCannotTouchAnotherRecipient", func(t *testing.T) {
		s := newStore(t)
		insert(t, s, msg("b1", bob, "inbox", carol, "b"))

		n, err := s.AcknowledgeMessages(ctx, alice, []string{"b1"})
		if err != nil {
			t.Fatalf("AcknowledgeMessages: %v", err)
		}
		if n != 0 {
			t.Errorf("deleted = %d, want 0", n)
		}
		if got := list(t, s, bob, "inbox"); len(got) != 1 {
			t.Errorf("bob lost his message: got %d, want 1", len(got))
		}
	})
}

// --- permissions ------------------------------------------------------------

func ptr(s string) *string { return &s }

func setPerm(t *testing.T, s storage.Store, recipient string, sender *string, box string, fee int) {
	t.Helper()
	if err := s.SetPermission(context.Background(), recipient, sender, box, fee); err != nil {
		t.Fatalf("SetPermission(%s, %v, %s): %v", recipient, sender, box, err)
	}
}

func getPerm(t *testing.T, s storage.Store, recipient string, sender *string, box string) *storage.Permission {
	t.Helper()
	p, err := s.GetPermission(context.Background(), recipient, sender, box)
	if err != nil {
		t.Fatalf("GetPermission(%s, %v, %s): %v", recipient, sender, box, err)
	}
	return p
}

// permKey renders a permission as "box/sender" for order assertions, using "*"
// for the box-wide row.
func permKey(p storage.Permission) string {
	if p.Sender == nil {
		return p.MessageBox + "/*"
	}
	return p.MessageBox + "/" + *p.Sender
}

func permKeys(ps []storage.Permission) []string {
	keys := make([]string, len(ps))
	for i, p := range ps {
		keys[i] = permKey(p)
	}
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testPermissions(t *testing.T, newStore NewStoreFunc) {
	ctx := context.Background()

	t.Run("BoxWideRoundTrip", func(t *testing.T) {
		s := newStore(t)
		setPerm(t, s, alice, nil, "inbox", 7)

		p := getPerm(t, s, alice, nil, "inbox")
		if p == nil {
			t.Fatal("GetPermission returned nil")
		}
		if p.Sender != nil {
			t.Errorf("Sender = %q, want nil for the box-wide row", *p.Sender)
		}
		if p.Recipient != alice {
			t.Errorf("Recipient = %q, want %q", p.Recipient, alice)
		}
		if p.MessageBox != "inbox" {
			t.Errorf("MessageBox = %q, want inbox", p.MessageBox)
		}
		if p.RecipientFee != 7 {
			t.Errorf("RecipientFee = %d, want 7", p.RecipientFee)
		}
		if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
			t.Error("timestamps are zero")
		}
	})

	t.Run("SenderSpecificRoundTrip", func(t *testing.T) {
		s := newStore(t)
		setPerm(t, s, alice, ptr(bob), "inbox", 42)

		p := getPerm(t, s, alice, ptr(bob), "inbox")
		if p == nil {
			t.Fatal("GetPermission returned nil")
		}
		if p.Sender == nil || *p.Sender != bob {
			t.Errorf("Sender = %v, want %q", p.Sender, bob)
		}
		if p.RecipientFee != 42 {
			t.Errorf("RecipientFee = %d, want 42", p.RecipientFee)
		}
	})

	t.Run("Absent", func(t *testing.T) {
		s := newStore(t)
		if p := getPerm(t, s, alice, nil, "inbox"); p != nil {
			t.Errorf("box-wide = %+v, want nil", p)
		}
		if p := getPerm(t, s, alice, ptr(bob), "inbox"); p != nil {
			t.Errorf("sender-specific = %+v, want nil", p)
		}
	})

	t.Run("UpsertKeepsCreatedAt", func(t *testing.T) {
		s := newStore(t)
		setPerm(t, s, alice, ptr(bob), "inbox", 1)
		before := getPerm(t, s, alice, ptr(bob), "inbox")

		setPerm(t, s, alice, ptr(bob), "inbox", 99)
		after := getPerm(t, s, alice, ptr(bob), "inbox")

		if after.RecipientFee != 99 {
			t.Errorf("RecipientFee = %d, want 99", after.RecipientFee)
		}
		if !after.CreatedAt.Truncate(time.Millisecond).Equal(before.CreatedAt.Truncate(time.Millisecond)) {
			t.Errorf("CreatedAt moved: %v -> %v", before.CreatedAt, after.CreatedAt)
		}
		if after.UpdatedAt.Before(before.UpdatedAt) {
			t.Errorf("UpdatedAt went backwards: %v -> %v", before.UpdatedAt, after.UpdatedAt)
		}

		page, err := s.ListPermissions(ctx, storage.PermissionQuery{Recipient: alice, Limit: 10})
		if err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
		if page.Total != 1 {
			t.Errorf("Total = %d, want 1 (upsert must not duplicate)", page.Total)
		}
	})

	t.Run("BoxWideAndSenderSpecificCoexist", func(t *testing.T) {
		s := newStore(t)
		setPerm(t, s, alice, nil, "inbox", 1)
		setPerm(t, s, alice, ptr(bob), "inbox", 2)

		if p := getPerm(t, s, alice, nil, "inbox"); p == nil || p.RecipientFee != 1 {
			t.Errorf("box-wide = %+v, want fee 1", p)
		}
		if p := getPerm(t, s, alice, ptr(bob), "inbox"); p == nil || p.RecipientFee != 2 {
			t.Errorf("sender-specific = %+v, want fee 2", p)
		}
		if p := getPerm(t, s, alice, ptr(carol), "inbox"); p != nil {
			t.Errorf("carol = %+v, want nil", p)
		}
	})

	t.Run("SetPermissionIfAbsentInserts", func(t *testing.T) {
		s := newStore(t)
		if err := s.SetPermissionIfAbsent(ctx, alice, nil, "inbox", 4); err != nil {
			t.Fatalf("SetPermissionIfAbsent: %v", err)
		}
		if p := getPerm(t, s, alice, nil, "inbox"); p == nil || p.RecipientFee != 4 {
			t.Fatalf("got %+v, want fee 4", p)
		}
	})

	// The fee fallback writes its default through SetPermissionIfAbsent while a
	// recipient may be setting a real permission concurrently. It must never
	// overwrite one — silently replacing a block with a free default would let
	// any sender deliver.
	t.Run("SetPermissionIfAbsentNeverOverwrites", func(t *testing.T) {
		for _, sender := range []*string{nil, ptr(bob)} {
			s := newStore(t)
			setPerm(t, s, alice, sender, "inbox", storage.FeeBlocked)
			before := getPerm(t, s, alice, sender, "inbox")

			if err := s.SetPermissionIfAbsent(ctx, alice, sender, "inbox", 0); err != nil {
				t.Fatalf("sender=%v: SetPermissionIfAbsent: %v", sender, err)
			}

			after := getPerm(t, s, alice, sender, "inbox")
			if after == nil {
				t.Fatalf("sender=%v: permission disappeared", sender)
			}
			if after.RecipientFee != storage.FeeBlocked {
				t.Errorf("sender=%v: RecipientFee = %d, want the existing %d", sender, after.RecipientFee, storage.FeeBlocked)
			}
			if !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("sender=%v: UpdatedAt moved %v -> %v, want untouched", sender, before.UpdatedAt, after.UpdatedAt)
			}

			page, err := s.ListPermissions(ctx, storage.PermissionQuery{Recipient: alice, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 {
				t.Errorf("sender=%v: Total = %d, want 1 (no duplicate row)", sender, page.Total)
			}
		}
	})

	t.Run("FeeBlockedSurvives", func(t *testing.T) {
		s := newStore(t)
		setPerm(t, s, alice, ptr(bob), "inbox", storage.FeeBlocked)
		if p := getPerm(t, s, alice, ptr(bob), "inbox"); p == nil || p.RecipientFee != storage.FeeBlocked {
			t.Errorf("got %+v, want RecipientFee = %d", p, storage.FeeBlocked)
		}
	})

	// seedOrdered writes two boxes x (box-wide + two senders) for alice, plus a
	// row for bob that must never appear in alice's results.
	seedOrdered := func(t *testing.T, s storage.Store) {
		for _, box := range []string{"inbox", "notifications"} {
			setPerm(t, s, alice, nil, box, 0)
			setPerm(t, s, alice, ptr(bob), box, 1)
			setPerm(t, s, alice, ptr(carol), box, 2)
		}
		setPerm(t, s, bob, nil, "inbox", 5)
	}

	// The contract order is MessageBox asc, box-wide first, Sender asc, then
	// CreatedAt per Order. Because (MessageBox, Sender) is unique, CreatedAt is
	// only ever a tiebreak that cannot trigger — so both sort directions yield
	// this same sequence. Asserted for both to pin the behaviour down.
	wantOrder := []string{
		"inbox/*", "inbox/" + bob, "inbox/" + carol,
		"notifications/*", "notifications/" + bob, "notifications/" + carol,
	}

	t.Run("ListOrderAndTotal", func(t *testing.T) {
		for _, order := range []storage.SortOrder{storage.SortAsc, storage.SortDesc} {
			s := newStore(t)
			seedOrdered(t, s)

			page, err := s.ListPermissions(ctx, storage.PermissionQuery{
				Recipient: alice, Limit: 100, Order: order,
			})
			if err != nil {
				t.Fatalf("ListPermissions(order=%d): %v", order, err)
			}
			if page.Total != 6 {
				t.Errorf("order=%d: Total = %d, want 6 (bob's row must be excluded)", order, page.Total)
			}
			if got := permKeys(page.Items); !equalStrings(got, wantOrder) {
				t.Errorf("order=%d: got %v, want %v", order, got, wantOrder)
			}
		}
	})

	t.Run("TotalIgnoresLimit", func(t *testing.T) {
		s := newStore(t)
		seedOrdered(t, s)

		page, err := s.ListPermissions(ctx, storage.PermissionQuery{Recipient: alice, Limit: 2})
		if err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
		if page.Total != 6 {
			t.Errorf("Total = %d, want 6", page.Total)
		}
		if len(page.Items) != 2 {
			t.Fatalf("got %d items, want 2", len(page.Items))
		}
		if got := permKeys(page.Items); !equalStrings(got, wantOrder[:2]) {
			t.Errorf("got %v, want %v", got, wantOrder[:2])
		}
	})

	t.Run("Paging", func(t *testing.T) {
		s := newStore(t)
		seedOrdered(t, s)

		page, err := s.ListPermissions(ctx, storage.PermissionQuery{Recipient: alice, Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
		if got := permKeys(page.Items); !equalStrings(got, wantOrder[2:4]) {
			t.Errorf("got %v, want %v", got, wantOrder[2:4])
		}
		if page.Total != 6 {
			t.Errorf("Total = %d, want 6", page.Total)
		}
	})

	t.Run("OffsetPastEnd", func(t *testing.T) {
		s := newStore(t)
		seedOrdered(t, s)

		page, err := s.ListPermissions(ctx, storage.PermissionQuery{Recipient: alice, Limit: 10, Offset: 100})
		if err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
		if len(page.Items) != 0 {
			t.Errorf("got %d items, want 0", len(page.Items))
		}
		if page.Total != 6 {
			t.Errorf("Total = %d, want 6", page.Total)
		}
	})

	t.Run("MessageBoxFilter", func(t *testing.T) {
		s := newStore(t)
		seedOrdered(t, s)

		page, err := s.ListPermissions(ctx, storage.PermissionQuery{
			Recipient: alice, MessageBox: ptr("notifications"), Limit: 10,
		})
		if err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
		if page.Total != 3 {
			t.Errorf("Total = %d, want 3", page.Total)
		}
		if got := permKeys(page.Items); !equalStrings(got, wantOrder[3:]) {
			t.Errorf("got %v, want %v", got, wantOrder[3:])
		}
	})

	// A zero Limit means "no rows", as LIMIT 0 does in SQL. MongoDB reads limit
	// 0 as "unlimited", so this pins the contract for every backend.
	t.Run("ZeroLimit", func(t *testing.T) {
		s := newStore(t)
		seedOrdered(t, s)

		page, err := s.ListPermissions(ctx, storage.PermissionQuery{Recipient: alice})
		if err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
		if len(page.Items) != 0 {
			t.Errorf("got %d items, want 0", len(page.Items))
		}
		if page.Total != 6 {
			t.Errorf("Total = %d, want 6", page.Total)
		}
	})

	t.Run("ListEmpty", func(t *testing.T) {
		s := newStore(t)
		page, err := s.ListPermissions(ctx, storage.PermissionQuery{Recipient: alice, Limit: 10})
		if err != nil {
			t.Fatalf("ListPermissions: %v", err)
		}
		if page.Total != 0 || len(page.Items) != 0 {
			t.Errorf("got %d items / total %d, want 0/0", len(page.Items), page.Total)
		}
	})
}

// --- devices ----------------------------------------------------------------

func register(t *testing.T, s storage.Store, d storage.NewDevice) {
	t.Helper()
	if err := s.RegisterDevice(context.Background(), d); err != nil {
		t.Fatalf("RegisterDevice(%s): %v", d.FCMToken, err)
	}
}

func devices(t *testing.T, s storage.Store, identityKey string, activeOnly bool) []storage.Device {
	t.Helper()
	var (
		got []storage.Device
		err error
	)
	if activeOnly {
		got, err = s.ListActiveDevices(context.Background(), identityKey)
	} else {
		got, err = s.ListDevices(context.Background(), identityKey)
	}
	if err != nil {
		t.Fatalf("list devices(%s, active=%v): %v", identityKey, activeOnly, err)
	}
	return got
}

func testDevices(t *testing.T, newStore NewStoreFunc) {
	ctx := context.Background()

	t.Run("RoundTrip", func(t *testing.T) {
		s := newStore(t)
		register(t, s, storage.NewDevice{
			IdentityKey: alice,
			FCMToken:    "tok-1",
			DeviceID:    ptr("dev-1"),
			Platform:    ptr("ios"),
		})

		got := devices(t, s, alice, false)
		if len(got) != 1 {
			t.Fatalf("got %d devices, want 1", len(got))
		}
		d := got[0]
		if d.IdentityKey != alice || d.FCMToken != "tok-1" {
			t.Errorf("got %+v, want identityKey %q token tok-1", d, alice)
		}
		if d.DeviceID == nil || *d.DeviceID != "dev-1" {
			t.Errorf("DeviceID = %v, want dev-1", d.DeviceID)
		}
		if d.Platform == nil || *d.Platform != "ios" {
			t.Errorf("Platform = %v, want ios", d.Platform)
		}
		if !d.Active {
			t.Error("Active = false, want true on registration")
		}
		if d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() {
			t.Error("timestamps are zero")
		}
		// Registration sets last_used, matching the pre-refactor SQL.
		if d.LastUsed == nil {
			t.Error("LastUsed = nil, want it set at registration")
		}
	})

	t.Run("NilOptionalFields", func(t *testing.T) {
		s := newStore(t)
		register(t, s, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-1"})

		got := devices(t, s, alice, false)
		if len(got) != 1 {
			t.Fatalf("got %d devices, want 1", len(got))
		}
		if got[0].DeviceID != nil {
			t.Errorf("DeviceID = %v, want nil", *got[0].DeviceID)
		}
		if got[0].Platform != nil {
			t.Errorf("Platform = %v, want nil", *got[0].Platform)
		}
	})

	t.Run("ReRegisterSameTokenUpdates", func(t *testing.T) {
		s := newStore(t)
		register(t, s, storage.NewDevice{
			IdentityKey: alice, FCMToken: "tok-1", DeviceID: ptr("dev-1"), Platform: ptr("ios"),
		})
		register(t, s, storage.NewDevice{
			IdentityKey: alice, FCMToken: "tok-1", DeviceID: ptr("dev-2"), Platform: ptr("android"),
		})

		got := devices(t, s, alice, false)
		if len(got) != 1 {
			t.Fatalf("got %d devices, want 1 (upsert must not duplicate)", len(got))
		}
		if got[0].DeviceID == nil || *got[0].DeviceID != "dev-2" {
			t.Errorf("DeviceID = %v, want dev-2", got[0].DeviceID)
		}
		if got[0].Platform == nil || *got[0].Platform != "android" {
			t.Errorf("Platform = %v, want android", got[0].Platform)
		}
	})

	t.Run("TwoTokens", func(t *testing.T) {
		s := newStore(t)
		register(t, s, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-1"})
		register(t, s, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-2"})
		register(t, s, storage.NewDevice{IdentityKey: bob, FCMToken: "tok-3"})

		if got := devices(t, s, alice, false); len(got) != 2 {
			t.Errorf("alice has %d devices, want 2", len(got))
		}
		if got := devices(t, s, bob, false); len(got) != 1 {
			t.Errorf("bob has %d devices, want 1", len(got))
		}
	})

	t.Run("DeactivateAndReactivate", func(t *testing.T) {
		s := newStore(t)
		register(t, s, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-1"})
		register(t, s, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-2"})

		if err := s.DeactivateDevice(ctx, "tok-1"); err != nil {
			t.Fatalf("DeactivateDevice: %v", err)
		}

		all := devices(t, s, alice, false)
		if len(all) != 2 {
			t.Errorf("ListDevices returned %d, want 2 (deactivated devices stay listed)", len(all))
		}
		active := devices(t, s, alice, true)
		if len(active) != 1 {
			t.Fatalf("ListActiveDevices returned %d, want 1", len(active))
		}
		if active[0].FCMToken != "tok-2" {
			t.Errorf("active token = %q, want tok-2", active[0].FCMToken)
		}

		// Re-registering an invalidated token brings it back.
		register(t, s, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-1"})
		if got := devices(t, s, alice, true); len(got) != 2 {
			t.Errorf("ListActiveDevices returned %d after re-register, want 2", len(got))
		}
	})

	t.Run("UpdateLastUsed", func(t *testing.T) {
		s := newStore(t)
		register(t, s, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-1"})
		before := devices(t, s, alice, false)[0]

		if err := s.UpdateDeviceLastUsed(ctx, "tok-1"); err != nil {
			t.Fatalf("UpdateDeviceLastUsed: %v", err)
		}
		after := devices(t, s, alice, false)[0]
		if after.LastUsed == nil {
			t.Fatal("LastUsed = nil after update")
		}
		if before.LastUsed != nil && after.LastUsed.Before(*before.LastUsed) {
			t.Errorf("LastUsed went backwards: %v -> %v", before.LastUsed, after.LastUsed)
		}
	})

	t.Run("UnknownTokenIsNoOp", func(t *testing.T) {
		s := newStore(t)
		if err := s.UpdateDeviceLastUsed(ctx, "nosuch"); err != nil {
			t.Errorf("UpdateDeviceLastUsed(unknown) = %v, want nil", err)
		}
		if err := s.DeactivateDevice(ctx, "nosuch"); err != nil {
			t.Errorf("DeactivateDevice(unknown) = %v, want nil", err)
		}
		if got := devices(t, s, alice, false); len(got) != 0 {
			t.Errorf("got %d devices, want 0", len(got))
		}
	})

	t.Run("ListEmpty", func(t *testing.T) {
		s := newStore(t)
		if got := devices(t, s, alice, false); len(got) != 0 {
			t.Errorf("ListDevices = %d, want 0", len(got))
		}
		if got := devices(t, s, alice, true); len(got) != 0 {
			t.Errorf("ListActiveDevices = %d, want 0", len(got))
		}
	})
}

// --- fees -------------------------------------------------------------------

func testFees(t *testing.T, newStore NewStoreFunc) {
	ctx := context.Background()

	t.Run("SeededFees", func(t *testing.T) {
		s := newStore(t)
		for box, want := range map[string]int{
			"notifications": 10,
			"inbox":         0,
			"payment_inbox": 0,
		} {
			got, err := s.GetServerDeliveryFee(ctx, box)
			if err != nil {
				t.Fatalf("GetServerDeliveryFee(%s): %v", box, err)
			}
			if got != want {
				t.Errorf("GetServerDeliveryFee(%s) = %d, want %d", box, got, want)
			}
		}
	})

	t.Run("UnknownBox", func(t *testing.T) {
		s := newStore(t)
		got, err := s.GetServerDeliveryFee(ctx, "nosuchbox")
		if err != nil {
			t.Fatalf("GetServerDeliveryFee: %v", err)
		}
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
}
