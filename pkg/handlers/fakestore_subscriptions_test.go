package handlers

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// The fake's subscription store, under the same mutex as everything else.

func copySub(s *storage.Subscription) *storage.Subscription {
	if s == nil {
		return nil
	}
	c := *s
	c.ReleasedAt = copyTime(s.ReleasedAt)
	return &c
}

func (f *fakeStore) GetSubscription(_ context.Context, id string) (*storage.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copySub(f.subs[id]), nil
}

func (f *fakeStore) BindSubscription(_ context.Context, sub storage.Subscription, prev *storage.Subscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.subs[sub.ID]
	if prev == nil {
		if cur != nil {
			return storage.ErrSubscriptionRaced
		}
		f.subs[sub.ID] = copySub(&sub)
		f.subs[sub.ID].ReleasedAt = nil
		return nil
	}
	if cur == nil || cur.Handle != prev.Handle || cur.IdentityKey != prev.IdentityKey {
		return storage.ErrSubscriptionRaced
	}
	next := copySub(&sub)
	next.CreatedAt, next.ReleasedAt = cur.CreatedAt, nil
	f.subs[sub.ID] = next
	return nil
}

func (f *fakeStore) UpdateSubscriptionStatus(_ context.Context, id string, st storage.SubscriptionStatus, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.subs[id]
	if cur == nil {
		return storage.ErrSubscriptionNotFound
	}
	cur.SubscriptionStatus, cur.UpdatedAt = st, now
	return nil
}

func (f *fakeStore) MarkSubscriptionReleased(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur := f.subs[id]
	if cur == nil {
		return storage.ErrSubscriptionNotFound
	}
	cur.ReleasedAt, cur.UpdatedAt = &at, at
	return nil
}

func (f *fakeStore) ListSubscriptionsByHandle(_ context.Context, handle string) ([]storage.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []storage.Subscription{}
	for _, s := range f.subs {
		if s.Handle == handle {
			out = append(out, *copySub(s))
		}
	}
	return out, nil
}

func (f *fakeStore) ListLapsedSubscriptions(_ context.Context, before time.Time, limit int) ([]storage.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []storage.Subscription{}
	for _, s := range f.subs {
		if s.ReleasedAt == nil && s.ExpiresAt.Before(before) {
			out = append(out, *copySub(s))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ExpiresAt.Equal(out[j].ExpiresAt) {
			return out[i].ExpiresAt.Before(out[j].ExpiresAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit <= 0 {
		return []storage.Subscription{}, nil
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

var _ storage.SubscriptionStore = (*fakeStore)(nil)

// The fake must keep the contract the real backend does, or the handler tests
// built on it prove nothing.
func TestFakeSubscriptionStoreConformance(t *testing.T) {
	storagetest.RunSubscriptionStoreTests(t, func(t *testing.T) storage.SubscriptionStore { return newFakeStore() })
}
