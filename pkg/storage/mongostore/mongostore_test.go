package mongostore

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// newMongoBare returns a store against MONGO_TEST_URI with an empty database
// and no schema, for a test that has to write documents the way an older
// release did before the schema of this one is applied. It drops every
// collection first, so point it at a throwaway database.
func newMongoBare(t *testing.T, uri, database string) *Store {
	t.Helper()
	ctx := context.Background()

	s, err := New(ctx, uri, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.db.Drop(ctx); err != nil {
		t.Fatalf("drop database: %v", err)
	}
	return s
}

// newMongo returns newMongoBare's store with the schema applied.
func newMongo(t *testing.T, uri, database string) storage.Store {
	t.Helper()
	s := newMongoBare(t, uri, database)
	if err := s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConformance_Mongo(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	storagetest.RunStoreTests(t, func(t *testing.T) storage.Store {
		return newMongo(t, uri, database)
	})
}

// TestHandleConformance_Mongo runs the handle registry contract. HandleStore is
// not part of storage.Store, so the suite needs the concrete type back.
func TestHandleConformance_Mongo(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	storagetest.RunHandleStoreTests(t, func(t *testing.T) storage.HandleStore {
		return newMongo(t, uri, database).(*Store)
	})
}

// TestPageMessages_FetchLimitLessThanOne pins storage.MessagePager's
// documented contract for out-of-range FetchLimit: the MongoDB driver's
// options.Find().SetLimit(n) treats n == 0 (and negative n) as "no limit",
// the opposite of SQL's LIMIT 0, so PageMessages must special-case it rather
// than let a caller that (incorrectly) passes FetchLimit <= 0 get every
// message back instead of none.
func TestPageMessages_FetchLimitLessThanOne(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongo(t, uri, database)
	const recipient = "02fetchlimit"
	if err := s.InsertMessage(ctx, storage.NewMessage{
		MessageID: "m1", Recipient: recipient, MessageBox: "inbox", Sender: "02sender", Body: "hi",
	}); err != nil {
		t.Fatal(err)
	}

	pager := s.(storage.MessagePager)
	for _, fetchLimit := range []int{0, -1, -100} {
		got, err := pager.PageMessages(ctx, storage.MessagePageQuery{
			Recipient: recipient, MessageBox: "inbox", FetchLimit: fetchLimit,
		})
		if err != nil {
			t.Fatalf("FetchLimit %d: %v", fetchLimit, err)
		}
		if len(got) != 0 {
			t.Errorf("FetchLimit %d: got %d messages, want 0", fetchLimit, len(got))
		}
	}
}

// TestHandleTimesTruncateToMilliseconds pins what the contract only states: a
// BSON datetime holds milliseconds, so a finer IssuedAt would be stored as a
// different instant than the one passed and the owner's own resubmission would
// come back ErrStaleCertificate instead of ClaimUnchanged. The store truncates
// on the way in so the value it compares against is the value it stored — and
// does so on copies, because the release times arrive as pointers the caller
// still owns.
func TestHandleTimesTruncateToMilliseconds(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongo(t, uri, database).(*Store)

	const owner = "02alice"
	issued := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC).Add(500 * time.Microsecond)
	c := storage.HandleClaim{
		Handle: "deggen", Skeleton: "degen", IdentityKey: owner,
		Certificate: `{"serialNumber":"s1"}`, SerialNumber: "s1",
		IssuedAt: issued, Now: issued,
	}

	if got, err := s.ClaimHandle(ctx, c); err != nil || got != storage.ClaimCreated {
		t.Fatalf("ClaimHandle = %v, %v; want %v, nil", got, err, storage.ClaimCreated)
	}
	rec, err := s.GetHandle(ctx, "deggen")
	if err != nil || rec == nil {
		t.Fatalf("GetHandle = %v, %v", rec, err)
	}
	if want := issued.Truncate(time.Millisecond); !rec.IssuedAt.Equal(want) {
		t.Errorf("stored issuedAt = %v, want %v", rec.IssuedAt, want)
	}
	// The same certificate again is the one the row already holds.
	if got, err := s.ClaimHandle(ctx, c); err != nil || got != storage.ClaimUnchanged {
		t.Fatalf("replayed ClaimHandle = %v, %v; want %v, nil", got, err, storage.ClaimUnchanged)
	}

	key := owner
	relIssued := issued.Add(time.Hour)
	cooldown := relIssued.Add(24 * time.Hour)
	keptIssued, keptCooldown := relIssued, cooldown
	if err := s.ReleaseHandle(ctx, storage.HandleRelease{
		Handle: "deggen", Owner: &key, IssuedAt: &relIssued,
		ReleasedBy: "owner", CooldownUntil: &cooldown, Now: relIssued,
	}); err != nil {
		t.Fatalf("ReleaseHandle: %v", err)
	}
	if !relIssued.Equal(keptIssued) || !cooldown.Equal(keptCooldown) {
		t.Errorf("release rewrote the caller's times: issuedAt %v cooldownUntil %v", relIssued, cooldown)
	}

	rec, err = s.GetHandle(ctx, "deggen")
	if err != nil || rec == nil {
		t.Fatalf("GetHandle after release = %v, %v", rec, err)
	}
	if want := relIssued.Truncate(time.Millisecond); !rec.IssuedAt.Equal(want) {
		t.Errorf("tombstone issuedAt = %v, want %v", rec.IssuedAt, want)
	}
	if want := cooldown.Truncate(time.Millisecond); rec.CooldownUntil == nil || !rec.CooldownUntil.Equal(want) {
		t.Errorf("tombstone cooldownUntil = %v, want %v", rec.CooldownUntil, want)
	}
	if want := relIssued.Truncate(time.Millisecond); rec.ReleasedAt == nil || !rec.ReleasedAt.Equal(want) {
		t.Errorf("tombstone releasedAt = %v, want %v", rec.ReleasedAt, want)
	}
}

// TestRegisterDevice_BackfillsLegacyID covers device documents written before
// registrations carried a numeric ID. $setOnInsert never fires for them, so
// without a backfill re-registering such a token would report ID 0, which
// @bsv/message-box-client rejects.
func TestRegisterDevice_BackfillsLegacyID(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongoBare(t, uri, database)

	// The oldest shape: no registrationId and no fcmToken. The first boot of
	// this release backfills the token; the ID is filled in on registration.
	const alice = "02alice"
	ts := now()
	if _, err := s.db.Collection(devicesColl).InsertOne(ctx, bson.M{
		"_id": "legacy-tok", "identityKey": alice, "deviceId": nil, "platform": nil,
		"active": true, "createdAt": ts, "updatedAt": ts, "lastUsed": ts,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	id, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: "legacy-tok"})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if id < 1 {
		t.Fatalf("id = %d, want a backfilled positive ID", id)
	}
	again, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: "legacy-tok"})
	if err != nil || again != id {
		t.Fatalf("re-register = %d, %v; want %d, nil", again, err, id)
	}
	if fresh, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: "new-tok"}); err != nil || fresh == id {
		t.Fatalf("new token id = %d, %v; want one distinct from %d", fresh, err, id)
	}

	devices, err := s.ListDevices(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("alice has %d devices, want the legacy one adopted rather than duplicated: %+v", len(devices), devices)
	}
	for _, d := range devices {
		if d.FCMToken != "legacy-tok" {
			continue
		}
		if d.ID != id {
			t.Errorf("listed legacy ID = %d, want %d", d.ID, id)
		}
		if !d.CreatedAt.Equal(ts) {
			t.Errorf("legacy createdAt = %v, want it kept at %v", d.CreatedAt, ts)
		}
	}
}

// storedDevice is a device document as it is on disk, for the assertions that
// have to see the fields the storage contract hides.
type storedDevice struct {
	// ID is the _id as stored: a string for a document of the previous
	// release, the token itself, and an ObjectID for one written since.
	ID             any    `bson:"_id"`
	IdentityKey    string `bson:"identityKey"`
	FCMToken       string `bson:"fcmToken"`
	Active         bool   `bson:"active"`
	RegistrationID int64  `bson:"registrationId"`
}

// pairKey names a registration, which is what a test can know of a document
// whose _id the store chose.
func pairKey(identityKey, fcmToken string) string { return identityKey + "|" + fcmToken }

// storedDevices returns the device documents keyed by pairKey. Call it only
// once every document has an fcmToken, which a boot of EnsureSchema gives them.
func storedDevices(t *testing.T, s *Store) map[string]storedDevice {
	t.Helper()
	ctx := context.Background()
	cur, err := s.db.Collection(devicesColl).Find(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	var docs []storedDevice
	if err := cur.All(ctx, &docs); err != nil {
		t.Fatal(err)
	}
	out := map[string]storedDevice{}
	for _, d := range docs {
		key := pairKey(d.IdentityKey, d.FCMToken)
		if _, dup := out[key]; dup {
			t.Errorf("more than one document for %s", key)
		}
		out[key] = d
	}
	return out
}

// TestEnsureSchema_MigratesLegacyDevices boots this release's schema over
// device documents as the previous one wrote them: the token is the _id, there
// is no fcmToken field, and one identity can hold several. The unique index on
// (identityKey, fcmToken) can only be built once fcmToken is backfilled, because
// until then every one of an identity's documents indexes it as null and the
// build fails on the second.
func TestEnsureSchema_MigratesLegacyDevices(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongoBare(t, uri, database)

	const alice, bob = "02alice", "02bob"
	created := now().Add(-48 * time.Hour)
	legacy := []bson.M{
		{"_id": "tok-a1", "identityKey": alice, "deviceId": "pixel", "platform": "android", "active": true, "registrationId": int64(7)},
		{"_id": "tok-a2", "identityKey": alice, "deviceId": nil, "platform": "ios", "active": false, "registrationId": int64(9)},
		// Older still: no registrationId either.
		{"_id": "tok-a3", "identityKey": alice, "deviceId": nil, "platform": nil, "active": true},
		{"_id": "tok-b1", "identityKey": bob, "deviceId": nil, "platform": nil, "active": true, "registrationId": int64(8)},
	}
	for _, doc := range legacy {
		doc["createdAt"], doc["updatedAt"], doc["lastUsed"] = created, created, created
		if _, err := s.db.Collection(devicesColl).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	// The counter an older release left behind; the migration must not need it.
	if _, err := s.db.Collection(countersColl).InsertOne(ctx, bson.M{"_id": devicesColl, "seq": int64(9)}); err != nil {
		t.Fatal(err)
	}

	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema over legacy documents: %v", err)
	}
	// A second boot is a no-op.
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema again: %v", err)
	}

	// Every legacy document keeps its _id and gains fcmToken from it.
	docs := storedDevices(t, s)
	if len(docs) != len(legacy) {
		t.Fatalf("%d documents after migration, want %d", len(docs), len(legacy))
	}
	for _, want := range legacy {
		id := want["_id"].(string)
		got, ok := docs[pairKey(want["identityKey"].(string), id)]
		if !ok {
			t.Fatalf("document %s lost, or not given its fcmToken; have %+v", id, docs)
		}
		if got.ID != id {
			t.Errorf("%s _id = %v, want it kept", id, got.ID)
		}
		if got.IdentityKey != want["identityKey"] || got.Active != want["active"] {
			t.Errorf("%s = %+v, want identity and active flag kept", id, got)
		}
		if reg, had := want["registrationId"]; had && got.RegistrationID != reg {
			t.Errorf("%s registrationId = %d, want %d kept", id, got.RegistrationID, reg)
		}
	}

	// The legacy documents still read back, under their own tokens and ids.
	listed, err := s.ListDevices(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	byToken := map[string]storage.Device{}
	for _, d := range listed {
		byToken[d.FCMToken] = d
	}
	if len(byToken) != 3 || byToken["tok-a1"].ID != 7 || byToken["tok-a2"].ID != 9 || byToken["tok-a2"].Active {
		t.Errorf("alice's devices = %+v, want tok-a1 (id 7), tok-a2 (id 9, inactive) and tok-a3", listed)
	}

	// Re-registering a legacy token adopts its document: same _id, same id.
	id, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-a1"})
	if err != nil || id != 7 {
		t.Errorf("alice re-registers tok-a1 = %d, %v; want 7, nil", id, err)
	}
	// A legacy document with no id gets one, and keeps it.
	idA3, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-a3"})
	if err != nil || idA3 < 1 {
		t.Fatalf("alice re-registers tok-a3 = %d, %v; want a backfilled id", idA3, err)
	}
	if again, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: "tok-a3"}); err != nil || again != idA3 {
		t.Errorf("tok-a3 re-register = %d, %v; want %d", again, err, idA3)
	}
	if got := storedDevices(t, s); len(got) != len(legacy) {
		t.Errorf("%d documents after re-registering, want the legacy ones adopted, not duplicated: %+v", len(got), got)
	}

	// Now a second identity can hold a legacy token. It gets a document of its
	// own, keyed by an ObjectID, and the legacy document is untouched.
	bobID, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: bob, FCMToken: "tok-a1"})
	if err != nil {
		t.Fatalf("bob registers alice's legacy token: %v", err)
	}
	if bobID == 7 {
		t.Errorf("bob's registration reused alice's id 7")
	}
	docs = storedDevices(t, s)
	own, ok := docs[pairKey(bob, "tok-a1")]
	if !ok || own.RegistrationID != bobID {
		t.Errorf("bob's document = %+v (present %v), want one for the token with id %d; have %+v", own, ok, bobID, docs)
	}
	if _, isObjectID := own.ID.(bson.ObjectID); !isObjectID {
		t.Errorf("bob's document _id = %#v, want an ObjectID", own.ID)
	}
	if legacyDoc := docs[pairKey(alice, "tok-a1")]; legacyDoc.ID != "tok-a1" {
		t.Errorf("alice's legacy document = %+v, want it still hers and keyed by the token", legacyDoc)
	}

	// Token-keyed writes reach both the legacy document and the new one.
	if err := s.DeactivateDevice(ctx, "tok-a1"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{alice, bob} {
		active, err := s.ListActiveDevices(ctx, who)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range active {
			if d.FCMToken == "tok-a1" {
				t.Errorf("%s still has tok-a1 active after the token was deactivated", who)
			}
		}
	}

	// Unregistering removes the legacy document too, and only alice's.
	if err := s.UnregisterDevice(ctx, alice, "tok-a1"); err != nil {
		t.Fatal(err)
	}
	docs = storedDevices(t, s)
	if _, ok := docs[pairKey(alice, "tok-a1")]; ok {
		t.Error("alice's legacy tok-a1 document survived UnregisterDevice")
	}
	if _, ok := docs[pairKey(bob, "tok-a1")]; !ok {
		t.Error("bob's tok-a1 document was removed by alice's UnregisterDevice")
	}

	// The unique index is what keeps a pair to one document.
	if _, err := s.db.Collection(devicesColl).InsertOne(ctx, bson.M{
		"_id": "forged", "identityKey": bob, "fcmToken": "tok-a1",
	}); err == nil {
		t.Error("inserted a second document for (bob, tok-a1), want the unique index to refuse it")
	}
}

// During a rolling deploy a replica of the previous release keeps writing
// device documents in the old shape: keyed by the token, with no fcmToken. The
// new schema has to tolerate them until the next boot backfills them.
func TestEnsureSchema_ToleratesLegacyWritesAfterMigration(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongoBare(t, uri, database)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	// Two devices for one identity, both written by an old replica. A unique
	// index that counted the missing fcmToken as null would refuse the second.
	const alice = "02alice"
	ts := now()
	for _, token := range []string{"old-tok-1", "old-tok-2"} {
		if _, err := s.db.Collection(devicesColl).InsertOne(ctx, bson.M{
			"_id": token, "identityKey": alice, "deviceId": nil, "platform": nil,
			"active": true, "createdAt": ts, "updatedAt": ts, "lastUsed": ts, "registrationId": int64(100),
		}); err != nil {
			t.Fatalf("old replica's insert of %s: %v", token, err)
		}
	}

	// They read back with their tokens even before the backfill.
	got, err := s.ListActiveDevices(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]bool{}
	for _, d := range got {
		tokens[d.FCMToken] = true
	}
	if len(got) != 2 || !tokens["old-tok-1"] || !tokens["old-tok-2"] {
		t.Errorf("alice's active devices = %+v, want both old-shape documents under their tokens", got)
	}

	// The next boot backfills them.
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	docs := storedDevices(t, s)
	for _, token := range []string{"old-tok-1", "old-tok-2"} {
		if got, ok := docs[pairKey(alice, token)]; !ok || got.ID != token {
			t.Errorf("%s = %+v (present %v) after the next boot, want it kept and given its fcmToken", token, got, ok)
		}
	}
}

// A registration through the new code and one through an old replica can land
// on the same (identity, token) pair mid-deploy, as two documents: one keyed by
// the pair, one by the bare token. The backfill would give them the same
// fcmToken and trip the unique index; a boot that failed there would leave the
// server unable to start with no way to recover short of editing the database.
func TestEnsureSchema_ResolvesLegacyDuplicateOfARegisteredPair(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongoBare(t, uri, database)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	const alice, bob = "02alice", "02bob"
	if _, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: "dup-tok"}); err != nil {
		t.Fatal(err)
	}
	ts := now().Add(-time.Hour)
	for _, doc := range []bson.M{
		{"_id": "dup-tok", "identityKey": alice},  // the collision
		{"_id": "solo-tok", "identityKey": alice}, // no collision, must still be backfilled
		{"_id": "bob-tok", "identityKey": bob},    // another identity's
	} {
		doc["active"], doc["createdAt"], doc["updatedAt"], doc["lastUsed"] = true, ts, ts, ts
		if _, err := s.db.Collection(devicesColl).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema with a colliding legacy document: %v", err)
	}

	// storedDevices fails the test on a second document for one pair.
	docs := storedDevices(t, s)
	if got, ok := docs[pairKey(alice, "dup-tok")]; !ok || got.ID == "dup-tok" {
		t.Errorf("registered pair = %+v (present %v), want the registration made through this release kept, not the legacy duplicate", got, ok)
	}
	for _, c := range []struct{ identity, token string }{{alice, "solo-tok"}, {bob, "bob-tok"}} {
		if got, ok := docs[pairKey(c.identity, c.token)]; !ok || got.ID != c.token {
			t.Errorf("%s = %+v (present %v), want it kept and backfilled", c.token, got, ok)
		}
	}
	if devices, err := s.ListDevices(ctx, alice); err != nil || len(devices) != 2 {
		t.Errorf("alice's devices = %+v, %v; want two (dup-tok once, solo-tok)", devices, err)
	}
}

// The previous release reads a device document's _id as the FCM token: it lists
// an identity's devices into a struct keyed on _id, sends to each, and when FCM
// calls a token invalid it deactivates the document whose _id is that string.
// A rolling deploy leaves such replicas serving next to this release, and the
// wallet does not register a pair again once it has (its marker says it is),
// so a document those replicas deactivate stays deactivated. A new document's
// _id must therefore be something no string they derive can match.
func TestRegisterDevice_PreviousReleaseCannotDeactivateIt(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	ctx := context.Background()
	s := newMongoBare(t, uri, database)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	const alice, bob, token = "02alice", "02bob", "shared-tok"
	for _, who := range []string{alice, bob} {
		if _, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: who, FCMToken: token}); err != nil {
			t.Fatal(err)
		}
	}
	coll := s.db.Collection(devicesColl)

	for _, who := range []string{alice, bob} {
		raw, err := coll.FindOne(ctx, bson.M{"identityKey": who}).Raw()
		if err != nil {
			t.Fatal(err)
		}
		if got := raw.Lookup("_id").Type; got != bson.TypeObjectID {
			t.Errorf("%s's document _id has BSON type %v, want an ObjectID, which is not a string", who, got)
		}
	}

	// What a replica of the previous release does. Whether its listing decodes
	// an ObjectID _id as a token or refuses it, every key it could end up
	// deactivating by has to miss.
	deactivated := 0
	deactivate := func(key string) {
		res, err := coll.UpdateOne(ctx,
			bson.M{"_id": key},
			bson.M{"$set": bson.M{"active": false, "updatedAt": now()}},
		)
		if err != nil {
			t.Fatal(err)
		}
		deactivated += int(res.ModifiedCount)
	}
	for _, who := range []string{alice, bob} {
		var listed []struct {
			Token string `bson:"_id"`
		}
		cur, err := coll.Find(ctx, bson.M{"identityKey": who, "active": true})
		if err == nil {
			err = cur.All(ctx, &listed)
		}
		t.Logf("previous release lists %s's devices: %d tokens, error %v", who, len(listed), err)
		for _, d := range listed {
			deactivate(d.Token)
		}
		// The key this release used to give a document, and the bare token.
		deactivate(who + "|" + token)
		deactivate(token)
	}
	if deactivated != 0 {
		t.Errorf("the previous release's token-keyed write reached %d documents, want none", deactivated)
	}

	for _, who := range []string{alice, bob} {
		active, err := s.ListActiveDevices(ctx, who)
		if err != nil || len(active) != 1 || active[0].FCMToken != token {
			t.Errorf("%s's active devices = %+v, %v; want the registration intact", who, active, err)
		}
	}
}

// A replica of the previous release that registers a pair this release already
// holds writes a second document for it, keyed by the bare token and without an
// fcmToken, and it stays until the next boot's backfill drops it. Until then it
// must not double the push, a token FCM reports dead must not stay active on it,
// and unregistering the pair must not leave it pushing.
func TestDevices_OldReplicaTwinOfARegisteredPair(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	database := os.Getenv("MONGO_TEST_DATABASE")
	if database == "" {
		database = "messagebox_test"
	}

	const alice, bob, token = "02alice", "02bob", "twin-tok"

	// twinned returns a store where alice holds the token through this release
	// and again through an old replica, and bob holds it through this release.
	twinned := func(t *testing.T) (*Store, int64) {
		t.Helper()
		ctx := context.Background()
		s := newMongoBare(t, uri, database)
		if err := s.EnsureSchema(ctx); err != nil {
			t.Fatal(err)
		}
		id, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: alice, FCMToken: token})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: bob, FCMToken: token}); err != nil {
			t.Fatal(err)
		}
		ts := now().Add(time.Minute)
		if _, err := s.db.Collection(devicesColl).InsertOne(ctx, bson.M{
			"_id": token, "identityKey": alice, "deviceId": nil, "platform": nil,
			"active": true, "createdAt": ts, "updatedAt": ts, "lastUsed": ts,
		}); err != nil {
			t.Fatalf("old replica's insert: %v", err)
		}
		return s, id
	}
	active := func(t *testing.T, s *Store, who string) int64 {
		t.Helper()
		n, err := s.db.Collection(devicesColl).CountDocuments(context.Background(), bson.M{"identityKey": who, "active": true})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("ListedOnce", func(t *testing.T) {
		ctx := context.Background()
		s, id := twinned(t)
		got, err := s.ListActiveDevices(ctx, alice)
		if err != nil || len(got) != 1 || got[0].ID != id {
			t.Errorf("active devices = %+v, %v; want the registration once, with id %d", got, err, id)
		}
		got, err = s.ListDevices(ctx, alice)
		if err != nil || len(got) != 1 || got[0].ID != id {
			t.Errorf("devices = %+v, %v; want the registration once, with id %d", got, err, id)
		}
	})

	t.Run("DeactivatedWithTheToken", func(t *testing.T) {
		s, _ := twinned(t)
		if err := s.DeactivateDevice(context.Background(), token); err != nil {
			t.Fatal(err)
		}
		for _, who := range []string{alice, bob} {
			if n := active(t, s, who); n != 0 {
				t.Errorf("%s has %d documents active after the token was deactivated, want none", who, n)
			}
		}
	})

	t.Run("RemovedWithTheRegistration", func(t *testing.T) {
		s, _ := twinned(t)
		if err := s.UnregisterDevice(context.Background(), alice, token); err != nil {
			t.Fatal(err)
		}
		if n := active(t, s, alice); n != 0 {
			t.Errorf("alice has %d documents active after unregistering, want none", n)
		}
		if n := active(t, s, bob); n != 1 {
			t.Errorf("bob has %d documents active after alice unregistered, want his one", n)
		}
	})
}
