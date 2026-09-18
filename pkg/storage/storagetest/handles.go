package storagetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// NewHandleStoreFunc returns a clean, empty handle store. Called once per subtest.
type NewHandleStoreFunc func(t *testing.T) storage.HandleStore

// handleT0 is the issuedAt of a first claim. The suite works in offsets from it
// so that certificate time (chosen by the caller) never depends on wall clock.
var handleT0 = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func claim(handle, skeleton, key, serial string, issuedAt time.Time) storage.HandleClaim {
	return storage.HandleClaim{
		Handle: handle, Skeleton: skeleton, IdentityKey: key,
		Certificate: `{"serialNumber":"` + serial + `"}`, SerialNumber: serial,
		IssuedAt: issuedAt, Now: issuedAt,
	}
}

func mustClaim(t *testing.T, s storage.HandleStore, c storage.HandleClaim, want storage.ClaimResult) {
	t.Helper()

	got, err := s.ClaimHandle(context.Background(), c)
	if err != nil || got != want {
		t.Fatalf("ClaimHandle(%s) = %v, %v; want %v, nil", c.Handle, got, err, want)
	}
}

func wantClaimErr(t *testing.T, s storage.HandleStore, c storage.HandleClaim, want error) {
	t.Helper()

	if _, err := s.ClaimHandle(context.Background(), c); !errors.Is(err, want) {
		t.Fatalf("ClaimHandle(%s) err = %v, want %v", c.Handle, err, want)
	}
}

// RunHandleStoreTests runs the HandleStore contract against a backend.
func RunHandleStoreTests(t *testing.T, newStore NewHandleStoreFunc) {
	t.Helper()
	ctx := context.Background()

	t.Run("RegisterAndRead", func(t *testing.T) {
		s := newStore(t)
		before := time.Now()
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		after := time.Now()

		rec, err := s.GetHandle(ctx, "deggen")
		if err != nil || rec == nil {
			t.Fatalf("GetHandle = %v, %v", rec, err)
		}
		if !rec.Active() || *rec.IdentityKey != alice || rec.LastIdentityKey != alice {
			t.Errorf("owner = %v / %s", rec.IdentityKey, rec.LastIdentityKey)
		}
		if rec.Certificate == nil || *rec.Certificate != `{"serialNumber":"s1"}` || rec.SerialNumber != "s1" {
			t.Errorf("certificate = %v serial = %s", rec.Certificate, rec.SerialNumber)
		}
		if !rec.IssuedAt.Equal(handleT0) || rec.Skeleton != "degen" {
			t.Errorf("issuedAt = %v skeleton = %s", rec.IssuedAt, rec.Skeleton)
		}
		if rec.ReleasedAt != nil || rec.CooldownUntil != nil || rec.ReleasedBy != nil {
			t.Error("release fields set on a fresh row")
		}
		assertRecentUTC(t, "CreatedAt", rec.CreatedAt, before, after)
		assertRecentUTC(t, "UpdatedAt", rec.UpdatedAt, before, after)

		if r, _ := s.GetHandleBySkeleton(ctx, "degen"); r == nil || r.Handle != "deggen" {
			t.Errorf("GetHandleBySkeleton = %v", r)
		}
		if r, _ := s.GetHandleByIdentityKey(ctx, alice); r == nil || r.Handle != "deggen" {
			t.Errorf("GetHandleByIdentityKey = %v", r)
		}
		for name, get := range map[string]func() (*storage.HandleRecord, error){
			"handle":   func() (*storage.HandleRecord, error) { return s.GetHandle(ctx, "nobody") },
			"skeleton": func() (*storage.HandleRecord, error) { return s.GetHandleBySkeleton(ctx, "nobody") },
			"key":      func() (*storage.HandleRecord, error) { return s.GetHandleByIdentityKey(ctx, bob) },
		} {
			r, err := get()
			if r != nil || err != nil {
				t.Errorf("missing %s = %v, %v; want nil, nil", name, r, err)
				continue
			}
			// The readers' (nil, nil) must be safe to ask about directly.
			if r.Active() {
				t.Errorf("missing %s reports Active", name)
			}
		}
	})

	t.Run("Conflicts", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)

		wantClaimErr(t, s, claim("deggen", "degen", bob, "s2", handleT0.Add(time.Hour)), storage.ErrHandleTaken)
		wantClaimErr(t, s, claim("d3ggen", "degen", bob, "s2", handleT0), storage.ErrHandleTooSimilar)
		wantClaimErr(t, s, claim("other", "other", alice, "s2", handleT0.Add(time.Hour)), storage.ErrKeyHasHandle)
	})

	t.Run("Update", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		first, _ := s.GetHandle(ctx, "deggen")
		separateWrites()

		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimUnchanged)
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s2", handleT0), storage.ErrStaleCertificate)
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s2", handleT0.Add(-time.Second)), storage.ErrStaleCertificate)
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s1", handleT0.Add(time.Second)), storage.ErrStaleCertificate)

		mustClaim(t, s, claim("deggen", "degen", alice, "s2", handleT0.Add(time.Second)), storage.ClaimUpdated)
		rec, _ := s.GetHandle(ctx, "deggen")
		if rec.SerialNumber != "s2" || !rec.IssuedAt.Equal(handleT0.Add(time.Second)) {
			t.Errorf("after update: serial %s issuedAt %v", rec.SerialNumber, rec.IssuedAt)
		}
		if !rec.CreatedAt.Equal(first.CreatedAt) || !rec.UpdatedAt.After(first.UpdatedAt) {
			t.Errorf("createdAt %v→%v updatedAt %v→%v", first.CreatedAt, rec.CreatedAt, first.UpdatedAt, rec.UpdatedAt)
		}
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ErrStaleCertificate)
	})

	t.Run("OwnerReleaseAndCooldown", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)

		relAt := handleT0.Add(time.Hour)
		until := relAt.Add(30 * 24 * time.Hour)
		a, b := alice, bob

		stale := handleT0
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &stale, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); !errors.Is(err, storage.ErrStaleCertificate) {
			t.Fatalf("stale release err = %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &b, IssuedAt: &relAt, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); !errors.Is(err, storage.ErrHandleTaken) {
			t.Fatalf("non-owner release err = %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "nobody", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", Now: relAt}); !errors.Is(err, storage.ErrHandleNotFound) {
			t.Fatalf("missing release err = %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); err != nil {
			t.Fatalf("release: %v", err)
		}
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &until, ReleasedBy: "owner", Now: until}); !errors.Is(err, storage.ErrHandleNotFound) {
			t.Fatalf("double release err = %v", err)
		}

		rec, _ := s.GetHandle(ctx, "deggen")
		if rec.Active() || rec.Certificate != nil || rec.LastIdentityKey != alice {
			t.Errorf("released row = %+v", rec)
		}
		if rec.ReleasedAt == nil || !rec.ReleasedAt.Equal(relAt) || rec.CooldownUntil == nil || !rec.CooldownUntil.Equal(until) || rec.ReleasedBy == nil || *rec.ReleasedBy != "owner" {
			t.Errorf("release fields = %v %v %v", rec.ReleasedAt, rec.CooldownUntil, rec.ReleasedBy)
		}
		if !rec.IssuedAt.Equal(relAt) {
			t.Errorf("issuedAt after tombstone = %v, want %v", rec.IssuedAt, relAt)
		}
		if r, _ := s.GetHandleByIdentityKey(ctx, alice); r != nil {
			t.Error("released key still resolves")
		}
		if r, _ := s.GetHandleBySkeleton(ctx, "degen"); r == nil {
			t.Error("skeleton reservation lost on release")
		}

		// Replay of the original certificate, and of anything not newer than the tombstone.
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ErrStaleCertificate)
		// Another key inside cooldown.
		in := claim("deggen", "degen", bob, "s3", relAt.Add(time.Minute))
		wantClaimErr(t, s, in, storage.ErrHandleCooldown)
		// A look-alike is still blocked while the row exists.
		wantClaimErr(t, s, claim("d3ggen", "degen", bob, "s3", relAt.Add(time.Minute)), storage.ErrHandleTooSimilar)
		// Alice frees herself to take another handle.
		mustClaim(t, s, claim("alice", "alice", alice, "s4", relAt.Add(time.Minute)), storage.ClaimCreated)
		// ...and so cannot reclaim the old one while holding the new.
		wantClaimErr(t, s, claim("deggen", "degen", alice, "s5", relAt.Add(2*time.Minute)), storage.ErrKeyHasHandle)
		// After cooldown another key may claim.
		late := claim("deggen", "degen", bob, "s6", until.Add(time.Second))
		mustClaim(t, s, late, storage.ClaimCreated)
		rec, _ = s.GetHandle(ctx, "deggen")
		if !rec.Active() || *rec.IdentityKey != bob || rec.LastIdentityKey != bob || rec.ReleasedAt != nil || rec.CooldownUntil != nil || rec.ReleasedBy != nil {
			t.Errorf("reclaimed row = %+v", rec)
		}
	})

	t.Run("PreviousOwnerReclaimsInsideCooldown", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		a := alice
		relAt := handleT0.Add(time.Hour)
		until := relAt.Add(time.Hour * 720)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); err != nil {
			t.Fatal(err)
		}
		mustClaim(t, s, claim("deggen", "degen", alice, "s2", relAt.Add(time.Minute)), storage.ClaimCreated)
	})

	t.Run("ClaimAtCooldownEnd", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		a := alice
		relAt := handleT0.Add(time.Hour)
		until := relAt.Add(30 * 24 * time.Hour)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", CooldownUntil: &until, Now: relAt}); err != nil {
			t.Fatal(err)
		}
		// The cooldown is over at until itself, not a tick later.
		mustClaim(t, s, claim("deggen", "degen", bob, "s2", until), storage.ClaimCreated)
	})

	t.Run("ReclaimKeepsSkeletonUnique", func(t *testing.T) {
		s := newStore(t)
		a := alice
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		mustClaim(t, s, claim("xyz", "degn", bob, "s1", handleT0), storage.ClaimCreated)
		relAt := handleT0.Add(time.Hour)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", Now: relAt}); err != nil {
			t.Fatal(err)
		}
		// A reclaim stores the claim's skeleton, so it collides like any other.
		wantClaimErr(t, s, claim("deggen", "degn", alice, "s2", relAt.Add(time.Minute)), storage.ErrHandleTooSimilar)
	})

	t.Run("ReclaimStoresNewSkeleton", func(t *testing.T) {
		s := newStore(t)
		a := alice
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		relAt := handleT0.Add(time.Hour)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", Owner: &a, IssuedAt: &relAt, ReleasedBy: "owner", Now: relAt}); err != nil {
			t.Fatal(err)
		}
		// The fold table can grow between the insert and the reclaim, so the
		// claim's skeleton — not the stored one — is what the row must end with.
		mustClaim(t, s, claim("deggen", "degn", alice, "s2", relAt.Add(time.Minute)), storage.ClaimCreated)
		if rec, _ := s.GetHandle(ctx, "deggen"); rec == nil || rec.Skeleton != "degn" {
			t.Errorf("reclaimed skeleton = %v, want degn", rec)
		}
		if r, _ := s.GetHandleBySkeleton(ctx, "degn"); r == nil || r.Handle != "deggen" {
			t.Errorf("GetHandleBySkeleton(degn) = %v, want deggen", r)
		}
		if r, _ := s.GetHandleBySkeleton(ctx, "degen"); r != nil {
			t.Errorf("stale skeleton still resolves to %v", r)
		}
	})

	t.Run("AdminReleaseSkipsCooldown", func(t *testing.T) {
		s := newStore(t)
		mustClaim(t, s, claim("deggen", "degen", alice, "s1", handleT0), storage.ClaimCreated)
		now := handleT0.Add(time.Hour)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "deggen", ReleasedBy: carol, Now: now}); err != nil {
			t.Fatalf("admin release: %v", err)
		}
		rec, _ := s.GetHandle(ctx, "deggen")
		if rec.Active() || rec.CooldownUntil != nil || rec.ReleasedBy == nil || *rec.ReleasedBy != carol || !rec.IssuedAt.Equal(handleT0) {
			t.Errorf("admin released row = %+v", rec)
		}
		mustClaim(t, s, claim("deggen", "degen", bob, "s2", now.Add(time.Minute)), storage.ClaimCreated)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "nobody", ReleasedBy: carol, Now: now}); !errors.Is(err, storage.ErrHandleNotFound) {
			t.Fatalf("admin release of missing handle err = %v", err)
		}
	})

	t.Run("FindHandles", func(t *testing.T) {
		s := newStore(t)
		for i, h := range []struct{ handle, skeleton string }{
			{"deggen", "degen"}, {"deg", "deg"}, {"degas", "degas"}, {"a_deg", "adeg"}, {"bob", "bob"}, {"d3x", "d3x"},
		} {
			mustClaim(t, s, claim(h.handle, h.skeleton, fmt.Sprintf("02key%d", i), "s", handleT0), storage.ClaimCreated)
		}
		// A released handle must not be found.
		k := "02key4"
		rel := handleT0.Add(time.Second)
		if err := s.ReleaseHandle(ctx, storage.HandleRelease{Handle: "bob", Owner: &k, IssuedAt: &rel, ReleasedBy: "owner", Now: rel}); err != nil {
			t.Fatal(err)
		}

		find := func(f storage.HandleField, m storage.HandleMatchMode, v string, limit int) []string {
			t.Helper()
			recs, err := s.FindHandles(ctx, storage.HandleMatch{Field: f, Mode: m, Value: v, Limit: limit})
			if err != nil {
				t.Fatalf("FindHandles: %v", err)
			}
			if recs == nil {
				t.Errorf("FindHandles(%v, %q) = nil slice, want empty non-nil", f, v)
			}
			out := make([]string, len(recs))
			for i, r := range recs {
				out[i] = r.Handle
			}
			return out
		}
		check := func(label string, got, want []string) {
			t.Helper()
			if !equalStrings(got, want) {
				t.Errorf("%s = %v, want %v", label, got, want)
			}
		}
		check("handle prefix", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "deg", 10), []string{"deg", "degas", "deggen"})
		check("handle prefix limit", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "deg", 2), []string{"deg", "degas"})
		check("handle contains", find(storage.HandleFieldHandle, storage.HandleMatchContains, "deg", 10), []string{"a_deg", "deg", "degas", "deggen"})
		check("skeleton prefix", find(storage.HandleFieldSkeleton, storage.HandleMatchPrefix, "dege", 10), []string{"deggen"})
		check("released excluded", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "bob", 10), []string{})
		// "_" and "%" and "." are literals, not wildcards.
		check("underscore literal", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "a_", 10), []string{"a_deg"})
		check("underscore is not wildcard", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "d_x", 10), []string{})
		check("percent literal", find(storage.HandleFieldHandle, storage.HandleMatchContains, "%", 10), []string{})
		check("dot literal", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "d.x", 10), []string{})
		// Metacharacters a backend must escape rather than compile: "de*" would
		// otherwise match every "d…", and "a(b[" is not a valid pattern at all.
		check("star literal", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "de*", 10), []string{})
		check("unbalanced group literal", find(storage.HandleFieldHandle, storage.HandleMatchContains, "a(b[", 10), []string{})
		// A limit that asks for nothing returns nothing; it is never "unlimited".
		check("zero limit", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "deg", 0), []string{})
		check("negative limit", find(storage.HandleFieldHandle, storage.HandleMatchPrefix, "deg", -1), []string{})
	})

	t.Run("ConcurrentClaimsOneWinner", func(t *testing.T) {
		s := newStore(t)
		const n = 16
		var wg sync.WaitGroup
		results := make([]storage.ClaimResult, n)
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				// Distinct handles and keys, one shared skeleton.
				c := claim(fmt.Sprintf("pay-pal%02d", i), "paypal", fmt.Sprintf("02racer%02d", i), "s", handleT0)
				results[i], errs[i] = s.ClaimHandle(ctx, c)
			}(i)
		}
		wg.Wait()
		created := 0
		for i := range results {
			switch {
			case errs[i] == nil && results[i] == storage.ClaimCreated:
				created++
			case errs[i] == nil:
				t.Errorf("racer %d: result %v without error", i, results[i])
			case !errors.Is(errs[i], storage.ErrHandleTooSimilar):
				t.Logf("racer %d lost with a backend error (acceptable, retryable): %v", i, errs[i])
			}
		}
		if created != 1 {
			t.Fatalf("%d claims created, want exactly 1", created)
		}
	})
}
