package mongostore

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// p2pDestinationDoc is one issued destination; the reference is the _id.
type p2pDestinationDoc struct {
	Reference        string    `bson:"_id"`
	Handle           string    `bson:"handle"`
	IdentityKey      string    `bson:"identityKey"`
	Script           string    `bson:"script"`
	Satoshis         uint64    `bson:"satoshis"`
	DerivationPrefix string    `bson:"derivationPrefix"`
	DerivationSuffix string    `bson:"derivationSuffix"`
	CreatedAt        time.Time `bson:"createdAt"`
	AcceptUntil      time.Time `bson:"acceptUntil"`
	PurgeAt          time.Time `bson:"purgeAt"`
	// UsedTxID is always written, empty until a payment lands, so the claim in
	// MarkDestinationUsed can match on the empty string.
	UsedTxID       string   `bson:"usedTxId"`
	DeliveredTxIDs []string `bson:"deliveredTxIds,omitempty"`
}

func (d p2pDestinationDoc) toDestination() *storage.P2PDestination {
	return &storage.P2PDestination{
		Reference: d.Reference, Handle: d.Handle, IdentityKey: d.IdentityKey, Script: d.Script,
		Satoshis: d.Satoshis, DerivationPrefix: d.DerivationPrefix, DerivationSuffix: d.DerivationSuffix,
		CreatedAt: d.CreatedAt.UTC(), AcceptUntil: d.AcceptUntil.UTC(), PurgeAt: d.PurgeAt.UTC(),
		UsedTxID: d.UsedTxID, DeliveredTxIDs: d.DeliveredTxIDs,
	}
}

// CreateDestination implements storage.P2PDestinationStore.
func (s *Store) CreateDestination(ctx context.Context, d storage.P2PDestination) error {
	_, err := s.db.Collection(p2pDestinationsColl).InsertOne(ctx, p2pDestinationDoc{
		Reference: d.Reference, Handle: d.Handle, IdentityKey: d.IdentityKey, Script: d.Script,
		Satoshis: d.Satoshis, DerivationPrefix: d.DerivationPrefix, DerivationSuffix: d.DerivationSuffix,
		CreatedAt: msUTC(d.CreatedAt), AcceptUntil: msUTC(d.AcceptUntil), PurgeAt: msUTC(d.PurgeAt),
		UsedTxID: d.UsedTxID,
	})
	return err
}

// GetDestination implements storage.P2PDestinationStore.
func (s *Store) GetDestination(ctx context.Context, reference string, now time.Time) (*storage.P2PDestination, error) {
	var doc p2pDestinationDoc
	err := s.db.Collection(p2pDestinationsColl).FindOne(ctx, bson.M{fieldID: reference}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, storage.ErrDestinationNotFound
	}
	if err != nil {
		return nil, err
	}
	if doc.UsedTxID == "" && now.After(doc.AcceptUntil) {
		return nil, storage.ErrDestinationNotFound
	}
	return doc.toDestination(), nil
}

// MarkDestinationUsed implements storage.P2PDestinationStore.
func (s *Store) MarkDestinationUsed(ctx context.Context, reference, txid string, now time.Time) error {
	coll := s.db.Collection(p2pDestinationsColl)
	// One conditional write is the claim, so two racing transactions cannot
	// both win.
	res := coll.FindOneAndUpdate(ctx,
		bson.M{fieldID: reference, "usedTxId": ""},
		bson.M{"$set": bson.M{"usedTxId": txid, "purgeAt": msUTC(now.Add(storage.P2PDestinationRetention))}},
		options.FindOneAndUpdate().SetProjection(bson.M{fieldID: 1}),
	)
	err := res.Err()
	if err == nil {
		return nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}
	var doc p2pDestinationDoc
	err = coll.FindOne(ctx, bson.M{fieldID: reference}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return storage.ErrDestinationNotFound
	}
	if err != nil {
		return err
	}
	if doc.UsedTxID == txid {
		return nil
	}
	return storage.ErrDestinationUsed
}

// MarkDestinationDelivered implements storage.P2PDestinationStore.
func (s *Store) MarkDestinationDelivered(ctx context.Context, reference, txid string) error {
	res, err := s.db.Collection(p2pDestinationsColl).UpdateOne(ctx,
		bson.M{fieldID: reference}, bson.M{"$addToSet": bson.M{"deliveredTxIds": txid}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return storage.ErrDestinationNotFound
	}
	return nil
}

var _ storage.P2PDestinationStore = (*Store)(nil)
