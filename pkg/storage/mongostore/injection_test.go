package mongostore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// TestOperatorPayloadsAreTreatedAsLiterals documents why CodeQL's
// "database query built from user-controlled sources" alerts on this file are
// false positives.
//
// Every filter value this package puts into a bson.M comes from a typed Go
// string, *string, []string or int. The driver BSON-encodes those as values,
// so a client string that looks like a query operator is matched as that
// literal string and never interpreted structurally. NoSQL injection needs the
// caller to splice a client-supplied *document* into a filter position, which
// the storage contract makes impossible: there is nowhere to pass one.
//
// The one place a value is interpreted as anything but a literal is the handle
// search, which puts the searcher's text under $regex: FindHandles runs it
// through regexp.QuoteMeta first — and anchors the prefix tier itself — so a
// pattern reaches the server with every metacharacter already escaped.
//
// The registry's writes (ClaimHandle, ReleaseHandle) are covered as well as its
// reads: a filter that matched the wrong row there would overwrite or release
// another key's handle rather than merely leak it.
func TestOperatorPayloadsAreTreatedAsLiterals(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongo(t, uri, database)

	const realRecipient = "02realrecipient"

	// Payloads that would match or delete everything if the value were parsed
	// as a query document rather than encoded as a string.
	payloads := []string{
		`{"$ne": null}`,
		`{"$gt": ""}`,
		`{"$exists": true}`,
		"$ne",
		`{"$regex": ".*"}`,
	}

	if err := s.InsertMessage(ctx, storage.NewMessage{
		MessageID: "m1", Recipient: realRecipient, MessageBox: "inbox",
		Sender: "02sender", Body: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPermission(ctx, realRecipient, nil, "inbox", storage.FeeBlocked); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: realRecipient, FCMToken: "tok-1"}); err != nil {
		t.Fatal(err)
	}

	for _, payload := range payloads {
		t.Run("ListMessages/"+payload, func(t *testing.T) {
			got, err := s.ListMessages(ctx, payload, "inbox")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("payload matched %d messages, want 0", len(got))
			}
		})

		t.Run("AcknowledgeMessages/"+payload, func(t *testing.T) {
			n, err := s.AcknowledgeMessages(ctx, payload, []string{payload, "m1"})
			if err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("payload deleted %d messages, want 0", n)
			}
		})

		t.Run("PageMessages/"+payload, func(t *testing.T) {
			pager, ok := s.(storage.MessagePager)
			if !ok {
				t.Fatal("mongostore.Store must implement storage.MessagePager")
			}
			// As the messageId filter (mongostore.go's filter["_id"]).
			got, err := pager.PageMessages(ctx, storage.MessagePageQuery{
				Recipient: realRecipient, MessageBox: "inbox", FetchLimit: 10, MessageID: &payload,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("payload as messageId matched %d messages, want 0", len(got))
			}
			// As messageBox.
			got, err = pager.PageMessages(ctx, storage.MessagePageQuery{
				Recipient: realRecipient, MessageBox: payload, FetchLimit: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("payload as messageBox matched %d messages, want 0", len(got))
			}
			// As recipient.
			got, err = pager.PageMessages(ctx, storage.MessagePageQuery{
				Recipient: payload, MessageBox: "inbox", FetchLimit: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("payload as recipient matched %d messages, want 0", len(got))
			}
		})

		t.Run("GetPermission/"+payload, func(t *testing.T) {
			p, err := s.GetPermission(ctx, payload, nil, "inbox")
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Errorf("payload matched permission %+v, want nil", p)
			}
			// Also as the sender, which is the nullable field.
			p, err = s.GetPermission(ctx, realRecipient, &payload, "inbox")
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Errorf("payload as sender matched %+v, want nil", p)
			}
		})

		t.Run("ListPermissions/"+payload, func(t *testing.T) {
			box := payload
			page, err := s.ListPermissions(ctx, storage.PermissionQuery{
				Recipient: payload, MessageBox: &box, Limit: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 0 || len(page.Items) != 0 {
				t.Errorf("payload matched %d/%d permissions, want 0/0", len(page.Items), page.Total)
			}
		})

		t.Run("ListDevices/"+payload, func(t *testing.T) {
			got, err := s.ListDevices(ctx, payload)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Errorf("payload matched %d devices, want 0", len(got))
			}
		})

		t.Run("DeactivateDevice/"+payload, func(t *testing.T) {
			if err := s.DeactivateDevice(ctx, payload); err != nil {
				t.Fatal(err)
			}
			active, err := s.ListActiveDevices(ctx, realRecipient)
			if err != nil {
				t.Fatal(err)
			}
			if len(active) != 1 {
				t.Errorf("payload deactivated the real device: %d active, want 1", len(active))
			}
		})

		t.Run("GetServerDeliveryFee/"+payload, func(t *testing.T) {
			fee, err := s.GetServerDeliveryFee(ctx, payload)
			if err != nil {
				t.Fatal(err)
			}
			if fee != 0 {
				t.Errorf("payload matched a fee of %d, want 0", fee)
			}
		})
	}

	// The handle registry is the one place a value becomes a pattern, so it adds
	// regex metacharacters to the operator payloads above. ".*" and "^" would
	// match every handle if the text were compiled rather than escaped.
	const realHandle = "deggen"
	issued := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	if _, err := s.(*Store).ClaimHandle(ctx, storage.HandleClaim{
		Handle: realHandle, Skeleton: "degen", IdentityKey: realRecipient,
		Certificate: `{"serialNumber":"s1"}`, SerialNumber: "s1",
		IssuedAt: issued, Now: issued,
	}); err != nil {
		t.Fatal(err)
	}

	for _, payload := range append([]string{".*", "^", "de*", "d.ggen", "[a-z]+"}, payloads...) {
		t.Run("FindHandles/"+payload, func(t *testing.T) {
			for _, m := range []storage.HandleMatch{
				{Field: storage.HandleFieldHandle, Mode: storage.HandleMatchPrefix, Value: payload, Limit: 10},
				{Field: storage.HandleFieldHandle, Mode: storage.HandleMatchContains, Value: payload, Limit: 10},
				{Field: storage.HandleFieldSkeleton, Mode: storage.HandleMatchPrefix, Value: payload, Limit: 10},
			} {
				got, err := s.(*Store).FindHandles(ctx, m)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != 0 {
					t.Errorf("payload matched %d handles on field %v mode %v, want 0", len(got), m.Field, m.Mode)
				}
			}
		})

		t.Run("GetHandle/"+payload, func(t *testing.T) {
			for name, get := range map[string]func() (*storage.HandleRecord, error){
				"handle":   func() (*storage.HandleRecord, error) { return s.(*Store).GetHandle(ctx, payload) },
				"skeleton": func() (*storage.HandleRecord, error) { return s.(*Store).GetHandleBySkeleton(ctx, payload) },
				"key":      func() (*storage.HandleRecord, error) { return s.(*Store).GetHandleByIdentityKey(ctx, payload) },
			} {
				rec, err := get()
				if err != nil {
					t.Fatal(err)
				}
				if rec != nil {
					t.Errorf("payload matched a %s record %+v, want nil", name, rec)
				}
			}
		})
	}

	// The registry's writes filter on the same client strings. Parsed as a
	// document, {"$ne": null} in the identityKey position would match the real
	// owner's row, so an update would overwrite their certificate and a release
	// would take their handle away. These run after the reads above because the
	// last case stores rows whose handle is the payload itself.
	later := issued.Add(time.Hour)
	for i, payload := range payloads {
		t.Run("ClaimHandle/"+payload, func(t *testing.T) {
			// As the identity key, against the real handle.
			_, err := s.(*Store).ClaimHandle(ctx, storage.HandleClaim{
				Handle: realHandle, Skeleton: "degen", IdentityKey: payload,
				Certificate: `{"serialNumber":"evil"}`, SerialNumber: "evil",
				IssuedAt: later, Now: later,
			})
			if !errors.Is(err, storage.ErrHandleTaken) {
				t.Errorf("payload as identity key: err = %v, want ErrHandleTaken", err)
			}

			// As the handle, it is stored and read back as that literal string.
			key := fmt.Sprintf("02payloadkey%d", i)
			res, err := s.(*Store).ClaimHandle(ctx, storage.HandleClaim{
				Handle: payload, Skeleton: fmt.Sprintf("payloadskeleton%d", i), IdentityKey: key,
				Certificate: `{"serialNumber":"p"}`, SerialNumber: "p",
				IssuedAt: later, Now: later,
			})
			if err != nil || res != storage.ClaimCreated {
				t.Fatalf("payload as handle: %v, %v; want ClaimCreated, nil", res, err)
			}
			rec, err := s.(*Store).GetHandle(ctx, payload)
			if err != nil {
				t.Fatal(err)
			}
			if rec == nil || rec.Handle != payload || rec.IdentityKey == nil || *rec.IdentityKey != key {
				t.Errorf("payload handle read back as %+v", rec)
			}
		})

		t.Run("ReleaseHandle/"+payload, func(t *testing.T) {
			// As the owner, against the real handle.
			owner := payload
			err := s.(*Store).ReleaseHandle(ctx, storage.HandleRelease{
				Handle: realHandle, Owner: &owner, IssuedAt: &later,
				ReleasedBy: storage.ReleasedByOwner, Now: later,
			})
			if !errors.Is(err, storage.ErrHandleTaken) {
				t.Errorf("payload as owner: err = %v, want ErrHandleTaken", err)
			}

			// As the handle of an operator release, which filters on nothing
			// else. The previous subtest stored a row under this exact string,
			// so exactly that row goes and the real one stays.
			if err := s.(*Store).ReleaseHandle(ctx, storage.HandleRelease{
				Handle: payload, ReleasedBy: "02operator", Now: later,
			}); err != nil {
				t.Fatalf("operator release of the payload's own row: %v", err)
			}
		})

		t.Run("RealHandleUntouched/"+payload, func(t *testing.T) {
			rec, err := s.(*Store).GetHandle(ctx, realHandle)
			if err != nil {
				t.Fatal(err)
			}
			if rec == nil || !rec.Active() || *rec.IdentityKey != realRecipient ||
				rec.SerialNumber != "s1" || rec.Certificate == nil || *rec.Certificate != `{"serialNumber":"s1"}` ||
				!rec.IssuedAt.Equal(issued) {
				t.Errorf("real handle was disturbed: %+v", rec)
			}
		})
	}

	// The real handle is still there, and still findable by its own text.
	found, err := s.(*Store).FindHandles(ctx, storage.HandleMatch{
		Field: storage.HandleFieldHandle, Mode: storage.HandleMatchPrefix, Value: "deg", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Handle != realHandle {
		t.Errorf("real handle was disturbed: %+v", found)
	}

	// The real data must still be intact after every payload above.
	msgs, err := s.ListMessages(ctx, realRecipient, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Body != "secret" {
		t.Errorf("real message was disturbed: %+v", msgs)
	}
	p, err := s.GetPermission(ctx, realRecipient, nil, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.RecipientFee != storage.FeeBlocked {
		t.Errorf("real permission was disturbed: %+v", p)
	}
}
