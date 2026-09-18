package mongostore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// --- handles ----------------------------------------------------------------

// handleDoc is one registry row. The handle is the _id, so the first claim of a
// handle wins on the primary key without a check-then-insert. A released row
// has no identityKey and no certificate field at all — unset, not null — so
// that the partial unique index skips it and the $exists filters below can use
// "has an identityKey" as the definition of active.
type handleDoc struct {
	Handle          string     `bson:"_id"`
	Skeleton        string     `bson:"skeleton"`
	IdentityKey     *string    `bson:"identityKey,omitempty"`
	LastIdentityKey string     `bson:"lastIdentityKey"`
	Certificate     *string    `bson:"certificate,omitempty"`
	SerialNumber    string     `bson:"serialNumber"`
	IssuedAt        time.Time  `bson:"issuedAt"`
	CreatedAt       time.Time  `bson:"createdAt"`
	UpdatedAt       time.Time  `bson:"updatedAt"`
	ReleasedAt      *time.Time `bson:"releasedAt,omitempty"`
	CooldownUntil   *time.Time `bson:"cooldownUntil,omitempty"`
	ReleasedBy      *string    `bson:"releasedBy,omitempty"`
}

// msUTC is the resolution the registry actually stores. A BSON datetime holds
// whole milliseconds, so a finer time would come back as an instant the caller
// never passed and the claim's exact IssuedAt comparisons — the replay check in
// particular — would stop matching the row they just wrote. The contract states
// this as a precondition; normalising costs one call and does not rely on it.
func msUTC(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// utcPtr moves an optional stored timestamp to UTC. The driver decodes BSON
// datetimes into the local zone, and the contract's times are UTC.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func toHandle(d handleDoc) storage.HandleRecord {
	return storage.HandleRecord{
		Handle:          d.Handle,
		Skeleton:        d.Skeleton,
		IdentityKey:     d.IdentityKey,
		LastIdentityKey: d.LastIdentityKey,
		Certificate:     d.Certificate,
		SerialNumber:    d.SerialNumber,
		IssuedAt:        d.IssuedAt.UTC(),
		CreatedAt:       d.CreatedAt.UTC(),
		UpdatedAt:       d.UpdatedAt.UTC(),
		ReleasedAt:      utcPtr(d.ReleasedAt),
		CooldownUntil:   utcPtr(d.CooldownUntil),
		ReleasedBy:      d.ReleasedBy,
	}
}

func (s *Store) getHandleWhere(ctx context.Context, filter bson.M) (*storage.HandleRecord, error) {
	var d handleDoc
	err := s.db.Collection(handlesColl).FindOne(ctx, filter).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := toHandle(d)
	return &r, nil
}

// GetHandle implements storage.HandleReader.
func (s *Store) GetHandle(ctx context.Context, handle string) (*storage.HandleRecord, error) {
	return s.getHandleWhere(ctx, bson.M{"_id": handle})
}

// GetHandleBySkeleton implements storage.HandleReader.
func (s *Store) GetHandleBySkeleton(ctx context.Context, skeleton string) (*storage.HandleRecord, error) {
	return s.getHandleWhere(ctx, bson.M{"skeleton": skeleton})
}

// GetHandleByIdentityKey implements storage.HandleReader. A released row has no
// identityKey field, so matching the key at all is what makes this active-only.
func (s *Store) GetHandleByIdentityKey(ctx context.Context, identityKey string) (*storage.HandleRecord, error) {
	return s.getHandleWhere(ctx, bson.M{"identityKey": identityKey})
}

// claimWriteErr splits the two failures a claim write can have. A duplicate key
// is the registry refusing, which is the only signal the contract turns into a
// conflict, so it goes to the diagnosis. Anything else — a timeout, a dropped
// reply, a stepdown — is the database not answering, and re-reading the row to
// call that ErrHandleTaken or ErrStaleCertificate would report a conflict that
// nothing rejected, for a write that may well have applied.
func (s *Store) claimWriteErr(ctx context.Context, c storage.HandleClaim, err error) (storage.ClaimResult, error) {
	if !mongo.IsDuplicateKeyError(err) {
		return 0, fmt.Errorf("claim %s: %w", c.Handle, err)
	}
	return storage.DiagnoseClaim(ctx, s, c, err)
}

// ClaimHandle implements storage.HandleStore.
//
// A claim that lost to a concurrent release is retried once: the row its insert
// collided with was gone again by the time the diagnosis read it, so there is
// nothing to report and the second attempt sees the settled row. The claim
// never applied, so the retry cannot double-write.
func (s *Store) ClaimHandle(ctx context.Context, c storage.HandleClaim) (storage.ClaimResult, error) {
	res, err := s.claimOnce(ctx, c)
	if errors.Is(err, storage.ErrClaimRaced) {
		return s.claimOnce(ctx, c)
	}
	return res, err
}

// claimOnce is one pass of the claim: owner update, reclaim, insert — one
// conditional write each, with the unique indexes deciding every race. None of
// them can half-apply, so whatever the registry ends up in is a state some
// caller asked for; a write the registry rejects hands the explaining to
// storage.DiagnoseClaim rather than reading first and deciding.
func (s *Store) claimOnce(ctx context.Context, c storage.HandleClaim) (storage.ClaimResult, error) {
	coll := s.db.Collection(handlesColl)
	ts := now()
	// c is a copy, so this pins the times the writes and the diagnosis below
	// both use to what the row can hold, without rewriting the caller's claim.
	c.IssuedAt, c.Now = msUTC(c.IssuedAt), msUTC(c.Now)

	// The owner replaces their certificate. Both guards matter: a certificate
	// must be strictly newer, and re-presenting the stored serial under a new
	// issuedAt is a replay, not an update.
	res, err := coll.UpdateOne(ctx,
		bson.M{
			"_id":          c.Handle,
			"identityKey":  c.IdentityKey,
			"issuedAt":     bson.M{"$lt": c.IssuedAt},
			"serialNumber": bson.M{"$ne": c.SerialNumber},
		},
		bson.M{"$set": bson.M{
			"certificate": c.Certificate, "serialNumber": c.SerialNumber,
			"issuedAt": c.IssuedAt, "updatedAt": ts,
		}},
	)
	if err != nil {
		return s.claimWriteErr(ctx, c, err)
	}
	if res.MatchedCount > 0 {
		return storage.ClaimUpdated, nil
	}

	// A released row is reclaimed by the key that released it, or by anyone once
	// the cooldown has run out — at cooldownUntil itself, not a tick later. The
	// reclaim stores the claim's skeleton, not the one the row was created with,
	// so it meets the look-alike constraint exactly like an insert does.
	free := bson.A{
		bson.M{"cooldownUntil": bson.M{"$exists": false}},
		bson.M{"cooldownUntil": bson.M{"$lte": c.Now}},
	}
	if c.IdentityKey != "" {
		// Only a handle its owner gave up lets that owner back in early. After an
		// operator release the removed key waits with everyone else, so a cooldown
		// an operator asks for is not a reservation for the key they removed.
		free = append(bson.A{bson.M{"lastIdentityKey": c.IdentityKey, "releasedBy": storage.ReleasedByOwner}}, free...)
	}
	res, err = coll.UpdateOne(ctx,
		bson.M{
			"_id":         c.Handle,
			"identityKey": bson.M{"$exists": false},
			"issuedAt":    bson.M{"$lt": c.IssuedAt},
			"$or":         free,
		},
		bson.M{
			"$set": bson.M{
				"skeleton": c.Skeleton, "identityKey": c.IdentityKey, "lastIdentityKey": c.IdentityKey,
				"certificate": c.Certificate, "serialNumber": c.SerialNumber,
				"issuedAt": c.IssuedAt, "updatedAt": ts,
			},
			"$unset": bson.M{"releasedAt": "", "cooldownUntil": "", "releasedBy": ""},
		},
	)
	if err != nil {
		return s.claimWriteErr(ctx, c, err)
	}
	if res.MatchedCount > 0 {
		return storage.ClaimCreated, nil
	}

	// Nothing to update: this is a first claim, and the three unique keys (_id,
	// skeleton, identityKey) reject it if it is not.
	key, cert := c.IdentityKey, c.Certificate
	if _, err := coll.InsertOne(ctx, handleDoc{
		Handle: c.Handle, Skeleton: c.Skeleton, IdentityKey: &key, LastIdentityKey: key,
		Certificate: &cert, SerialNumber: c.SerialNumber,
		IssuedAt: c.IssuedAt, CreatedAt: ts, UpdatedAt: ts,
	}); err != nil {
		return s.claimWriteErr(ctx, c, err)
	}
	return storage.ClaimCreated, nil
}

// ReleaseHandle implements storage.HandleStore. The row is kept: its issuedAt
// is the replay guard for the next claim and its skeleton stays reserved.
//
// lastIdentityKey keeps naming the key that just lost the handle. Paired with
// releasedBy that is what lets a key back into the cooldown of a handle it gave
// up itself, while leaving an operator's cooldown binding on everyone.
func (s *Store) ReleaseHandle(ctx context.Context, r storage.HandleRelease) error {
	if err := storage.ValidateRelease(r); err != nil {
		return err
	}
	// The times the caller points at stay the caller's; only these copies are
	// cut down to what the row stores — including the copy the diagnosis below
	// compares against, or a sub-millisecond tombstone would never be
	// recognised as the one already applied.
	r.Now = msUTC(r.Now)
	filter := bson.M{"_id": r.Handle, "identityKey": bson.M{"$exists": true}}
	set := bson.M{"updatedAt": now(), "releasedAt": r.Now, "releasedBy": r.ReleasedBy}
	unset := bson.M{"identityKey": "", "certificate": ""}

	if r.Owner != nil {
		// The tombstone is itself a certificate: it must come from the owner and
		// be newer than the one on the row, and it becomes the value a later
		// claim is compared against.
		issuedAt := msUTC(*r.IssuedAt)
		r.IssuedAt = &issuedAt
		filter["identityKey"] = *r.Owner
		filter["issuedAt"] = bson.M{"$lt": issuedAt}
		set["issuedAt"] = issuedAt
	}
	if r.CooldownUntil != nil {
		set["cooldownUntil"] = msUTC(*r.CooldownUntil)
	} else {
		// An operator release carries no cooldown, and must clear any left by an
		// earlier one rather than leave the handle parked.
		unset["cooldownUntil"] = ""
	}

	res, err := s.db.Collection(handlesColl).UpdateOne(ctx, filter, bson.M{"$set": set, "$unset": unset})
	if err != nil {
		// Nothing here can collide with a unique key, so a driver error is the
		// database failing rather than the registry refusing. Diagnosing it would
		// answer ErrHandleNotFound for a tombstone that may have applied.
		return fmt.Errorf("release %s: %w", r.Handle, err)
	}
	if res.MatchedCount == 0 {
		return storage.DiagnoseRelease(ctx, s, r, nil)
	}
	return nil
}

// FindHandles implements storage.HandleStore.
func (s *Store) FindHandles(ctx context.Context, m storage.HandleMatch) ([]storage.HandleRecord, error) {
	// A limit that asks for nothing is not an invitation to return everything.
	if m.Limit <= 0 {
		return []storage.HandleRecord{}, nil
	}

	field := "_id"
	if m.Field == storage.HandleFieldSkeleton {
		field = "skeleton"
	}
	// The value is a searcher's text. QuoteMeta makes every character of it a
	// literal, so "de*" is a handle fragment that matches nothing rather than a
	// pattern matching every handle starting with "d", and "a(b[" is not a
	// syntax error the server has to reject.
	pattern := regexp.QuoteMeta(m.Value)
	// Anything but an explicit substring search is anchored, so a caller that
	// leaves Mode at its zero value gets the narrower search on every backend
	// rather than a prefix here and a substring there.
	if m.Mode != storage.HandleMatchContains {
		pattern = "^" + pattern
	}

	cur, err := s.db.Collection(handlesColl).Find(ctx,
		bson.M{"identityKey": bson.M{"$exists": true}, field: bson.M{"$regex": pattern}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(m.Limit)),
	)
	if err != nil {
		return nil, err
	}

	var docs []handleDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}

	out := make([]storage.HandleRecord, 0, len(docs))
	for _, d := range docs {
		out = append(out, toHandle(d))
	}
	return out, nil
}

// compile-time assertion that Store satisfies the optional handle contract.
var _ storage.HandleStore = (*Store)(nil)
