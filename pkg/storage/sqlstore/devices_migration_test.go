package sqlstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// The device table used to be unique on fcm_token alone, so one token had one
// owner. It is now unique on (identity_key, fcm_token). These tests build the
// old table by hand, fill it the way production did, and boot the new schema
// over it: a migration that loses a row, renumbers a registration, or leaves
// the old constraint behind fails here.

const legacyDevicesSQLite = `CREATE TABLE device_registrations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	identity_key TEXT NOT NULL,
	fcm_token TEXT NOT NULL UNIQUE,
	device_id TEXT,
	platform TEXT,
	last_used DATETIME,
	active BOOLEAN DEFAULT TRUE
)`

const legacyDevicesPostgres = `CREATE TABLE device_registrations (
	id SERIAL PRIMARY KEY,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	identity_key TEXT NOT NULL,
	fcm_token TEXT NOT NULL UNIQUE,
	device_id TEXT,
	platform TEXT,
	last_used TIMESTAMP,
	active BOOLEAN DEFAULT TRUE
)`

// legacyIndexes are the two non-unique indexes the old schema also had.
var legacyIndexes = []string{
	`CREATE INDEX idx_device_registrations_identity ON device_registrations(identity_key)`,
	`CREATE INDEX idx_device_registrations_identity_active ON device_registrations(identity_key, active)`,
}

const (
	legacyAlice = "02alice"
	legacyBob   = "02bob"
)

// seedLegacyDevices writes three registrations as the old schema stored them
// and returns their ids by token. Alice's second token is inactive, because a
// migration that resets active would resurrect a token FCM already rejected.
func seedLegacyDevices(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	used := created.Add(time.Hour)

	rows := []struct {
		identity, token string
		deviceID, plat  any
		active          bool
	}{
		{legacyAlice, "tok-a1", "pixel", "android", true},
		{legacyAlice, "tok-a2", nil, "ios", false},
		{legacyBob, "tok-b1", nil, nil, true},
	}
	ids := map[string]int64{}
	for _, r := range rows {
		var id int64
		if err := s.queryRow(ctx,
			`INSERT INTO device_registrations (identity_key, fcm_token, device_id, platform, created_at, updated_at, active, last_used)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			r.identity, r.token, r.deviceID, r.plat, created, used, r.active, used,
		).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", r.token, err)
		}
		ids[r.token] = id
	}
	return ids
}

// assertLegacyDevicesSurvived checks that every seeded row came through the
// migration with its id, fields and active flag intact.
func assertLegacyDevicesSurvived(t *testing.T, s *Store, ids map[string]int64) {
	t.Helper()
	ctx := context.Background()

	alice, err := s.ListDevices(ctx, legacyAlice)
	if err != nil {
		t.Fatal(err)
	}
	if len(alice) != 2 {
		t.Fatalf("alice has %d devices after migration, want 2: %+v", len(alice), alice)
	}
	byToken := map[string]storage.Device{}
	for _, d := range alice {
		byToken[d.FCMToken] = d
	}
	a1, a2 := byToken["tok-a1"], byToken["tok-a2"]
	if a1.ID != ids["tok-a1"] || a2.ID != ids["tok-a2"] {
		t.Errorf("ids = %d, %d after migration, want %d, %d: clients have stored these", a1.ID, a2.ID, ids["tok-a1"], ids["tok-a2"])
	}
	if a1.DeviceID == nil || *a1.DeviceID != "pixel" || a1.Platform == nil || *a1.Platform != "android" || !a1.Active {
		t.Errorf("tok-a1 = %+v, want deviceId pixel, platform android, active", a1)
	}
	if a2.Active {
		t.Errorf("tok-a2 active = true after migration, want the deactivation kept")
	}
	if a1.LastUsed == nil || a1.CreatedAt.IsZero() {
		t.Errorf("tok-a1 = %+v, want createdAt and lastUsed kept", a1)
	}

	bob, err := s.ListDevices(ctx, legacyBob)
	if err != nil {
		t.Fatal(err)
	}
	if len(bob) != 1 || bob[0].ID != ids["tok-b1"] {
		t.Errorf("bob devices = %+v, want tok-b1 with id %d", bob, ids["tok-b1"])
	}
}

// assertTokenIsNoLongerUnique registers an existing token for a second identity,
// which the old constraint refuses (or, through the old upsert, steals).
func assertTokenIsNoLongerUnique(t *testing.T, s *Store, ids map[string]int64) {
	t.Helper()
	ctx := context.Background()

	id, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: legacyBob, FCMToken: "tok-a1"})
	if err != nil {
		t.Fatalf("RegisterDevice(bob, tok-a1): %v", err)
	}
	if id == ids["tok-a1"] {
		t.Errorf("bob's registration reused alice's id %d", id)
	}
	for _, who := range []string{legacyAlice, legacyBob} {
		var found bool
		active, err := s.ListActiveDevices(ctx, who)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range active {
			found = found || d.FCMToken == "tok-a1"
		}
		if !found {
			t.Errorf("%s does not hold tok-a1 after both registered it: %+v", who, active)
		}
	}
	// Alice's own registration kept her id: the pair, not the token, is the key.
	if again, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: legacyAlice, FCMToken: "tok-a1"}); err != nil || again != ids["tok-a1"] {
		t.Errorf("alice re-register = %d, %v; want %d, nil", again, err, ids["tok-a1"])
	}
}

// assertNoSingleColumnTokenKey fails if any unique constraint or index covers
// fcm_token by itself, which is what stops a second identity registering it.
func assertNoSingleColumnTokenKey(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.exec(ctx,
		`INSERT INTO device_registrations (identity_key, fcm_token) VALUES (?, ?), (?, ?)`,
		"02x", "dup-token", "02y", "dup-token",
	); err != nil {
		t.Errorf("two identities on one token: %v, want the old single-column unique key gone", err)
	}
	if _, err := s.exec(ctx,
		`INSERT INTO device_registrations (identity_key, fcm_token) VALUES (?, ?), (?, ?)`,
		"02z", "same-pair", "02z", "same-pair",
	); err == nil {
		t.Error("one identity registered one token twice, want the (identity_key, fcm_token) key to refuse it")
	}
}

func runLegacyMigration(t *testing.T, s *Store, legacyDDL string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range legacyIndexes {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	ids := seedLegacyDevices(t, s)

	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema over the old schema: %v", err)
	}
	assertLegacyDevicesSurvived(t, s, ids)

	// The next boot finds the new shape and leaves everything alone.
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema again: %v", err)
	}
	assertLegacyDevicesSurvived(t, s, ids)

	assertNoSingleColumnTokenKey(t, s)
	assertTokenIsNoLongerUnique(t, s, ids)

	// A registration made after the migration continues the id sequence rather
	// than reusing one of the old ids.
	fresh, err := s.RegisterDevice(ctx, storage.NewDevice{IdentityKey: legacyAlice, FCMToken: "tok-new"})
	if err != nil {
		t.Fatal(err)
	}
	for token, id := range ids {
		if fresh == id {
			t.Errorf("new registration got id %d, which %s already holds", fresh, token)
		}
	}
}

func TestEnsureSchema_MigratesLegacyDevices_SQLite(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		s, err := New("sqlite3", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		s.db.SetMaxOpenConns(1)
		t.Cleanup(func() { s.Close() })
		runLegacyMigration(t, s, legacyDevicesSQLite)
	})

	// The server runs a default pool, so the table rebuild must hold up when
	// other connections exist.
	t.Run("file", func(t *testing.T) {
		s, err := New("sqlite3", filepath.Join(t.TempDir(), "messagebox.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		runLegacyMigration(t, s, legacyDevicesSQLite)
	})
}

func TestEnsureSchema_MigratesLegacyDevices_Postgres(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	s, err := New("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	for _, table := range []string{"messages", "messageBox", "message_permissions", "server_fees", "device_registrations"} {
		if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS `+table+` CASCADE`); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	runLegacyMigration(t, s, legacyDevicesPostgres)
}
