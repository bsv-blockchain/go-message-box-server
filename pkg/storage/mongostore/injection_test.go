package mongostore

import (
	"context"
	"os"
	"testing"

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
