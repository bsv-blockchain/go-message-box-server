// Package mongostore implements storage.Store on top of MongoDB.
//
// The document model differs from the SQL one in two ways that the storage
// contract permits: a message carries its box type as a plain string field, so
// there is no message box collection at all, and a message's _id is its
// messageId, so duplicate detection is a unique key violation rather than an
// upsert dance.
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

// Collection names.
const (
	messagesColl    = "messages"
	permissionsColl = "message_permissions"
	feesColl        = "server_fees"
	devicesColl     = "device_registrations"
)

// Store is a MongoDB-backed storage.Store.
type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

// New connects to MongoDB and verifies the connection.
func New(ctx context.Context, uri, database string) (*Store, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mongo: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to ping mongo: %w", err)
	}
	return &Store{client: client, db: client.Database(database)}, nil
}

// Close disconnects the client.
func (s *Store) Close() error { return s.client.Disconnect(context.Background()) }

// now returns the current time at the precision BSON can store, so a value
// written here compares equal to the one read back.
func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// EnsureSchema creates the indexes and seeds the default delivery fees.
func (s *Store) EnsureSchema(ctx context.Context) error {
	indexes := map[string][]mongo.IndexModel{
		messagesColl: {
			{Keys: bson.D{{Key: "recipient", Value: 1}, {Key: "messageBox", Value: 1}, {Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}},
		},
		permissionsColl: {
			{
				Keys:    bson.D{{Key: "recipient", Value: 1}, {Key: "sender", Value: 1}, {Key: "messageBox", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
			{Keys: bson.D{{Key: "recipient", Value: 1}, {Key: "messageBox", Value: 1}, {Key: "createdAt", Value: 1}}},
		},
		devicesColl: {
			{Keys: bson.D{{Key: "identityKey", Value: 1}, {Key: "active", Value: 1}, {Key: "updatedAt", Value: -1}}},
		},
	}

	for coll, models := range indexes {
		if _, err := s.db.Collection(coll).Indexes().CreateMany(ctx, models); err != nil {
			return fmt.Errorf("failed to create indexes on %s: %w", coll, err)
		}
	}

	// Seed the default fees without clobbering values an operator has changed,
	// matching the SQL migration's ON CONFLICT DO NOTHING.
	for box, fee := range map[string]int{"notifications": 10, "inbox": 0, "payment_inbox": 0} {
		_, err := s.db.Collection(feesColl).UpdateOne(ctx,
			bson.M{"_id": box},
			bson.M{"$setOnInsert": bson.M{"deliveryFee": fee}},
			options.UpdateOne().SetUpsert(true),
		)
		if err != nil {
			return fmt.Errorf("failed to seed delivery fee for %s: %w", box, err)
		}
	}
	return nil
}

// --- messages ---------------------------------------------------------------

type messageDoc struct {
	MessageID  string    `bson:"_id"`
	Recipient  string    `bson:"recipient"`
	MessageBox string    `bson:"messageBox"`
	Sender     string    `bson:"sender"`
	Body       string    `bson:"body"`
	CreatedAt  time.Time `bson:"createdAt"`
	UpdatedAt  time.Time `bson:"updatedAt"`
}

// InsertMessage implements storage.MessageStore.
func (s *Store) InsertMessage(ctx context.Context, m storage.NewMessage) error {
	ts := now()
	_, err := s.db.Collection(messagesColl).InsertOne(ctx, messageDoc{
		MessageID:  m.MessageID,
		Recipient:  m.Recipient,
		MessageBox: m.MessageBox,
		Sender:     m.Sender,
		Body:       m.Body,
		CreatedAt:  ts,
		UpdatedAt:  ts,
	})
	if mongo.IsDuplicateKeyError(err) {
		return storage.ErrDuplicateMessage
	}
	return err
}

// ListMessages implements storage.MessageStore.
func (s *Store) ListMessages(ctx context.Context, recipient, messageBox string) ([]storage.Message, error) {
	cur, err := s.db.Collection(messagesColl).Find(ctx,
		bson.M{"recipient": recipient, "messageBox": messageBox},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}

	var docs []messageDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}

	var msgs []storage.Message
	for _, d := range docs {
		msgs = append(msgs, storage.Message{
			MessageID: d.MessageID,
			Sender:    d.Sender,
			Body:      d.Body,
			CreatedAt: d.CreatedAt,
			UpdatedAt: d.UpdatedAt,
		})
	}
	return msgs, nil
}

// AcknowledgeMessages implements storage.MessageStore.
func (s *Store) AcknowledgeMessages(ctx context.Context, recipient string, messageIDs []string) (int64, error) {
	if len(messageIDs) == 0 {
		return 0, nil
	}
	res, err := s.db.Collection(messagesColl).DeleteMany(ctx,
		bson.M{"recipient": recipient, "_id": bson.M{"$in": messageIDs}},
	)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// --- fees -------------------------------------------------------------------

// GetServerDeliveryFee implements storage.FeeStore.
func (s *Store) GetServerDeliveryFee(ctx context.Context, messageBox string) (int, error) {
	var doc struct {
		DeliveryFee int `bson:"deliveryFee"`
	}
	err := s.db.Collection(feesColl).FindOne(ctx, bson.M{"_id": messageBox}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return doc.DeliveryFee, nil
}

// --- permissions ------------------------------------------------------------

type permissionDoc struct {
	Recipient    string    `bson:"recipient"`
	Sender       *string   `bson:"sender"`
	MessageBox   string    `bson:"messageBox"`
	RecipientFee int       `bson:"recipientFee"`
	CreatedAt    time.Time `bson:"createdAt"`
	UpdatedAt    time.Time `bson:"updatedAt"`
}

// permissionKey is the unique filter for one permission. A nil sender is stored
// as an explicit null, which Mongo indexes as a value — so unlike SQL there is
// no NULL != NULL problem to work around.
func permissionKey(recipient string, sender *string, messageBox string) bson.M {
	return bson.M{"recipient": recipient, "sender": sender, "messageBox": messageBox}
}

// SetPermission implements storage.PermissionStore.
func (s *Store) SetPermission(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error {
	ts := now()
	update := bson.M{
		"$set": bson.M{"recipientFee": recipientFee, "updatedAt": ts},
		"$setOnInsert": bson.M{
			"recipient": recipient, "sender": sender, "messageBox": messageBox, "createdAt": ts,
		},
	}
	opts := options.UpdateOne().SetUpsert(true)

	_, err := s.db.Collection(permissionsColl).UpdateOne(ctx, permissionKey(recipient, sender, messageBox), update, opts)
	if mongo.IsDuplicateKeyError(err) {
		// Two upserts raced to insert the same key; the row now exists, so the
		// same statement succeeds as a plain update.
		_, err = s.db.Collection(permissionsColl).UpdateOne(ctx, permissionKey(recipient, sender, messageBox), update, opts)
	}
	return err
}

// SetPermissionIfAbsent implements storage.PermissionStore. The unique index on
// (recipient, sender, messageBox) makes the upsert atomic, so a $setOnInsert-only
// update cannot touch an existing row.
func (s *Store) SetPermissionIfAbsent(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error {
	ts := now()
	_, err := s.db.Collection(permissionsColl).UpdateOne(ctx,
		permissionKey(recipient, sender, messageBox),
		bson.M{"$setOnInsert": bson.M{
			"recipient": recipient, "sender": sender, "messageBox": messageBox,
			"recipientFee": recipientFee, "createdAt": ts, "updatedAt": ts,
		}},
		options.UpdateOne().SetUpsert(true),
	)
	if mongo.IsDuplicateKeyError(err) {
		// Another caller inserted it first, which is the outcome this asked for.
		return nil
	}
	return err
}

// GetPermission implements storage.PermissionStore.
func (s *Store) GetPermission(ctx context.Context, recipient string, sender *string, messageBox string) (*storage.Permission, error) {
	var doc permissionDoc
	err := s.db.Collection(permissionsColl).FindOne(ctx, permissionKey(recipient, sender, messageBox)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := toPermission(doc)
	return &p, nil
}

func toPermission(d permissionDoc) storage.Permission {
	return storage.Permission{
		Recipient:    d.Recipient,
		Sender:       d.Sender,
		MessageBox:   d.MessageBox,
		RecipientFee: d.RecipientFee,
		CreatedAt:    d.CreatedAt,
		UpdatedAt:    d.UpdatedAt,
	}
}

// ListPermissions implements storage.PermissionStore.
func (s *Store) ListPermissions(ctx context.Context, q storage.PermissionQuery) (storage.PermissionPage, error) {
	filter := bson.M{"recipient": q.Recipient}
	if q.MessageBox != nil {
		filter["messageBox"] = *q.MessageBox
	}

	coll := s.db.Collection(permissionsColl)

	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return storage.PermissionPage{}, err
	}

	// MongoDB reads a limit of 0 as "unlimited", where SQL's LIMIT 0 returns
	// nothing. The contract follows SQL.
	if q.Limit <= 0 {
		return storage.PermissionPage{Total: int(total)}, nil
	}

	// BSON's type ordering already sorts null before strings, so sorting on
	// sender reproduces the SQL "box-wide row first" tie-break for free.
	createdAt := -1
	if q.Order == storage.SortAsc {
		createdAt = 1
	}
	sort := bson.D{
		{Key: "messageBox", Value: 1},
		{Key: "sender", Value: 1},
		{Key: "createdAt", Value: createdAt},
	}

	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(sort).
		SetSkip(int64(q.Offset)).
		SetLimit(int64(q.Limit)),
	)
	if err != nil {
		return storage.PermissionPage{}, err
	}

	var docs []permissionDoc
	if err := cur.All(ctx, &docs); err != nil {
		return storage.PermissionPage{}, err
	}

	page := storage.PermissionPage{Total: int(total)}
	for _, d := range docs {
		page.Items = append(page.Items, toPermission(d))
	}
	return page, nil
}

// --- devices ----------------------------------------------------------------

type deviceDoc struct {
	FCMToken    string     `bson:"_id"`
	IdentityKey string     `bson:"identityKey"`
	DeviceID    *string    `bson:"deviceId"`
	Platform    *string    `bson:"platform"`
	Active      bool       `bson:"active"`
	CreatedAt   time.Time  `bson:"createdAt"`
	UpdatedAt   time.Time  `bson:"updatedAt"`
	LastUsed    *time.Time `bson:"lastUsed"`
}

// RegisterDevice implements storage.DeviceStore.
func (s *Store) RegisterDevice(ctx context.Context, d storage.NewDevice) error {
	ts := now()
	_, err := s.db.Collection(devicesColl).UpdateOne(ctx,
		bson.M{"_id": d.FCMToken},
		bson.M{
			"$set": bson.M{
				"identityKey": d.IdentityKey,
				"deviceId":    d.DeviceID,
				"platform":    d.Platform,
				"active":      true,
				"updatedAt":   ts,
				"lastUsed":    ts,
			},
			"$setOnInsert": bson.M{"createdAt": ts},
		},
		options.UpdateOne().SetUpsert(true),
	)
	return err
}

// listDevices runs the shared device query with an optional active filter.
func (s *Store) listDevices(ctx context.Context, identityKey string, activeOnly bool) ([]storage.Device, error) {
	filter := bson.M{"identityKey": identityKey}
	if activeOnly {
		filter["active"] = true
	}

	cur, err := s.db.Collection(devicesColl).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}

	var docs []deviceDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}

	var devices []storage.Device
	for _, d := range docs {
		devices = append(devices, storage.Device{
			IdentityKey: d.IdentityKey,
			FCMToken:    d.FCMToken,
			DeviceID:    d.DeviceID,
			Platform:    d.Platform,
			Active:      d.Active,
			CreatedAt:   d.CreatedAt,
			UpdatedAt:   d.UpdatedAt,
			LastUsed:    d.LastUsed,
		})
	}
	return devices, nil
}

// ListDevices implements storage.DeviceStore.
func (s *Store) ListDevices(ctx context.Context, identityKey string) ([]storage.Device, error) {
	return s.listDevices(ctx, identityKey, false)
}

// ListActiveDevices implements storage.DeviceStore.
func (s *Store) ListActiveDevices(ctx context.Context, identityKey string) ([]storage.Device, error) {
	return s.listDevices(ctx, identityKey, true)
}

// UpdateDeviceLastUsed implements storage.DeviceStore.
func (s *Store) UpdateDeviceLastUsed(ctx context.Context, fcmToken string) error {
	ts := now()
	_, err := s.db.Collection(devicesColl).UpdateOne(ctx,
		bson.M{"_id": fcmToken},
		bson.M{"$set": bson.M{"lastUsed": ts, "updatedAt": ts}},
	)
	return err
}

// DeactivateDevice implements storage.DeviceStore.
func (s *Store) DeactivateDevice(ctx context.Context, fcmToken string) error {
	_, err := s.db.Collection(devicesColl).UpdateOne(ctx,
		bson.M{"_id": fcmToken},
		bson.M{"$set": bson.M{"active": false, "updatedAt": now()}},
	)
	return err
}

// compile-time assertion that Store satisfies the contract.
var _ storage.Store = (*Store)(nil)
