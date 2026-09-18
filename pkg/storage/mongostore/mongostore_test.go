package mongostore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// newMongo returns a store against MONGO_TEST_URI with an empty database. It
// drops every collection first, so point it at a throwaway database.
func newMongo(t *testing.T, uri, database string) storage.Store {
	t.Helper()
	ctx := context.Background()

	s, err := New(ctx, uri, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.db.Drop(ctx); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConformance_Mongo(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	storagetest.RunStoreTests(t, func(t *testing.T) storage.Store {
		return newMongo(t, uri, database)
	})
}

// TestHandleConformance_Mongo runs the handle registry contract. HandleStore is
// not part of storage.Store, so the suite needs the concrete type back.
func TestHandleConformance_Mongo(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	storagetest.RunHandleStoreTests(t, func(t *testing.T) storage.HandleStore {
		return newMongo(t, uri, database).(*Store)
	})
}

// TestHandleTimesTruncateToMilliseconds pins what the contract only states: a
// BSON datetime holds milliseconds, so a finer IssuedAt would be stored as a
// different instant than the one passed and the owner's own resubmission would
// come back ErrStaleCertificate instead of ClaimUnchanged. The store truncates
// on the way in so the value it compares against is the value it stored — and
// does so on copies, because the release times arrive as pointers the caller
// still owns.
func TestHandleTimesTruncateToMilliseconds(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongo(t, uri, database).(*Store)

	const owner = "02alice"
	issued := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC).Add(500 * time.Microsecond)
	c := storage.HandleClaim{
		Handle: "deggen", Skeleton: "degen", IdentityKey: owner,
		Certificate: `{"serialNumber":"s1"}`, SerialNumber: "s1",
		IssuedAt: issued, Now: issued,
	}

	if got, err := s.ClaimHandle(ctx, c); err != nil || got != storage.ClaimCreated {
		t.Fatalf("ClaimHandle = %v, %v; want %v, nil", got, err, storage.ClaimCreated)
	}
	rec, err := s.GetHandle(ctx, "deggen")
	if err != nil || rec == nil {
		t.Fatalf("GetHandle = %v, %v", rec, err)
	}
	if want := issued.Truncate(time.Millisecond); !rec.IssuedAt.Equal(want) {
		t.Errorf("stored issuedAt = %v, want %v", rec.IssuedAt, want)
	}
	// The same certificate again is the one the row already holds.
	if got, err := s.ClaimHandle(ctx, c); err != nil || got != storage.ClaimUnchanged {
		t.Fatalf("replayed ClaimHandle = %v, %v; want %v, nil", got, err, storage.ClaimUnchanged)
	}

	key := owner
	relIssued := issued.Add(time.Hour)
	cooldown := relIssued.Add(24 * time.Hour)
	keptIssued, keptCooldown := relIssued, cooldown
	if err := s.ReleaseHandle(ctx, storage.HandleRelease{
		Handle: "deggen", Owner: &key, IssuedAt: &relIssued,
		ReleasedBy: "owner", CooldownUntil: &cooldown, Now: relIssued,
	}); err != nil {
		t.Fatalf("ReleaseHandle: %v", err)
	}
	if !relIssued.Equal(keptIssued) || !cooldown.Equal(keptCooldown) {
		t.Errorf("release rewrote the caller's times: issuedAt %v cooldownUntil %v", relIssued, cooldown)
	}

	rec, err = s.GetHandle(ctx, "deggen")
	if err != nil || rec == nil {
		t.Fatalf("GetHandle after release = %v, %v", rec, err)
	}
	if want := relIssued.Truncate(time.Millisecond); !rec.IssuedAt.Equal(want) {
		t.Errorf("tombstone issuedAt = %v, want %v", rec.IssuedAt, want)
	}
	if want := cooldown.Truncate(time.Millisecond); rec.CooldownUntil == nil || !rec.CooldownUntil.Equal(want) {
		t.Errorf("tombstone cooldownUntil = %v, want %v", rec.CooldownUntil, want)
	}
	if want := relIssued.Truncate(time.Millisecond); rec.ReleasedAt == nil || !rec.ReleasedAt.Equal(want) {
		t.Errorf("tombstone releasedAt = %v, want %v", rec.ReleasedAt, want)
	}
}
