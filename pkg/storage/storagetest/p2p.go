package storagetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// NewP2PDestinationStoreFunc returns a clean, empty destination store. Called
// once per subtest.
type NewP2PDestinationStoreFunc func(t *testing.T) storage.P2PDestinationStore

var p2pT0 = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

func p2pDest(ref string) storage.P2PDestination {
	return storage.P2PDestination{
		Reference:        ref,
		Handle:           "deggen",
		IdentityKey:      "02" + "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11",
		Script:           "76a914000000000000000000000000000000000000000088ac",
		Satoshis:         1000,
		DerivationPrefix: "cHJlZml4",
		DerivationSuffix: "c3VmZml4",
		CreatedAt:        p2pT0,
		AcceptUntil:      p2pT0.Add(24 * time.Hour),
		PurgeAt:          p2pT0.Add(storage.P2PUnpaidRetention),
	}
}

// RunP2PDestinationTests runs the P2PDestinationStore contract against a backend.
func RunP2PDestinationTests(t *testing.T, newStore NewP2PDestinationStoreFunc) {
	t.Helper()
	ctx := context.Background()
	const ref = "0123456789abcdef0123456789abcdef"
	const tx1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const tx2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	t.Run("CreateThenGet", func(t *testing.T) {
		s := newStore(t)
		want := p2pDest(ref)
		if err := s.CreateDestination(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDestination(ctx, ref, p2pT0.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		// Millisecond precision: that is all a Mongo datetime keeps.
		if got.Reference != want.Reference || got.Handle != want.Handle || got.IdentityKey != want.IdentityKey ||
			got.Script != want.Script || got.Satoshis != want.Satoshis ||
			got.DerivationPrefix != want.DerivationPrefix || got.DerivationSuffix != want.DerivationSuffix ||
			!got.CreatedAt.Truncate(time.Millisecond).Equal(want.CreatedAt.Truncate(time.Millisecond)) ||
			!got.AcceptUntil.Truncate(time.Millisecond).Equal(want.AcceptUntil.Truncate(time.Millisecond)) ||
			!got.PurgeAt.Truncate(time.Millisecond).Equal(want.PurgeAt.Truncate(time.Millisecond)) ||
			got.UsedTxID != "" {
			t.Fatalf("read back %+v, want %+v", got, want)
		}
	})

	t.Run("GetUnknown", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.GetDestination(ctx, "nope", p2pT0); !errors.Is(err, storage.ErrDestinationNotFound) {
			t.Fatalf("err = %v, want ErrDestinationNotFound", err)
		}
	})

	t.Run("GetAfterAcceptUntil", func(t *testing.T) {
		s := newStore(t)
		d := p2pDest(ref)
		if err := s.CreateDestination(ctx, d); err != nil {
			t.Fatal(err)
		}
		late := d.AcceptUntil.Add(time.Millisecond)
		if _, err := s.GetDestination(ctx, ref, late); !errors.Is(err, storage.ErrDestinationNotFound) {
			t.Fatalf("unused past the window: err = %v, want ErrDestinationNotFound", err)
		}
		if err := s.MarkDestinationUsed(ctx, ref, tx1, d.AcceptUntil.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDestination(ctx, ref, late)
		if err != nil || got.UsedTxID != tx1 {
			t.Fatalf("used past the window = %+v, %v; a replay has to find it", got, err)
		}
	})

	t.Run("MarkUsedMovesPurgeAt", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateDestination(ctx, p2pDest(ref)); err != nil {
			t.Fatal(err)
		}
		now := p2pT0.Add(2 * time.Hour)
		if err := s.MarkDestinationUsed(ctx, ref, tx1, now); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDestination(ctx, ref, now)
		if err != nil {
			t.Fatal(err)
		}
		want := now.Add(30 * 24 * time.Hour)
		if !got.PurgeAt.Truncate(time.Millisecond).Equal(want.Truncate(time.Millisecond)) {
			t.Fatalf("PurgeAt = %v, want %v", got.PurgeAt, want)
		}
	})

	t.Run("MarkUsedOnce", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateDestination(ctx, p2pDest(ref)); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDestinationUsed(ctx, ref, tx1, p2pT0); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDestination(ctx, ref, p2pT0)
		if err != nil || got.UsedTxID != tx1 {
			t.Fatalf("got %+v, %v; want UsedTxID %s", got, err, tx1)
		}
	})

	t.Run("MarkUsedSameTxidTwice", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateDestination(ctx, p2pDest(ref)); err != nil {
			t.Fatal(err)
		}
		for i := range 2 {
			if err := s.MarkDestinationUsed(ctx, ref, tx1, p2pT0); err != nil {
				t.Fatalf("call %d: %v", i+1, err)
			}
		}
	})

	t.Run("MarkUsedOtherTxid", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateDestination(ctx, p2pDest(ref)); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDestinationUsed(ctx, ref, tx1, p2pT0); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDestinationUsed(ctx, ref, tx2, p2pT0); !errors.Is(err, storage.ErrDestinationUsed) {
			t.Fatalf("err = %v, want ErrDestinationUsed", err)
		}
		got, err := s.GetDestination(ctx, ref, p2pT0)
		if err != nil || got.UsedTxID != tx1 {
			t.Fatalf("got %+v, %v; UsedTxID must stay %s", got, err, tx1)
		}
	})

	t.Run("ConcurrentMarkUsed", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateDestination(ctx, p2pDest(ref)); err != nil {
			t.Fatal(err)
		}
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, tx := range []string{tx1, tx2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = s.MarkDestinationUsed(ctx, ref, tx, p2pT0)
			}()
		}
		wg.Wait()
		var ok, used int
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, storage.ErrDestinationUsed):
				used++
			default:
				t.Fatalf("unexpected error %v", err)
			}
		}
		if ok != 1 || used != 1 {
			t.Fatalf("results %v, want exactly one nil and one ErrDestinationUsed", errs)
		}
	})

	t.Run("ExtendRetention", func(t *testing.T) {
		s := newStore(t)
		d := p2pDest(ref)
		if err := s.CreateDestination(ctx, d); err != nil {
			t.Fatal(err)
		}
		purgeAt := func() time.Time {
			got, err := s.GetDestination(ctx, ref, p2pT0)
			if err != nil {
				t.Fatal(err)
			}
			return got.PurgeAt.Truncate(time.Millisecond)
		}
		// Extends.
		later := d.PurgeAt.Add(10 * 24 * time.Hour)
		if err := s.ExtendDestinationRetention(ctx, ref, later); err != nil {
			t.Fatal(err)
		}
		if got := purgeAt(); !got.Equal(later) {
			t.Fatalf("PurgeAt = %v, want %v", got, later)
		}
		// Never shortens.
		if err := s.ExtendDestinationRetention(ctx, ref, later.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got := purgeAt(); !got.Equal(later) {
			t.Fatalf("PurgeAt = %v after a shorter extend, want %v", got, later)
		}
	})

	t.Run("ExtendRetentionAfterOtherTxid", func(t *testing.T) {
		// The case it exists for: a second payment on a used destination.
		s := newStore(t)
		if err := s.CreateDestination(ctx, p2pDest(ref)); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkDestinationUsed(ctx, ref, tx1, p2pT0); err != nil {
			t.Fatal(err)
		}
		day29 := p2pT0.Add(29 * 24 * time.Hour)
		if err := s.MarkDestinationUsed(ctx, ref, tx2, day29); !errors.Is(err, storage.ErrDestinationUsed) {
			t.Fatalf("err = %v, want ErrDestinationUsed", err)
		}
		want := day29.Add(storage.P2PDestinationRetention)
		if err := s.ExtendDestinationRetention(ctx, ref, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDestination(ctx, ref, day29)
		if err != nil || !got.PurgeAt.Truncate(time.Millisecond).Equal(want.Truncate(time.Millisecond)) {
			t.Fatalf("got %+v, %v; want PurgeAt %v", got, err, want)
		}
	})

	t.Run("ExtendRetentionUnknown", func(t *testing.T) {
		s := newStore(t)
		if err := s.ExtendDestinationRetention(ctx, "nope", p2pT0); !errors.Is(err, storage.ErrDestinationNotFound) {
			t.Fatalf("err = %v, want ErrDestinationNotFound", err)
		}
	})

	t.Run("MarkDelivered", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateDestination(ctx, p2pDest(ref)); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDestination(ctx, ref, p2pT0)
		if err != nil || len(got.DeliveredTxIDs) != 0 {
			t.Fatalf("fresh destination = %+v, %v; want nothing delivered", got, err)
		}
		// Twice, and a second txid: each is recorded once.
		for _, tx := range []string{tx1, tx1, tx2} {
			if err := s.MarkDestinationDelivered(ctx, ref, tx); err != nil {
				t.Fatal(err)
			}
		}
		got, err = s.GetDestination(ctx, ref, p2pT0)
		if err != nil || len(got.DeliveredTxIDs) != 2 || got.DeliveredTxIDs[0] != tx1 || got.DeliveredTxIDs[1] != tx2 {
			t.Fatalf("got %+v, %v; want delivered [%s %s]", got, err, tx1, tx2)
		}
	})

	t.Run("MarkDeliveredUnknown", func(t *testing.T) {
		s := newStore(t)
		if err := s.MarkDestinationDelivered(ctx, "nope", tx1); !errors.Is(err, storage.ErrDestinationNotFound) {
			t.Fatalf("err = %v, want ErrDestinationNotFound", err)
		}
	})
}
