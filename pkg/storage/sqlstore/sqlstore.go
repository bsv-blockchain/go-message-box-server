// Package sqlstore implements storage.Store on top of database/sql. It supports
// SQLite and PostgreSQL; the dialect differences are the ?/$n placeholder
// rebinding and the DDL in the two migration sets.
package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// Store is a SQL-backed storage.Store.
type Store struct {
	db     *sql.DB
	driver string
}

// New opens a database connection.
func New(driver, source string) (*Store, error) {
	conn, err := sql.Open(driver, source)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return &Store{db: conn, driver: driver}, nil
}

// Close releases the connection pool.
func (s *Store) Close() error { return s.db.Close() }

// messagesOrderBy is the ORDER BY clause ListMessages and PageMessages share.
// On PostgreSQL, messageId is ordered COLLATE "C" (byte-wise) rather than the
// database's default locale collation: a fresh PostgreSQL database (such as
// the official postgres:16 image, whose default locale is en_US.utf8) sorts
// text case-insensitively-ish ('a' before 'B'), while SQLite's default BINARY
// collation and MongoDB's default sort both order by UTF-8 byte value ('B'
// before 'a'). Without this, two messages that tie on created_at — which
// created_at alone cannot happen to prevent, since it is only millisecond (Mongo)
// or wall-clock (SQL) resolution — would come back in a different order
// depending only on which backend serves the request, breaking the documented
// "ordered by CreatedAt ascending, then MessageID ascending" contract for
// Postgres specifically. SQLite has no collation named "C", so this must stay
// a query-time choice rather than a shared literal.
func (s *Store) messagesOrderBy() string {
	if s.driver == "postgres" {
		return `ORDER BY created_at ASC, messageId COLLATE "C" ASC`
	}
	return `ORDER BY created_at ASC, messageId ASC`
}

// rebind converts ? placeholders to $1, $2, ... for postgres.
func (s *Store) rebind(query string) string {
	if s.driver != "postgres" {
		return query
	}
	var buf strings.Builder
	n := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			fmt.Fprintf(&buf, "$%d", n)
			n++
		} else {
			buf.WriteByte(query[i])
		}
	}
	return buf.String()
}

// exec wraps sql.DB.ExecContext with placeholder rebinding.
func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(query), args...)
}

// queryRow wraps sql.DB.QueryRowContext with placeholder rebinding.
func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.rebind(query), args...)
}

// query wraps sql.DB.QueryContext with placeholder rebinding.
func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.rebind(query), args...)
}

// nullStr converts a scanned nullable column to a pointer.
func nullStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

// nullTime converts a scanned nullable timestamp to a pointer.
func nullTime(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	v := n.Time
	return &v
}

// EnsureSchema runs all migrations to bring the schema up to date.
func (s *Store) EnsureSchema(ctx context.Context) error {
	var migrations []string
	switch s.driver {
	case "postgres":
		migrations = postgresMigrations()
	default:
		// SQLite cannot drop a column's inline UNIQUE, so the old device table
		// is rebuilt in Go. It runs first: the table may not exist yet, and the
		// statements below create the indexes the rebuild drops with it.
		if err := s.rebuildSQLiteDevicesTable(ctx); err != nil {
			return err
		}
		migrations = sqliteMigrations()
	}

	for _, m := range migrations {
		if _, err := s.db.ExecContext(ctx, m); err != nil {
			return fmt.Errorf("migration failed: %s: %w", m[:min(60, len(m))], err)
		}
	}

	// Seed the default fees without clobbering values an operator has changed.
	for _, f := range storage.DefaultDeliveryFees() {
		_, err := s.exec(ctx,
			`INSERT INTO server_fees (message_box, delivery_fee) VALUES (?, ?) ON CONFLICT DO NOTHING`,
			f.MessageBox, f.Fee,
		)
		if err != nil {
			return fmt.Errorf("failed to seed delivery fee for %s: %w", f.MessageBox, err)
		}
	}
	return nil
}

// messagesIndex covers ListMessages: equality on recipient and messageBoxId,
// then the ORDER BY keys. Stopping at the two equality columns leaves the sort
// in place (EXPLAIN QUERY PLAN: USE TEMP B-TREE FOR ORDER BY).
const messagesIndexColumns = `messages(recipient, messageBoxId, created_at, messageId)`

func commonMigrations() []string {
	return []string{
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_recipient ON message_permissions(recipient)`,
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_recipient_box ON message_permissions(recipient, message_box)`,
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_box ON message_permissions(message_box)`,
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_sender ON message_permissions(sender)`,
		`CREATE INDEX IF NOT EXISTS idx_device_registrations_identity ON device_registrations(identity_key)`,
		`CREATE INDEX IF NOT EXISTS idx_device_registrations_identity_active ON device_registrations(identity_key, active)`,
		// A registration is one identity's hold on one token. The same token is
		// held by several identities when one install runs several wallet
		// profiles, so the token alone cannot be unique.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_device_registrations_identity_token ON device_registrations(identity_key, fcm_token)`,
		// DeactivateDevice and UpdateDeviceLastUsed look a token up on its own,
		// on every push. The unique index above leads with identity_key, so it
		// cannot serve that.
		`CREATE INDEX IF NOT EXISTS idx_device_registrations_token ON device_registrations(fcm_token)`,
	}
}

// sqliteDevicesTable is the device table DDL. It takes the table name so the
// rebuild below can create its replacement from the same text. The only key is
// the primary key: uniqueness on (identity_key, fcm_token) is an index in
// commonMigrations.
func sqliteDevicesTable(name string) string {
	return `CREATE TABLE IF NOT EXISTS ` + name + ` (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			identity_key TEXT NOT NULL,
			fcm_token TEXT NOT NULL,
			device_id TEXT,
			platform TEXT,
			last_used DATETIME,
			active BOOLEAN DEFAULT TRUE
		)`
}

// hasLegacyTokenKey reports whether device_registrations still carries a unique
// key on fcm_token alone, which is what the schema used to have. It asks the
// catalog rather than comparing DDL text, so it is right whatever the key is
// called and however the table was created. A missing table has no key.
func (s *Store) hasLegacyTokenKey(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pragma_index_list('device_registrations') il
		WHERE il."unique" = 1
		  AND (SELECT COUNT(*) FROM pragma_index_info(il.name)) = 1
		  AND (SELECT name FROM pragma_index_info(il.name)) = 'fcm_token'`,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("failed to inspect device_registrations keys: %w", err)
	}
	return n > 0, nil
}

// rebuildSQLiteDevicesTable moves an old device table, unique on fcm_token,
// onto the current shape. It copies every row with its id, because clients keep
// the registration id they were given, and it does the whole swap in one
// transaction, so an interruption leaves the old table whole. It does nothing on
// a fresh database or an already-migrated one.
func (s *Store) rebuildSQLiteDevicesTable(ctx context.Context) error {
	legacy, err := s.hasLegacyTokenKey(ctx)
	if err != nil || !legacy {
		return err
	}

	const staging = "device_registrations_rebuild"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin device table rebuild: %w", err)
	}
	defer tx.Rollback()

	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ` + staging,
		sqliteDevicesTable(staging),
		`INSERT INTO ` + staging + ` (id, created_at, updated_at, identity_key, fcm_token, device_id, platform, last_used, active)
		 SELECT id, created_at, updated_at, identity_key, fcm_token, device_id, platform, last_used, active FROM device_registrations`,
		// The old table's indexes go with it; commonMigrations recreates them.
		`DROP TABLE device_registrations`,
		`ALTER TABLE ` + staging + ` RENAME TO device_registrations`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("device table rebuild failed: %s: %w", stmt[:min(60, len(stmt))], err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit device table rebuild: %w", err)
	}
	return nil
}

// sqliteMessagesIndex builds in one go. SQLite serialises writers anyway, so
// there is no rolling-deploy window to protect.
const sqliteMessagesIndex = `CREATE INDEX IF NOT EXISTS idx_messages_recipient_box ON ` + messagesIndexColumns

func sqliteMigrations() []string {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS messageBox (
			messageBoxId INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			type TEXT NOT NULL,
			identityKey TEXT NOT NULL,
			UNIQUE(type, identityKey)
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			messageId TEXT NOT NULL UNIQUE,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			messageBoxId INTEGER REFERENCES messageBox(messageBoxId) ON DELETE CASCADE,
			sender TEXT NOT NULL,
			recipient TEXT NOT NULL,
			body TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS message_permissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			recipient TEXT NOT NULL,
			sender TEXT,
			message_box TEXT NOT NULL,
			recipient_fee INTEGER NOT NULL,
			UNIQUE(recipient, sender, message_box)
		)`,
		`CREATE TABLE IF NOT EXISTS server_fees (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			message_box TEXT NOT NULL UNIQUE,
			delivery_fee INTEGER NOT NULL
		)`,
		sqliteDevicesTable("device_registrations"),
	}
	return append(append(tables, sqliteMessagesIndex), commonMigrations()...)
}

// postgresMessagesIndex builds without blocking writers. A plain CREATE INDEX
// holds a SHARE lock on messages for the whole build, so during a rolling
// deploy every sendMessage INSERT and acknowledgeMessage DELETE on the old
// replicas waits while the new instance sits before ListenAndServe. Each
// migration runs in its own autocommit ExecContext, which is what CONCURRENTLY
// requires.
//
// IF NOT EXISTS would happily skip an invalid leftover from a build that failed
// part way, so the invalid one is dropped first; CONCURRENTLY cannot run inside
// the DO block, hence the two statements.
var postgresMessagesIndex = []string{
	`DO $$
	 BEGIN
	   IF EXISTS (
	     SELECT 1 FROM pg_class c
	     JOIN pg_index i ON i.indexrelid = c.oid
	     WHERE c.relname = 'idx_messages_recipient_box' AND NOT i.indisvalid
	   ) THEN
	     EXECUTE 'DROP INDEX idx_messages_recipient_box';
	   END IF;
	 END $$`,
	`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_recipient_box ON ` + messagesIndexColumns,
}

func postgresMigrations() []string {
	tables := []string{
		`CREATE TABLE IF NOT EXISTS messageBox (
			messageBoxId SERIAL PRIMARY KEY,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			type TEXT NOT NULL,
			identityKey TEXT NOT NULL,
			UNIQUE(type, identityKey)
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			messageId TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			messageBoxId INTEGER REFERENCES messageBox(messageBoxId) ON DELETE CASCADE,
			sender TEXT NOT NULL,
			recipient TEXT NOT NULL,
			body TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS message_permissions (
			id SERIAL PRIMARY KEY,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			recipient TEXT NOT NULL,
			sender TEXT,
			message_box TEXT NOT NULL,
			recipient_fee INTEGER NOT NULL,
			UNIQUE(recipient, sender, message_box)
		)`,
		`CREATE TABLE IF NOT EXISTS server_fees (
			id SERIAL PRIMARY KEY,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			message_box TEXT NOT NULL UNIQUE,
			delivery_fee INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS device_registrations (
			id SERIAL PRIMARY KEY,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			identity_key TEXT NOT NULL,
			fcm_token TEXT NOT NULL,
			device_id TEXT,
			platform TEXT,
			last_used TIMESTAMP,
			active BOOLEAN DEFAULT TRUE
		)`,
	}
	migrations := append(tables, postgresMessagesIndex...)
	migrations = append(migrations, commonMigrations()...)
	// Last, so the (identity_key, fcm_token) index from commonMigrations
	// already guards the table when the old key goes: there is no moment with
	// neither.
	return append(migrations, postgresDropLegacyTokenKey)
}

// postgresDropLegacyTokenKey drops the unique constraint an old database has on
// fcm_token alone, which would refuse a second identity's registration of the
// token. It finds the constraint by its shape, not its generated name, and the
// DROP is IF EXISTS so replicas booting together cannot trip over each other.
// A database created by this release has no such constraint and this is a no-op.
//
// During a rolling deploy, replicas still on the old release upsert with
// ON CONFLICT (fcm_token), which no longer has a key to infer from. Their
// /registerDevice calls fail until they are replaced.
const postgresDropLegacyTokenKey = `DO $$
	DECLARE legacy text;
	BEGIN
	  FOR legacy IN
	    SELECT con.conname FROM pg_constraint con
	    JOIN pg_class rel ON rel.oid = con.conrelid
	    JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = con.conkey[1]
	    WHERE rel.relname = 'device_registrations' AND pg_table_is_visible(rel.oid)
	      AND con.contype = 'u' AND array_length(con.conkey, 1) = 1 AND att.attname = 'fcm_token'
	  LOOP
	    EXECUTE format('ALTER TABLE device_registrations DROP CONSTRAINT IF EXISTS %I', legacy);
	  END LOOP;
	END $$`

// compile-time assertion that Store satisfies the contract.
var _ storage.Store = (*Store)(nil)

// compile-time assertion that Store also implements the optional pager.
var _ storage.MessagePager = (*Store)(nil)
