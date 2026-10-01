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
	handlesColl     = "handles"
	countersColl    = "counters"
)

// Store is a MongoDB-backed storage.Store.
type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

// opTimeout bounds every operation the driver performs. Without it the driver
// has no per-operation deadline at all: socket deadlines come only from the
// context, so a server that accepts a connection and then stops answering
// parks the caller for good.
const opTimeout = 10 * time.Second

// New connects to MongoDB and verifies the connection.
func New(ctx context.Context, uri, database string) (*Store, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetTimeout(opTimeout))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mongo: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to ping mongo: %w", err)
	}
	return &Store{client: client, db: client.Database(database)}, nil
}

// Close disconnects the client. Disconnect runs endSessions on the wire first,
// so it gets a deadline of its own: this is on the shutdown path, where a
// stalled server must not stop the process exiting.
func (s *Store) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return s.client.Disconnect(ctx)
}

// now returns the current time at the precision BSON can store, so a value
// written here compares equal to the one read back.
func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// EnsureSchema creates the indexes and seeds the default delivery fees.
func (s *Store) EnsureSchema(ctx context.Context) error {
	indexes := map[string][]mongo.IndexModel{
		messagesColl: {
			// ListMessages: equality on recipient and messageBox, then the sort.
			{Keys: bson.D{{Key: "recipient", Value: 1}, {Key: "messageBox", Value: 1}, {Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}},
		},
		permissionsColl: {
			// Uniqueness, including the box-wide row: Mongo indexes null as a
			// value, so this covers a nil sender too.
			{
				Keys:    bson.D{{Key: "recipient", Value: 1}, {Key: "sender", Value: 1}, {Key: "messageBox", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
			// ListPermissions sorts on messageBox, sender, createdAt after an
			// equality match on recipient. The sort keys have to appear in that
			// order after the equality prefix or the whole page is sorted in
			// memory, and one index per sort direction is needed because a
			// compound index is only walkable forwards or fully reversed.
			{Keys: bson.D{{Key: "recipient", Value: 1}, {Key: "messageBox", Value: 1}, {Key: "sender", Value: 1}, {Key: "createdAt", Value: 1}}},
			{Keys: bson.D{{Key: "recipient", Value: 1}, {Key: "messageBox", Value: 1}, {Key: "sender", Value: 1}, {Key: "createdAt", Value: -1}}},
		},
		devicesColl: {
			// Serves both device queries: ListDevices matches identityKey and
			// sorts on updatedAt, and ListActiveDevices adds an equality on
			// active, which a later index field does not disturb. Putting active
			// between the two would push ListDevices into an in-memory sort.
			{Keys: bson.D{{Key: "identityKey", Value: 1}, {Key: "updatedAt", Value: -1}, {Key: "active", Value: 1}}},
			// A registration is one identity's hold on one token, and one install
			// runs several identities on one token, so neither is unique alone.
			// Partial, because a document written by a replica of the previous
			// release has no fcmToken until the next boot backfills it, and
			// indexing the missing field as null would let only one such
			// document per identity exist: a second device registered through an
			// old replica mid-deploy would fail on a duplicate key.
			{
				Keys: bson.D{{Key: "identityKey", Value: 1}, {Key: "fcmToken", Value: 1}},
				Options: options.Index().SetUnique(true).
					SetPartialFilterExpression(bson.D{{Key: "fcmToken", Value: bson.D{{Key: "$type", Value: "string"}}}}),
			},
			// DeactivateDevice and UpdateDeviceLastUsed look a token up on its
			// own, on every push; the compound indexes above lead with identityKey.
			{Keys: bson.D{{Key: "fcmToken", Value: 1}}},
		},
		handlesColl: {
			// Look-alike handles collide here, which is what makes a claim's
			// similarity check a unique key violation rather than a read. A
			// released row keeps its skeleton, so the reservation outlives the
			// owner. The handle itself needs no index: it is the _id.
			{Keys: bson.D{{Key: "skeleton", Value: 1}}, Options: options.Index().SetUnique(true)},
			// One handle per key, and the reverse lookup's query. Partial,
			// because a released row has no identityKey at all and any number of
			// those must coexist — a plain unique index would index the missing
			// field as null and let only one row be released at a time.
			{
				Keys: bson.D{{Key: "identityKey", Value: 1}},
				Options: options.Index().SetUnique(true).
					SetPartialFilterExpression(bson.D{{Key: "identityKey", Value: bson.D{{Key: "$type", Value: "string"}}}}),
			},
		},
	}

	// Before the indexes: the unique one cannot be built while an identity's
	// documents all still lack fcmToken.
	if err := s.backfillDeviceTokens(ctx); err != nil {
		return err
	}

	for coll, models := range indexes {
		if _, err := s.db.Collection(coll).Indexes().CreateMany(ctx, models); err != nil {
			return fmt.Errorf("failed to create indexes on %s: %w", coll, err)
		}
	}

	// Seed the default fees without clobbering values an operator has changed,
	// matching the SQL migration's ON CONFLICT DO NOTHING.
	for _, f := range storage.DefaultDeliveryFees() {
		_, err := s.db.Collection(feesColl).UpdateOne(ctx,
			bson.M{"_id": f.MessageBox},
			bson.M{"$setOnInsert": bson.M{"deliveryFee": f.Fee}},
			options.UpdateOne().SetUpsert(true),
		)
		if err != nil {
			return fmt.Errorf("failed to seed delivery fee for %s: %w", f.MessageBox, err)
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

// PageMessages implements storage.MessagePager. The existing
// (recipient, messageBox, createdAt, _id) index covers the equality prefix and
// the full sort; a MessageID filter is an equality on _id, the collection's
// globally unique key, so it narrows to at most one document however the
// planner chooses to use it.
func (s *Store) PageMessages(ctx context.Context, q storage.MessagePageQuery) ([]storage.Message, error) {
	if q.FetchLimit <= 0 {
		// The MongoDB driver's options.Find().SetLimit(n) treats n == 0 (and any
		// negative n) as "no limit" rather than "no rows" — the opposite of SQL's
		// LIMIT 0, which sqlstore's PageMessages relies on for the same input.
		// MessagePager's contract only promises FetchLimit is "always at least 1"
		// from POST /listMessages's own caller, but it is an exported extension
		// point other code can call directly, so guard the out-of-contract input
		// explicitly rather than let this backend alone return every row.
		return nil, nil
	}

	filter := bson.M{"recipient": q.Recipient, "messageBox": q.MessageBox}
	if q.MessageID != nil {
		filter["_id"] = *q.MessageID
	}

	cur, err := s.db.Collection(messagesColl).Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).
			SetSkip(int64(q.Offset)).
			SetLimit(int64(q.FetchLimit)),
	)
	if err != nil {
		return nil, err
	}

	var docs []messageDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}

	msgs := make([]storage.Message, len(docs))
	for i, d := range docs {
		msgs[i] = storage.Message{
			MessageID: d.MessageID,
			Sender:    d.Sender,
			Body:      d.Body,
			CreatedAt: d.CreatedAt,
			UpdatedAt: d.UpdatedAt,
		}
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
	upsert := func() error {
		_, err := s.db.Collection(permissionsColl).UpdateOne(ctx,
			permissionKey(recipient, sender, messageBox), update, options.UpdateOne().SetUpsert(true))
		return err
	}

	err := upsert()
	if mongo.IsDuplicateKeyError(err) {
		// Two upserts raced to insert the same key; the row now exists, so the
		// same statement succeeds as a plain update. MongoDB 4.2+ retries this
		// server-side and never surfaces the error; wire-compatible servers
		// that are not MongoDB may.
		err = upsert()
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
	if err := q.Validate(); err != nil {
		return storage.PermissionPage{}, err
	}

	filter := bson.M{"recipient": q.Recipient}
	if q.MessageBox != nil {
		filter["messageBox"] = *q.MessageBox
	}

	coll := s.db.Collection(permissionsColl)

	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return storage.PermissionPage{}, err
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
	// DocID is the document key. A document written by the previous release
	// keys on the token alone and keeps that key for life; one written since
	// keys on deviceDocID, because the same token now belongs to one document
	// per identity.
	DocID string `bson:"_id"`
	// FCMToken is the token proper. Documents of the previous release lack it
	// until EnsureSchema backfills it from the key.
	FCMToken    string     `bson:"fcmToken"`
	IdentityKey string     `bson:"identityKey"`
	DeviceID    *string    `bson:"deviceId"`
	Platform    *string    `bson:"platform"`
	Active      bool       `bson:"active"`
	CreatedAt   time.Time  `bson:"createdAt"`
	UpdatedAt   time.Time  `bson:"updatedAt"`
	LastUsed    *time.Time `bson:"lastUsed"`
	// ID is the numeric ID storage.DeviceStore reports. The document key is not
	// numeric, so it comes from a counter; documents written before it existed
	// lack it until their token next registers.
	ID int64 `bson:"registrationId,omitempty"`
}

// token is the document's FCM token. A document still waiting for the backfill
// reads as its key, which is the token for exactly those documents, so a
// replica of the previous release writing mid-deploy does not produce a device
// with an empty token.
func (d deviceDoc) token() string {
	if d.FCMToken != "" {
		return d.FCMToken
	}
	return d.DocID
}

// deviceDocID is the key of a new registration. An identity key is a fixed-width
// hex string and never contains the separator, so the pair maps to the key one
// to one.
//
// The previous release reads the key as the token. While its replicas still
// serve, one that delivers to a recipient holding a document with this key sends
// to a token FCM rejects, then deactivates that document. Replace replicas
// rather than overlapping them; see the README's upgrade notes.
func deviceDocID(identityKey, fcmToken string) string {
	return identityKey + "|" + fcmToken
}

// backfillDeviceTokens gives every document of the previous release its fcmToken,
// copied from the key it was stored under. It is one update per document with an
// in-server pipeline, so it is atomic per document and safe to run from several
// replicas at once, and it is a no-op once nothing lacks the field. A document
// a still-old replica writes mid-deploy is picked up by the next boot.
//
// That mid-deploy overlap can also leave an old-shape document and a new one for
// the same (identityKey, fcmToken): an old replica and a new one both took the
// registration. Giving the old one its fcmToken then collides with the unique
// index. A boot that failed on that could not be recovered without editing the
// database, so the collision is resolved instead: the document that already
// holds the pair is the registration made through this release, and the
// old-shape duplicate is dropped.
func (s *Store) backfillDeviceTokens(ctx context.Context) error {
	coll := s.db.Collection(devicesColl)
	lacking := bson.M{"fcmToken": bson.M{"$exists": false}}
	fromID := mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "fcmToken", Value: "$_id"}}}}}

	_, err := coll.UpdateMany(ctx, lacking, fromID)
	if err == nil {
		return nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("failed to backfill device fcmToken: %w", err)
	}

	// Some document collides, and UpdateMany stopped at it. Take the rest one at
	// a time, so the collision can be told from the documents that backfill.
	cur, err := coll.Find(ctx, lacking, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return fmt.Errorf("failed to list devices to backfill: %w", err)
	}
	var left []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &left); err != nil {
		return fmt.Errorf("failed to list devices to backfill: %w", err)
	}
	for _, d := range left {
		// Still without fcmToken, so a replica that got there first is not undone.
		byDoc := bson.M{"_id": d.ID, "fcmToken": bson.M{"$exists": false}}
		_, err := coll.UpdateOne(ctx, byDoc, fromID)
		if mongo.IsDuplicateKeyError(err) {
			_, err = coll.DeleteOne(ctx, byDoc)
		}
		if err != nil {
			return fmt.Errorf("failed to backfill device %q: %w", d.ID, err)
		}
	}
	return nil
}

// nextSeq allocates the next value of the named counter, starting at 1.
func (s *Store) nextSeq(ctx context.Context, name string) (int64, error) {
	var counter struct {
		Seq int64 `bson:"seq"`
	}
	err := s.db.Collection(countersColl).FindOneAndUpdate(ctx,
		bson.M{"_id": name},
		bson.M{"$inc": bson.M{"seq": int64(1)}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&counter)
	return counter.Seq, err
}

// RegisterDevice implements storage.DeviceStore. It upserts on (identityKey,
// fcmToken), so another identity's document for the same token is never
// matched. It allocates an ID up front and keeps it only if the pair is new, so
// a re-registration leaves a gap in the sequence; IDs need to be unique and
// stable, not dense.
func (s *Store) RegisterDevice(ctx context.Context, d storage.NewDevice) (int64, error) {
	seq, err := s.nextSeq(ctx, devicesColl)
	if err != nil {
		return 0, err
	}

	ts := now()
	coll := s.db.Collection(devicesColl)
	byPair := bson.M{"identityKey": d.IdentityKey, "fcmToken": d.FCMToken}
	idOnly := bson.M{"registrationId": 1}
	update := bson.M{
		"$set": bson.M{
			"deviceId":  d.DeviceID,
			"platform":  d.Platform,
			"active":    true,
			"updatedAt": ts,
			"lastUsed":  ts,
		},
		// identityKey and fcmToken come from the filter on insert.
		"$setOnInsert": bson.M{
			"_id":            deviceDocID(d.IdentityKey, d.FCMToken),
			"createdAt":      ts,
			"registrationId": seq,
		},
	}

	var doc deviceDoc
	// Two first registrations of one pair can race to insert, and the loser
	// fails with a duplicate key on _id, which the server does not retry for the
	// caller. The document exists by then, so one retry is a plain update.
	for attempt := 0; ; attempt++ {
		err = coll.FindOneAndUpdate(ctx, byPair, update,
			options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After).SetProjection(idOnly),
		).Decode(&doc)
		if err == nil || attempt > 0 || !mongo.IsDuplicateKeyError(err) {
			break
		}
	}
	if err != nil {
		return 0, err
	}
	if doc.ID > 0 {
		return doc.ID, nil
	}

	// A document from before registrations had IDs. Fill it in only if it is
	// still absent, so concurrent re-registrations agree on one, then read
	// back whichever landed.
	byDoc := bson.M{"_id": doc.DocID}
	if _, err := coll.UpdateOne(ctx,
		bson.M{"_id": doc.DocID, "registrationId": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"registrationId": seq}},
	); err != nil {
		return 0, err
	}
	if err := coll.FindOne(ctx, byDoc, options.FindOne().SetProjection(idOnly)).Decode(&doc); err != nil {
		return 0, err
	}
	return doc.ID, nil
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
			ID:          d.ID,
			IdentityKey: d.IdentityKey,
			FCMToken:    d.token(),
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

// UpdateDeviceLastUsed implements storage.DeviceStore. It is keyed by token
// alone, so it stamps every identity's document for it.
func (s *Store) UpdateDeviceLastUsed(ctx context.Context, fcmToken string) error {
	ts := now()
	_, err := s.db.Collection(devicesColl).UpdateMany(ctx,
		bson.M{"fcmToken": fcmToken},
		bson.M{"$set": bson.M{"lastUsed": ts, "updatedAt": ts}},
	)
	return err
}

// DeactivateDevice implements storage.DeviceStore. It is keyed by token alone,
// so it deactivates every identity's document for it: FCM reported the token
// dead, not one identity's use of it.
func (s *Store) DeactivateDevice(ctx context.Context, fcmToken string) error {
	_, err := s.db.Collection(devicesColl).UpdateMany(ctx,
		bson.M{"fcmToken": fcmToken},
		bson.M{"$set": bson.M{"active": false, "updatedAt": now()}},
	)
	return err
}

// UnregisterDevice implements storage.DeviceStore. Both fields are in the
// filter: the token alone would delete every identity's document for it.
func (s *Store) UnregisterDevice(ctx context.Context, identityKey, fcmToken string) error {
	_, err := s.db.Collection(devicesColl).DeleteMany(ctx,
		bson.M{"identityKey": identityKey, "fcmToken": fcmToken},
	)
	return err
}

// compile-time assertion that Store satisfies the contract.
var _ storage.Store = (*Store)(nil)

// compile-time assertion that Store also implements the optional pager.
var _ storage.MessagePager = (*Store)(nil)
