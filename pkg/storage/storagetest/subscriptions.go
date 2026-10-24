package storagetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// NewSubscriptionStoreFunc returns a clean, empty subscription store. Called
// once per subtest.
type NewSubscriptionStoreFunc func(t *testing.T) storage.SubscriptionStore

func sub(storeID, handle, key string, expires time.Time) storage.Subscription {
	return storage.Subscription{
		ID: storage.SubscriptionID(storage.StoreAppStore, storeID), Store: storage.StoreAppStore, StoreID: storeID,
		Handle: handle, IdentityKey: key,
		SubscriptionStatus: storage.SubscriptionStatus{
			ProductID: "org.bsvblockchain.wallet.handle.len6.monthly", Tier: 6,
			State: storage.SubActive, ExpiresAt: expires, Environment: "Production",
		},
		CreatedAt: handleT0, UpdatedAt: handleT0,
	}
}

// RunSubscriptionStoreTests runs the SubscriptionStore contract against a backend.
func RunSubscriptionStoreTests(t *testing.T, newStore NewSubscriptionStoreFunc) {
	t.Helper()
	ctx := context.Background()

	t.Run("BindAndRead", func(t *testing.T) {
		s := newStore(t)
		if got, err := s.GetSubscription(ctx, "app-store:none"); got != nil || err != nil {
			t.Fatalf("GetSubscription(missing) = %v, %v; want nil, nil", got, err)
		}
		want := sub("1000", "deggen", alice, handleT0.Add(30*24*time.Hour))
		if err := s.BindSubscription(ctx, want, nil); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSubscription(ctx, want.ID)
		if err != nil || got == nil {
			t.Fatalf("GetSubscription = %v, %v", got, err)
		}
		if got.Handle != "deggen" || got.IdentityKey != alice || got.StoreID != "1000" || got.Store != storage.StoreAppStore ||
			got.ProductID != want.ProductID || got.Tier != 6 || got.State != storage.SubActive ||
			!got.ExpiresAt.Equal(want.ExpiresAt) || got.Environment != "Production" || got.ReleasedAt != nil {
			t.Fatalf("read back %+v, want %+v", got, want)
		}
	})

	t.Run("InsertTwiceRaces", func(t *testing.T) {
		s := newStore(t)
		a := sub("1000", "deggen", alice, handleT0)
		if err := s.BindSubscription(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		b := sub("1000", "other", bob, handleT0)
		if err := s.BindSubscription(ctx, b, nil); !errors.Is(err, storage.ErrSubscriptionRaced) {
			t.Fatalf("second insert = %v, want ErrSubscriptionRaced", err)
		}
		got, _ := s.GetSubscription(ctx, a.ID)
		if got.Handle != "deggen" || got.IdentityKey != alice {
			t.Fatalf("losing insert overwrote the row: %+v", got)
		}
	})

	t.Run("RebindIsConditional", func(t *testing.T) {
		s := newStore(t)
		a := sub("1000", "deggen", alice, handleT0)
		if err := s.BindSubscription(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkSubscriptionReleased(ctx, a.ID, handleT0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		// A rebind against a stale picture of the row writes nothing.
		stale := a
		stale.Handle = "someone"
		moved := sub("1000", "newname", alice, handleT0.Add(time.Hour))
		if err := s.BindSubscription(ctx, moved, &stale); !errors.Is(err, storage.ErrSubscriptionRaced) {
			t.Fatalf("stale rebind = %v, want ErrSubscriptionRaced", err)
		}
		if err := s.BindSubscription(ctx, moved, &a); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetSubscription(ctx, a.ID)
		if got.Handle != "newname" || got.ReleasedAt != nil || !got.CreatedAt.Equal(handleT0) {
			t.Fatalf("after rebind %+v; want handle newname, no ReleasedAt, CreatedAt kept", got)
		}
		// Rebinding to the same binding is allowed (a refreshed status).
		if err := s.BindSubscription(ctx, moved, &moved); err != nil {
			t.Fatalf("rebind in place = %v", err)
		}
		// A rebind of a row that does not exist is a race too.
		ghost := sub("2000", "x", alice, handleT0)
		if err := s.BindSubscription(ctx, ghost, &ghost); !errors.Is(err, storage.ErrSubscriptionRaced) {
			t.Fatalf("rebind of missing row = %v, want ErrSubscriptionRaced", err)
		}
	})

	t.Run("UpdateStatus", func(t *testing.T) {
		s := newStore(t)
		a := sub("1000", "deggen", alice, handleT0)
		if err := s.BindSubscription(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		st := storage.SubscriptionStatus{ProductID: "p", BasePlan: "yearly", Tier: 3, State: storage.SubGrace, ExpiresAt: handleT0.Add(time.Hour), Environment: "Sandbox"}
		if err := s.UpdateSubscriptionStatus(ctx, a.ID, st, handleT0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetSubscription(ctx, a.ID)
		gotSt := got.SubscriptionStatus
		gotSt.ExpiresAt = gotSt.ExpiresAt.UTC()
		if gotSt != st || got.Handle != "deggen" || !got.UpdatedAt.Equal(handleT0.Add(time.Minute)) {
			t.Fatalf("after update %+v, want status %+v", got, st)
		}
		if err := s.UpdateSubscriptionStatus(ctx, "app-store:none", st, handleT0); !errors.Is(err, storage.ErrSubscriptionNotFound) {
			t.Fatalf("update missing = %v, want ErrSubscriptionNotFound", err)
		}
		if err := s.MarkSubscriptionReleased(ctx, "app-store:none", handleT0); !errors.Is(err, storage.ErrSubscriptionNotFound) {
			t.Fatalf("release missing = %v, want ErrSubscriptionNotFound", err)
		}
	})

	t.Run("ListByHandle", func(t *testing.T) {
		s := newStore(t)
		if got, err := s.ListSubscriptionsByHandle(ctx, "deggen"); err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty list = %v, %v; want [], nil", got, err)
		}
		for _, x := range []storage.Subscription{sub("1", "deggen", alice, handleT0), sub("2", "deggen", alice, handleT0), sub("3", "other", bob, handleT0)} {
			if err := s.BindSubscription(ctx, x, nil); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.ListSubscriptionsByHandle(ctx, "deggen")
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, x := range got {
			ids = append(ids, x.StoreID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"1", "2"}) {
			t.Fatalf("ids = %v, want [1 2]", ids)
		}
	})

	t.Run("ListLapsed", func(t *testing.T) {
		s := newStore(t)
		for i, d := range []time.Duration{3, 1, 2, 10} {
			x := sub(string(rune('a'+i)), "h"+string(rune('a'+i)), alice, handleT0.Add(d*time.Hour))
			if err := s.BindSubscription(ctx, x, nil); err != nil {
				t.Fatal(err)
			}
		}
		// b (1h) is already released, so the sweep never sees it again.
		if err := s.MarkSubscriptionReleased(ctx, storage.SubscriptionID(storage.StoreAppStore, "b"), handleT0); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListLapsedSubscriptions(ctx, handleT0.Add(5*time.Hour), 10)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, x := range got {
			ids = append(ids, x.StoreID)
		}
		if !slices.Equal(ids, []string{"c", "a"}) {
			t.Fatalf("lapsed = %v, want [c a] (earliest expiry first)", ids)
		}
		if got, _ := s.ListLapsedSubscriptions(ctx, handleT0.Add(5*time.Hour), 1); len(got) != 1 || got[0].StoreID != "c" {
			t.Fatalf("limit 1 = %+v", got)
		}
	})
}
