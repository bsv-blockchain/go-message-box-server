package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// The fake's paymail destination store, under the same mutex as everything else.

var errFakeDuplicate = errors.New("duplicate destination reference")

func (f *fakeStore) CreateDestination(_ context.Context, d storage.P2PDestination) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.p2p[d.Reference]; ok {
		return errFakeDuplicate
	}
	c := d
	f.p2p[d.Reference] = &c
	return nil
}

func (f *fakeStore) GetDestination(_ context.Context, reference string, now time.Time) (*storage.P2PDestination, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.p2p[reference]
	if d == nil || (d.UsedTxID == "" && now.After(d.AcceptUntil)) {
		return nil, storage.ErrDestinationNotFound
	}
	c := *d
	return &c, nil
}

func (f *fakeStore) MarkDestinationUsed(_ context.Context, reference, txid string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.p2p[reference]
	if d == nil {
		return storage.ErrDestinationNotFound
	}
	if d.UsedTxID == "" {
		d.UsedTxID = txid
		d.PurgeAt = now.Add(storage.P2PDestinationRetention)
		return nil
	}
	if d.UsedTxID == txid {
		return nil
	}
	return storage.ErrDestinationUsed
}

var _ storage.P2PDestinationStore = (*fakeStore)(nil)

// The fake must keep the contract the real backend does, or the handler tests
// built on it prove nothing.
func TestP2PFakeDestinationConformance(t *testing.T) {
	storagetest.RunP2PDestinationTests(t, func(_ *testing.T) storage.P2PDestinationStore { return newFakeStore() })
}
