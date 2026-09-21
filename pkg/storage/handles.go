package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Errors returned by HandleStore. They are the vocabulary the claim and release
// diagnoses map a backend's "my write matched nothing" into.
var (
	ErrHandleTaken      = errors.New("handle taken")
	ErrHandleTooSimilar = errors.New("handle too similar to an existing handle")
	ErrKeyHasHandle     = errors.New("identity key already has a handle")
	ErrHandleCooldown   = errors.New("handle is in cooldown")
	ErrStaleCertificate = errors.New("certificate is not newer than the stored one")
	ErrHandleNotFound   = errors.New("handle not found")

	// ErrClaimRaced says a claim write was refused while the registry shows
	// nothing that would refuse it: the row the write collided with changed
	// between the write and the diagnosis. The claim never applied, so retrying
	// it against the settled row is safe — which is what a backend does before
	// ever returning this.
	ErrClaimRaced = errors.New("claim raced a concurrent write")

	// ErrInvalidRelease says the release itself is malformed, which is a caller
	// bug rather than anything the registry decided.
	ErrInvalidRelease = errors.New("invalid release request")
)

// ReleasedByOwner is the ReleasedBy value of an owner's own tombstone, as
// opposed to an operator release, which records the admin's identity key. Only
// a row released by its owner lets that owner back in during the cooldown.
const ReleasedByOwner = "owner"

// HandleRecord is one row of the handle registry. Rows are never deleted: a
// released handle keeps its IssuedAt (the replay guard) and its Skeleton.
type HandleRecord struct {
	Handle          string
	Skeleton        string
	IdentityKey     *string
	LastIdentityKey string
	Certificate     *string
	SerialNumber    string
	IssuedAt        time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ReleasedAt      *time.Time
	CooldownUntil   *time.Time
	ReleasedBy      *string
}

// Active reports whether the handle currently belongs to a key. A nil record —
// what the readers return for a handle nobody has ever claimed — is not active.
func (r *HandleRecord) Active() bool { return r != nil && r.IdentityKey != nil }

// HandleClaim asks for Handle on behalf of IdentityKey. Callers pass times
// already in UTC truncated to milliseconds, because that is all a backend
// stores and the claim compares IssuedAt exactly.
type HandleClaim struct {
	Handle       string
	Skeleton     string
	IdentityKey  string
	Certificate  string
	SerialNumber string
	IssuedAt     time.Time
	Now          time.Time
}

// ClaimResult says what a successful ClaimHandle did.
type ClaimResult int

// The outcomes of a successful claim: a handle newly bound to the key, a fresh
// certificate on a handle the key already held, or a resubmission of the stored
// certificate.
const (
	ClaimCreated ClaimResult = iota + 1
	ClaimUpdated
	ClaimUnchanged
)

// HandleRelease releases a handle. With Owner set it is the owner's tombstone,
// IssuedAt must be newer than the stored value and ReleasedBy must be
// ReleasedByOwner; with Owner nil it is an operator release and ReleasedBy is
// the admin's identity key. Times arrive UTC truncated to milliseconds, as on a
// claim: the tombstone's IssuedAt is the value a later claim is compared
// against.
type HandleRelease struct {
	Handle        string
	Owner         *string
	IssuedAt      *time.Time
	ReleasedBy    string
	CooldownUntil *time.Time
	Now           time.Time
}

// ValidateRelease rejects a release no backend can carry out, so that both
// answer a caller's mistake the same way instead of each inventing a conflict
// for it.
func ValidateRelease(r HandleRelease) error {
	if r.Owner == nil {
		return nil
	}
	if r.IssuedAt == nil {
		return fmt.Errorf("%w: owner release of %s without IssuedAt", ErrInvalidRelease, r.Handle)
	}
	// The reclaim rule reads ReleasedBy to tell a handle its owner gave up from
	// one an operator took away, so the two fields cannot be allowed to
	// disagree: an owner tombstone filed under another name would quietly cost
	// that owner the cooldown it is entitled to.
	if r.ReleasedBy != ReleasedByOwner {
		return fmt.Errorf("%w: owner release of %s recorded as %q, want %q", ErrInvalidRelease, r.Handle, r.ReleasedBy, ReleasedByOwner)
	}
	return nil
}

// cooldownExempt reports whether identityKey may reclaim rec while its cooldown
// is still running. Only the key that released the handle itself is let back
// in: after an operator release the removed key is a stranger like any other,
// or the cooldown an operator asks for would park the handle against everyone
// except the key they were removing.
func cooldownExempt(rec *HandleRecord, identityKey string) bool {
	return identityKey != "" && rec.LastIdentityKey == identityKey &&
		rec.ReleasedBy != nil && *rec.ReleasedBy == ReleasedByOwner
}

// HandleField names the column a search matches against.
type HandleField int

// The searchable fields: the handle itself, and its look-alike skeleton.
const (
	HandleFieldHandle HandleField = iota + 1
	HandleFieldSkeleton
)

// HandleMatchMode says where in the field the value must appear. The zero value
// is not a mode: backends read anything but HandleMatchContains as the narrower
// anchored prefix, so a caller that forgets it gets fewer rows rather than a
// different search on each backend.
type HandleMatchMode int

// The two search tiers: anchored prefix, and unanchored substring.
const (
	HandleMatchPrefix HandleMatchMode = iota + 1
	HandleMatchContains
)

// HandleMatch selects active handles whose Field matches Value literally (no
// wildcards are interpreted).
type HandleMatch struct {
	Field HandleField
	Mode  HandleMatchMode
	Value string

	// Limit is the greatest number of records to return. It must be greater
	// than zero: a backend returns no records for anything else, so a caller
	// that forgets it gets nothing rather than the whole collection.
	Limit int
}

// HandleReader is the read side, split out so the diagnosis helpers can run
// against any backend.
type HandleReader interface {
	// GetHandle returns the record in any state, or (nil, nil).
	GetHandle(ctx context.Context, handle string) (*HandleRecord, error)

	// GetHandleBySkeleton returns the record in any state, or (nil, nil).
	GetHandleBySkeleton(ctx context.Context, skeleton string) (*HandleRecord, error)

	// GetHandleByIdentityKey returns the key's active record, or (nil, nil).
	GetHandleByIdentityKey(ctx context.Context, identityKey string) (*HandleRecord, error)
}

// HandleStore is the handle registry. It is a standalone contract, not part of
// Store: only backends that can enforce its uniqueness rules implement it.
//
// ClaimHandle must be safe to race: of any number of concurrent claims whose
// Handle, Skeleton or IdentityKey collide, at most one is Created. Backends get
// that from unique keys and single conditional writes, never from
// check-then-insert. The order is: owner update, reclaim of a released row,
// insert; whatever fails falls through to DiagnoseClaim.
type HandleStore interface {
	HandleReader

	// ClaimHandle registers, updates or reclaims. Every row it writes holds
	// c.Skeleton, not the one the row was created with, so an update and a
	// reclaim meet the look-alike constraint exactly like an insert and report
	// ErrHandleTooSimilar when another row holds it. Times arrive UTC truncated
	// to milliseconds and are compared exactly. Errors:
	// ErrHandleTaken, ErrHandleTooSimilar, ErrKeyHasHandle, ErrHandleCooldown,
	// ErrStaleCertificate.
	ClaimHandle(ctx context.Context, c HandleClaim) (ClaimResult, error)

	// ReleaseHandle clears IdentityKey and Certificate and records the release.
	// Replaying the owner tombstone a row already carries is a no-op, so a
	// client whose response was lost may retry. Errors: ErrHandleNotFound (no
	// active row), ErrHandleTaken (Owner is not the owner), ErrStaleCertificate,
	// ErrInvalidRelease.
	ReleaseHandle(ctx context.Context, r HandleRelease) error

	// FindHandles returns active records ordered by Handle ascending. No
	// matches returns an empty, non-nil slice.
	FindHandles(ctx context.Context, m HandleMatch) ([]HandleRecord, error)
}

// DiagnoseClaim explains why none of a backend's claim writes applied. cause is
// the backend error, if any, returned when the registry offers no explanation.
func DiagnoseClaim(ctx context.Context, r HandleReader, c HandleClaim, cause error) (ClaimResult, error) {
	rec, err := r.GetHandle(ctx, c.Handle)
	if err != nil {
		return 0, err
	}
	if rec != nil {
		if rec.Active() {
			if *rec.IdentityKey != c.IdentityKey {
				return 0, ErrHandleTaken
			}
			// A no-op is the stored certificate arriving again, which means the
			// document too: the same serial and issuedAt over a different body is
			// a new certificate reusing a serial it must not reuse, and answering
			// it with success would report an edit the registry did not keep.
			if rec.SerialNumber == c.SerialNumber && rec.IssuedAt.Equal(c.IssuedAt) &&
				rec.Certificate != nil && *rec.Certificate == c.Certificate {
				return ClaimUnchanged, nil
			}
			// The certificate is strictly newer and reuses no serial, so the
			// owner update's own guards accepted it and what refused the write
			// was the skeleton it stores. A fold-table change can fold a handle
			// onto a skeleton another row already reserves; calling that a stale
			// certificate would send the owner back for a newer one, which is
			// the one thing that cannot fix it.
			if c.IssuedAt.After(rec.IssuedAt) && c.SerialNumber != rec.SerialNumber {
				sim, err := r.GetHandleBySkeleton(ctx, c.Skeleton)
				if err != nil {
					return 0, err
				}
				if sim != nil && sim.Handle != c.Handle {
					return 0, ErrHandleTooSimilar
				}
				// The row as it stands would take this very certificate and no
				// other row holds the skeleton, so the collision the write lost to
				// is already gone — the row that held it moved off between the two.
				// Calling that stale would be a conflict no certificate can clear;
				// the claim never applied, so the caller retries instead.
				return 0, claimRaced(cause)
			}
			return 0, ErrStaleCertificate
		}
		if !c.IssuedAt.After(rec.IssuedAt) {
			return 0, ErrStaleCertificate
		}
	}
	other, err := r.GetHandleByIdentityKey(ctx, c.IdentityKey)
	if err != nil {
		return 0, err
	}
	if other != nil {
		return 0, ErrKeyHasHandle
	}
	// The row exists, is released, the certificate is newer and the key is free,
	// so the cooldown is the one remaining reason the reclaim can have been
	// refused — but only when it really is running against this caller. Blaming
	// it unconditionally would dress any other divergence in a backend's reclaim
	// filter up as a plausible-looking cooldown.
	if rec != nil && rec.CooldownUntil != nil && rec.CooldownUntil.After(c.Now) && !cooldownExempt(rec, c.IdentityKey) {
		return 0, ErrHandleCooldown
	}
	sim, err := r.GetHandleBySkeleton(ctx, c.Skeleton)
	if err != nil {
		return 0, err
	}
	// The claimed handle's own row does not make it too similar to itself.
	if sim != nil && sim.Handle != c.Handle {
		return 0, ErrHandleTooSimilar
	}
	// Nothing in the registry refuses this claim, so the row the write collided
	// with is already gone — a release that landed between the two. The caller
	// retries rather than reporting a fault for a claim that would now apply.
	return 0, claimRaced(cause)
}

// claimRaced wraps the backend's error, if it gave one, in ErrClaimRaced: the
// write was refused and the registry read afterwards no longer explains it.
func claimRaced(cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: the registry shows no conflict", ErrClaimRaced)
	}
	return fmt.Errorf("%w: %w", ErrClaimRaced, cause)
}

// DiagnoseRelease explains why a release write matched nothing.
func DiagnoseRelease(ctx context.Context, r HandleReader, rel HandleRelease, cause error) error {
	rec, err := r.GetHandle(ctx, rel.Handle)
	if err != nil {
		return err
	}
	if rec == nil {
		return ErrHandleNotFound
	}
	if !rec.Active() {
		// A tombstone is a certificate, and the stored one arriving again is the
		// same no-op a replayed claim is: the row already says exactly what this
		// release asks for, so a client whose response was lost may retry.
		if releaseAlreadyApplied(rec, rel) {
			return nil
		}
		return ErrHandleNotFound
	}
	if rel.Owner != nil && *rec.IdentityKey != *rel.Owner {
		return ErrHandleTaken
	}
	// The owner is right and the row is active, so the tombstone's issuedAt is
	// what the write rejected.
	if rel.Owner != nil {
		return ErrStaleCertificate
	}
	if cause == nil {
		cause = errors.New("release applied nowhere")
	}
	return cause
}

// releaseAlreadyApplied reports whether rel is the very tombstone that put rec
// in its current state. Only an owner tombstone can be recognised: it leaves
// lastIdentityKey naming its owner and issuedAt holding its own value, a pair
// no other write produces. An operator release carries neither, so replaying
// one stays a 404.
func releaseAlreadyApplied(rec *HandleRecord, rel HandleRelease) bool {
	return rel.Owner != nil && rel.IssuedAt != nil &&
		rel.ReleasedBy == ReleasedByOwner &&
		rec.ReleasedBy != nil && *rec.ReleasedBy == ReleasedByOwner &&
		rec.LastIdentityKey == *rel.Owner &&
		// Both sides at the resolution a row stores, so that a tombstone finer
		// than a millisecond is still recognised as the one already applied.
		rec.IssuedAt.UTC().Truncate(time.Millisecond).Equal(rel.IssuedAt.UTC().Truncate(time.Millisecond))
}
