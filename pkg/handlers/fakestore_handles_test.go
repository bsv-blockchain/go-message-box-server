package handlers

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// The fake's handle registry. A single mutex stands in for the unique indexes
// and conditional writes the real backends rely on: every claim and release
// decides and writes under one lock, so it is atomic in the same way.

// copyHandle deep-copies a record so a caller never shares a pointee with the
// row still in f.handles — the write paths may then update a field either way.
func copyHandle(r *storage.HandleRecord) *storage.HandleRecord {
	if r == nil {
		return nil
	}
	c := *r
	c.IdentityKey, c.Certificate, c.ReleasedBy = copyString(r.IdentityKey), copyString(r.Certificate), copyString(r.ReleasedBy)
	c.ReleasedAt, c.CooldownUntil = copyTime(r.ReleasedAt), copyTime(r.CooldownUntil)
	return &c
}

func (f *fakeStore) GetHandle(_ context.Context, handle string) (*storage.HandleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyHandle(f.handles[handle]), nil
}

func (f *fakeStore) GetHandleBySkeleton(_ context.Context, skeleton string) (*storage.HandleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, r := range f.handles {
		if r.Skeleton == skeleton {
			return copyHandle(r), nil
		}
	}
	return nil, nil
}

// skeletonTakenLocked reports whether a row other than handle already reserves
// skeleton — the unique index, standing in for which released rows still count.
// The caller holds f.mu.
func (f *fakeStore) skeletonTakenLocked(skeleton, handle string) bool {
	for _, r := range f.handles {
		if r.Handle != handle && r.Skeleton == skeleton {
			return true
		}
	}
	return false
}

// getByKeyLocked returns the key's active record. The caller holds f.mu.
func (f *fakeStore) getByKeyLocked(identityKey string) *storage.HandleRecord {
	for _, r := range f.handles {
		if r.IdentityKey != nil && *r.IdentityKey == identityKey {
			return r
		}
	}
	return nil
}

func (f *fakeStore) GetHandleByIdentityKey(_ context.Context, identityKey string) (*storage.HandleRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyHandle(f.getByKeyLocked(identityKey)), nil
}

// ClaimHandle retries a claim that lost to a concurrent release exactly as the
// Mongo backend does: tryClaim and the diagnosis take the lock separately, so
// the row a write was refused for can be gone by the time the reason is read.
func (f *fakeStore) ClaimHandle(ctx context.Context, c storage.HandleClaim) (storage.ClaimResult, error) {
	res, err := f.claimOnce(ctx, c)
	if errors.Is(err, storage.ErrClaimRaced) {
		return f.claimOnce(ctx, c)
	}
	return res, err
}

func (f *fakeStore) claimOnce(ctx context.Context, c storage.HandleClaim) (storage.ClaimResult, error) {
	if res, ok := f.tryClaim(c); ok {
		return res, nil
	}
	return storage.DiagnoseClaim(ctx, f, c, nil)
}

// tryClaim is the write half: owner update, reclaim of a released row, insert.
// It reports false without writing when none applies, leaving the reason to
// DiagnoseClaim — which is exactly how a real backend's conditional writes fail.
func (f *fakeStore) tryClaim(c storage.HandleClaim) (storage.ClaimResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key, cert := c.IdentityKey, c.Certificate
	rec := f.handles[c.Handle]

	if rec != nil && rec.IdentityKey != nil {
		if *rec.IdentityKey == c.IdentityKey && rec.IssuedAt.Before(c.IssuedAt) && rec.SerialNumber != c.SerialNumber {
			rec.Certificate, rec.SerialNumber, rec.IssuedAt, rec.UpdatedAt = &cert, c.SerialNumber, c.IssuedAt, f.tick()
			return storage.ClaimUpdated, true
		}
		return 0, false
	}
	if f.getByKeyLocked(c.IdentityKey) != nil {
		return 0, false
	}
	if rec != nil {
		// Only a handle its owner gave up lets that owner back in early; after an
		// operator release the removed key waits like everyone else.
		ownGiveUp := c.IdentityKey != "" && rec.LastIdentityKey == c.IdentityKey &&
			rec.ReleasedBy != nil && *rec.ReleasedBy == storage.ReleasedByOwner
		free := ownGiveUp || rec.CooldownUntil == nil || !rec.CooldownUntil.After(c.Now)
		// The reclaim writes the claim's skeleton, so it meets the unique index
		// exactly as an insert does.
		if !rec.IssuedAt.Before(c.IssuedAt) || !free || f.skeletonTakenLocked(c.Skeleton, c.Handle) {
			return 0, false
		}
		rec.Skeleton = c.Skeleton
		rec.IdentityKey, rec.LastIdentityKey, rec.Certificate = &key, key, &cert
		rec.SerialNumber, rec.IssuedAt, rec.UpdatedAt = c.SerialNumber, c.IssuedAt, f.tick()
		rec.ReleasedAt, rec.CooldownUntil, rec.ReleasedBy = nil, nil, nil
		return storage.ClaimCreated, true
	}
	if f.skeletonTakenLocked(c.Skeleton, c.Handle) {
		return 0, false
	}
	ts := f.tick()
	f.handles[c.Handle] = &storage.HandleRecord{
		Handle: c.Handle, Skeleton: c.Skeleton, IdentityKey: &key, LastIdentityKey: key,
		Certificate: &cert, SerialNumber: c.SerialNumber, IssuedAt: c.IssuedAt, CreatedAt: ts, UpdatedAt: ts,
	}
	return storage.ClaimCreated, true
}

func (f *fakeStore) ReleaseHandle(ctx context.Context, r storage.HandleRelease) error {
	if err := storage.ValidateRelease(r); err != nil {
		return err
	}
	if f.tryRelease(r) {
		return nil
	}
	return storage.DiagnoseRelease(ctx, f, r, nil)
}

// tryRelease is the conditional release write; see tryClaim.
func (f *fakeStore) tryRelease(r storage.HandleRelease) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	rec := f.handles[r.Handle]
	if rec == nil || rec.IdentityKey == nil {
		return false
	}
	if r.Owner != nil {
		if *rec.IdentityKey != *r.Owner || !rec.IssuedAt.Before(*r.IssuedAt) {
			return false
		}
		// The tombstone advances the replay guard; the row keeps it once released.
		rec.IssuedAt = *r.IssuedAt
	}
	now, by := r.Now, r.ReleasedBy
	rec.IdentityKey, rec.Certificate = nil, nil
	rec.ReleasedAt, rec.ReleasedBy, rec.CooldownUntil, rec.UpdatedAt = &now, &by, copyTime(r.CooldownUntil), f.tick()
	return true
}

// copyTime dereferences a caller's time pointer so the stored record does not
// alias memory the caller may still hold.
func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// copyString is copyTime for the record's optional strings.
func copyString(s *string) *string {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

func (f *fakeStore) FindHandles(_ context.Context, m storage.HandleMatch) ([]storage.HandleRecord, error) {
	out := []storage.HandleRecord{}
	if m.Limit <= 0 {
		return out, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, r := range f.handles {
		if r.IdentityKey == nil {
			continue
		}
		v := r.Handle
		if m.Field == storage.HandleFieldSkeleton {
			v = r.Skeleton
		}
		hit := strings.HasPrefix(v, m.Value)
		if m.Mode == storage.HandleMatchContains {
			hit = strings.Contains(v, m.Value)
		}
		if hit {
			out = append(out, *copyHandle(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	if len(out) > m.Limit {
		out = out[:m.Limit]
	}
	return out, nil
}

var _ storage.HandleStore = (*fakeStore)(nil)
