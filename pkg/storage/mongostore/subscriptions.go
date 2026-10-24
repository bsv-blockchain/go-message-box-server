package mongostore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// --- handle subscriptions ---------------------------------------------------

// subscriptionDoc is one store subscription. The subscription id is the _id,
// so one receipt is one row and an insert of a receipt already on file fails
// on the primary key rather than on a check-then-insert.
type subscriptionDoc struct {
	ID          string     `bson:"_id"`
	Store       string     `bson:"store"`
	StoreID     string     `bson:"storeId"`
	Handle      string     `bson:"handle"`
	IdentityKey string     `bson:"identityKey"`
	ProductID   string     `bson:"productId"`
	BasePlan    string     `bson:"basePlan,omitempty"`
	Tier        int        `bson:"tier"`
	State       string     `bson:"state"`
	ExpiresAt   time.Time  `bson:"expiresAt"`
	Environment string     `bson:"environment"`
	CreatedAt   time.Time  `bson:"createdAt"`
	UpdatedAt   time.Time  `bson:"updatedAt"`
	ReleasedAt  *time.Time `bson:"releasedAt,omitempty"`
}

func toSubscription(d subscriptionDoc) storage.Subscription {
	return storage.Subscription{
		ID: d.ID, Store: d.Store, StoreID: d.StoreID, Handle: d.Handle, IdentityKey: d.IdentityKey,
		SubscriptionStatus: storage.SubscriptionStatus{
			ProductID: d.ProductID, BasePlan: d.BasePlan, Tier: d.Tier, State: d.State,
			ExpiresAt: d.ExpiresAt.UTC(), Environment: d.Environment,
		},
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(), ReleasedAt: utcPtr(d.ReleasedAt),
	}
}

func statusFields(st storage.SubscriptionStatus) bson.M {
	return bson.M{
		"productId": st.ProductID, "basePlan": st.BasePlan, "tier": st.Tier, "state": st.State,
		"expiresAt": msUTC(st.ExpiresAt), "environment": st.Environment,
	}
}

// GetSubscription implements storage.SubscriptionStore.
func (s *Store) GetSubscription(ctx context.Context, id string) (*storage.Subscription, error) {
	var d subscriptionDoc
	err := s.db.Collection(subscriptionsColl).FindOne(ctx, bson.M{"_id": id}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil //nolint:nilnil // SubscriptionStore contract: no row is (nil, nil)
	}
	if err != nil {
		return nil, err
	}
	sub := toSubscription(d)
	return &sub, nil
}

// BindSubscription implements storage.SubscriptionStore.
func (s *Store) BindSubscription(ctx context.Context, sub storage.Subscription, prev *storage.Subscription) error {
	coll := s.db.Collection(subscriptionsColl)
	if prev == nil {
		_, err := coll.InsertOne(ctx, subscriptionDoc{
			ID: sub.ID, Store: sub.Store, StoreID: sub.StoreID, Handle: sub.Handle, IdentityKey: sub.IdentityKey,
			ProductID: sub.ProductID, BasePlan: sub.BasePlan, Tier: sub.Tier, State: sub.State,
			ExpiresAt: msUTC(sub.ExpiresAt), Environment: sub.Environment,
			CreatedAt: msUTC(sub.CreatedAt), UpdatedAt: msUTC(sub.UpdatedAt),
		})
		if mongo.IsDuplicateKeyError(err) {
			return storage.ErrSubscriptionRaced
		}
		if err != nil {
			return fmt.Errorf("bind subscription %s: %w", sub.ID, err)
		}
		return nil
	}
	set := statusFields(sub.SubscriptionStatus)
	set["handle"], set["identityKey"], set["updatedAt"] = sub.Handle, sub.IdentityKey, msUTC(sub.UpdatedAt)
	res, err := coll.UpdateOne(ctx,
		bson.M{"_id": sub.ID, "handle": prev.Handle, "identityKey": prev.IdentityKey},
		bson.M{opSet: set, "$unset": bson.M{"releasedAt": ""}},
	)
	if err != nil {
		return fmt.Errorf("bind subscription %s: %w", sub.ID, err)
	}
	if res.MatchedCount == 0 {
		return storage.ErrSubscriptionRaced
	}
	return nil
}

// UpdateSubscriptionStatus implements storage.SubscriptionStore.
func (s *Store) UpdateSubscriptionStatus(ctx context.Context, id string, st storage.SubscriptionStatus, now time.Time) error {
	set := statusFields(st)
	set["updatedAt"] = msUTC(now)
	return s.updateSubscription(ctx, id, bson.M{opSet: set})
}

// MarkSubscriptionReleased implements storage.SubscriptionStore.
func (s *Store) MarkSubscriptionReleased(ctx context.Context, id string, at time.Time) error {
	return s.updateSubscription(ctx, id, bson.M{opSet: bson.M{"releasedAt": msUTC(at), "updatedAt": msUTC(at)}})
}

func (s *Store) updateSubscription(ctx context.Context, id string, update bson.M) error {
	res, err := s.db.Collection(subscriptionsColl).UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		return fmt.Errorf("update subscription %s: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return storage.ErrSubscriptionNotFound
	}
	return nil
}

func (s *Store) findSubscriptions(ctx context.Context, filter bson.M, opts ...options.Lister[options.FindOptions]) ([]storage.Subscription, error) {
	cur, err := s.db.Collection(subscriptionsColl).Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	var docs []subscriptionDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]storage.Subscription, 0, len(docs))
	for _, d := range docs {
		out = append(out, toSubscription(d))
	}
	return out, nil
}

// ListSubscriptionsByHandle implements storage.SubscriptionStore.
func (s *Store) ListSubscriptionsByHandle(ctx context.Context, handle string) ([]storage.Subscription, error) {
	return s.findSubscriptions(ctx, bson.M{"handle": handle})
}

// ListLapsedSubscriptions implements storage.SubscriptionStore.
func (s *Store) ListLapsedSubscriptions(ctx context.Context, before time.Time, limit int) ([]storage.Subscription, error) {
	// SetLimit(0) is "no limit" to the driver; a caller asking for nothing gets nothing.
	if limit <= 0 {
		return []storage.Subscription{}, nil
	}
	return s.findSubscriptions(ctx,
		bson.M{"expiresAt": bson.M{"$lt": msUTC(before)}, "releasedAt": bson.M{"$exists": false}},
		options.Find().SetSort(bson.D{{Key: "expiresAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)),
	)
}

// compile-time assertion that Store satisfies the optional subscription contract.
var _ storage.SubscriptionStore = (*Store)(nil)
