package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// stubReader is a HandleReader whose answers are fixed. DiagnoseClaim reads the
// registry after a write was refused, so the cases below are exactly the states
// it can find — including the ones a backend cannot be made to produce on
// demand, because they only arise when another write lands between the two.
type stubReader struct {
	byHandle   *storage.HandleRecord
	bySkeleton *storage.HandleRecord
	byKey      *storage.HandleRecord
}

func (s stubReader) GetHandle(context.Context, string) (*storage.HandleRecord, error) {
	return s.byHandle, nil
}

func (s stubReader) GetHandleBySkeleton(context.Context, string) (*storage.HandleRecord, error) {
	return s.bySkeleton, nil
}

func (s stubReader) GetHandleByIdentityKey(context.Context, string) (*storage.HandleRecord, error) {
	return s.byKey, nil
}

// TestDiagnoseClaimOwnerUpdate covers the refusals of an owner re-certifying a
// handle they already hold. The row is active and theirs in every case, so the
// only question is which of the update's three guards — issuedAt, serialNumber,
// the skeleton it writes — the registry still shows refusing the write.
func TestDiagnoseClaimOwnerUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t0 := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	const alice, bob = "02alice", "02bob"
	key, cert := alice, `{"serialNumber":"s1"}`
	owned := func() *storage.HandleRecord {
		return &storage.HandleRecord{
			Handle: "deggen", Skeleton: "degen", IdentityKey: &key, LastIdentityKey: alice,
			Certificate: &cert, SerialNumber: "s1", IssuedAt: t0,
		}
	}
	// A newer certificate on a skeleton the fold table has moved the handle onto.
	update := storage.HandleClaim{
		Handle: "deggen", Skeleton: "degn", IdentityKey: alice,
		Certificate: `{"serialNumber":"s2"}`, SerialNumber: "s2",
		IssuedAt: t0.Add(time.Minute), Now: t0.Add(time.Minute),
	}
	otherKey := bob
	otherRow := &storage.HandleRecord{
		Handle: "xyz", Skeleton: "degn", IdentityKey: &otherKey, LastIdentityKey: bob, SerialNumber: "s1", IssuedAt: t0,
	}

	stale := update
	stale.IssuedAt, stale.Now = t0, t0

	replay := update
	replay.SerialNumber, replay.Certificate, replay.Skeleton = "s1", cert, "degen"
	replay.IssuedAt, replay.Now = t0, t0

	for name, tc := range map[string]struct {
		reader stubReader
		claim  storage.HandleClaim
		want   error
		result storage.ClaimResult
	}{
		// The update's own guards accepted the certificate, so the skeleton is
		// what the write lost on, and the owner must be told which.
		"skeleton held by another row": {
			reader: stubReader{byHandle: owned(), bySkeleton: otherRow, byKey: owned()},
			claim:  update,
			want:   storage.ErrHandleTooSimilar,
		},
		// The registry no longer refuses this claim: the row that held the
		// skeleton moved off it between the write and this read. Answering stale
		// would be a conflict the owner cannot clear with any certificate, so the
		// claim is retried instead.
		"skeleton free again": {
			reader: stubReader{byHandle: owned(), byKey: owned()},
			claim:  update,
			want:   storage.ErrClaimRaced,
		},
		// The handle's own row is not something it can be too similar to.
		"skeleton held by the claimed handle itself": {
			reader: stubReader{byHandle: owned(), bySkeleton: owned(), byKey: owned()},
			claim:  update,
			want:   storage.ErrClaimRaced,
		},
		// The certificate is not newer, which the skeleton read cannot excuse.
		"certificate not newer": {
			reader: stubReader{byHandle: owned(), bySkeleton: otherRow, byKey: owned()},
			claim:  stale,
			want:   storage.ErrStaleCertificate,
		},
		// The stored certificate arriving again stays a no-op, even while another
		// row holds the skeleton this claim asks for.
		"stored certificate replayed": {
			reader: stubReader{byHandle: owned(), bySkeleton: otherRow, byKey: owned()},
			claim:  replay,
			result: storage.ClaimUnchanged,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := storage.DiagnoseClaim(ctx, tc.reader, tc.claim, nil)
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("DiagnoseClaim err = %v, want %v", err, tc.want)
				}
				return
			}
			if err != nil || got != tc.result {
				t.Fatalf("DiagnoseClaim = %v, %v; want %v, nil", got, err, tc.result)
			}
		})
	}
}
