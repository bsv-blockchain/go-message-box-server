package storage

import (
	"context"
	"errors"
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
)

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

// HandleRelease releases a handle. With Owner set it is the owner's tombstone
// and IssuedAt must be newer than the stored value; with Owner nil it is an
// operator release. Times arrive UTC truncated to milliseconds, as on a claim:
// the tombstone's IssuedAt is the value a later claim is compared against.
type HandleRelease struct {
	Handle        string
	Owner         *string
	IssuedAt      *time.Time
	ReleasedBy    string
	CooldownUntil *time.Time
	Now           time.Time
}

// HandleField names the column a search matches against.
type HandleField int

// The searchable fields: the handle itself, and its look-alike skeleton.
const (
	HandleFieldHandle HandleField = iota + 1
	HandleFieldSkeleton
)

// HandleMatchMode says where in the field the value must appear.
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

	// ClaimHandle registers, updates or reclaims. A created or reclaimed row
	// holds c.Skeleton, so a reclaim meets the look-alike constraint like an
	// insert and reports ErrHandleTooSimilar when another row holds it. Times
	// arrive UTC truncated to milliseconds and are compared exactly. Errors:
	// ErrHandleTaken, ErrHandleTooSimilar, ErrKeyHasHandle, ErrHandleCooldown,
	// ErrStaleCertificate.
	ClaimHandle(ctx context.Context, c HandleClaim) (ClaimResult, error)

	// ReleaseHandle clears IdentityKey and Certificate and records the release.
	// Errors: ErrHandleNotFound (no active row), ErrHandleTaken (Owner is not
	// the owner), ErrStaleCertificate.
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
			if rec.SerialNumber == c.SerialNumber && rec.IssuedAt.Equal(c.IssuedAt) {
				return ClaimUnchanged, nil
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
	if rec != nil && rec.CooldownUntil != nil && rec.CooldownUntil.After(c.Now) && rec.LastIdentityKey != c.IdentityKey {
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
	if cause == nil {
		cause = errors.New("claim applied nowhere and the registry shows no conflict")
	}
	return 0, cause
}

// DiagnoseRelease explains why a release write matched nothing.
func DiagnoseRelease(ctx context.Context, r HandleReader, rel HandleRelease, cause error) error {
	rec, err := r.GetHandle(ctx, rel.Handle)
	if err != nil {
		return err
	}
	if rec == nil || !rec.Active() {
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
