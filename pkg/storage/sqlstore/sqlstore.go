// Package sqlstore implements storage.Store on top of database/sql. It supports
// SQLite and PostgreSQL; the dialect differences are the ?/$n placeholder
// rebinding and the DDL in the two migration sets.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
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

// permissionUniqueIndex enforces one permission row per
// (recipient, sender, message_box), treating a NULL sender as the empty string
// so that the box-wide row is covered too. A plain UNIQUE constraint cannot do
// this: SQL treats NULLs as distinct, which is why the table constraint alone
// let concurrent callers insert duplicate box-wide rows under PostgreSQL.
const permissionUniqueIndex = `CREATE UNIQUE INDEX IF NOT EXISTS idx_message_permissions_unique
	ON message_permissions(recipient, COALESCE(sender, ''), message_box)`

// permissionConflictTarget names that index as an upsert conflict target. Both
// SQLite and PostgreSQL can infer an expression index this way.
const permissionConflictTarget = `(recipient, COALESCE(sender, ''), message_box)`

// ErrDuplicatePermissions is returned by EnsureSchema when the database already
// holds duplicate permission rows, which blocks the unique index. Deduplicating
// deletes rows, so it is never done implicitly on startup — run the server with
// -dedupe-permissions, or call DedupePermissions.
var ErrDuplicatePermissions = errors.New(
	"message_permissions holds duplicate rows for the same (recipient, sender, message_box); " +
		"run the server once with -dedupe-permissions to remove them")

// EnsureSchema runs all migrations to bring the schema up to date.
func (s *Store) EnsureSchema(ctx context.Context) error {
	var migrations []string
	switch s.driver {
	case "postgres":
		migrations = postgresMigrations()
	default:
		migrations = sqliteMigrations()
	}

	for _, m := range migrations {
		if _, err := s.db.ExecContext(ctx, m); err != nil {
			return fmt.Errorf("migration failed: %s: %w", m[:min(60, len(m))], err)
		}
	}

	return s.ensurePermissionUniqueIndex(ctx)
}

// ensurePermissionUniqueIndex creates permissionUniqueIndex, reporting a
// duplicate backlog as ErrDuplicatePermissions rather than letting the index
// creation fail with a message that says nothing about how to fix it.
func (s *Store) ensurePermissionUniqueIndex(ctx context.Context) error {
	n, err := s.countDuplicatePermissions(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for duplicate permissions: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w (%d duplicate rows)", ErrDuplicatePermissions, n)
	}

	if _, err := s.db.ExecContext(ctx, permissionUniqueIndex); err != nil {
		return fmt.Errorf("failed to create the permission unique index: %w", err)
	}
	return nil
}

// countDuplicatePermissions counts the rows that would have to go for the
// unique index to hold.
func (s *Store) countDuplicatePermissions(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(c - 1), 0) FROM (
		   SELECT COUNT(*) AS c FROM message_permissions
		   GROUP BY recipient, COALESCE(sender, ''), message_box
		   HAVING COUNT(*) > 1
		 ) dupes`,
	).Scan(&n)
	return n, err
}

// DedupePermissions removes duplicate permission rows so that the unique index
// can be created, keeping one row per (recipient, sender, message_box) and
// returning the number deleted. It is safe to run repeatedly.
//
// Which row survives is a security decision, not a bookkeeping one. A duplicate
// pair can hold conflicting intent — an older row a stale fee write-back
// created, and a newer row where the recipient blocked a sender — so keeping the
// lowest id would delete the block and leave delivery open. The ordering below
// keeps any blocked row first, then the most recently updated, then the highest
// id: it collapses toward the most restrictive setting, which is the only safe
// direction when the rows disagree.
func (s *Store) DedupePermissions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM message_permissions WHERE id IN (
		   SELECT id FROM (
		     SELECT id, ROW_NUMBER() OVER (
		       PARTITION BY recipient, COALESCE(sender, ''), message_box
		       ORDER BY CASE WHEN recipient_fee = `+fmt.Sprint(storage.FeeBlocked)+` THEN 0 ELSE 1 END,
		                updated_at DESC,
		                id DESC
		     ) AS rn
		     FROM message_permissions
		   ) ranked
		   WHERE rn > 1
		 )`,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to deduplicate permissions: %w", err)
	}
	return res.RowsAffected()
}

func commonMigrations() []string {
	return []string{
		`INSERT INTO server_fees (message_box, delivery_fee) VALUES ('notifications', 10) ON CONFLICT DO NOTHING`,
		`INSERT INTO server_fees (message_box, delivery_fee) VALUES ('inbox', 0) ON CONFLICT DO NOTHING`,
		`INSERT INTO server_fees (message_box, delivery_fee) VALUES ('payment_inbox', 0) ON CONFLICT DO NOTHING`,
		`CREATE INDEX IF NOT EXISTS idx_messages_recipient_box ON messages(recipient, messageBoxId)`,
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_recipient ON message_permissions(recipient)`,
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_recipient_box ON message_permissions(recipient, message_box)`,
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_box ON message_permissions(message_box)`,
		`CREATE INDEX IF NOT EXISTS idx_message_permissions_sender ON message_permissions(sender)`,
		`CREATE INDEX IF NOT EXISTS idx_device_registrations_identity ON device_registrations(identity_key)`,
		`CREATE INDEX IF NOT EXISTS idx_device_registrations_identity_active ON device_registrations(identity_key, active)`,
	}
}

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
			recipient_fee INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS server_fees (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			message_box TEXT NOT NULL UNIQUE,
			delivery_fee INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS device_registrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			identity_key TEXT NOT NULL,
			fcm_token TEXT NOT NULL UNIQUE,
			device_id TEXT,
			platform TEXT,
			last_used DATETIME,
			active BOOLEAN DEFAULT TRUE
		)`,
	}
	return append(tables, commonMigrations()...)
}

// postgresDropRedundantPermissionUnique removes the table-level UNIQUE that
// older databases were created with. It is redundant once
// permissionUniqueIndex exists, and actively harmful: an INSERT whose
// ON CONFLICT targets the expression index raises on a violation of any other
// constraint, so a sender-specific upsert would fail instead of updating.
// SQLite cannot drop a constraint without rebuilding the table; it tolerates
// both, so existing SQLite databases keep theirs.
const postgresDropRedundantPermissionUnique = `ALTER TABLE message_permissions
	DROP CONSTRAINT IF EXISTS message_permissions_recipient_sender_message_box_key`

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
			recipient_fee INTEGER NOT NULL
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
			fcm_token TEXT NOT NULL UNIQUE,
			device_id TEXT,
			platform TEXT,
			last_used TIMESTAMP,
			active BOOLEAN DEFAULT TRUE
		)`,
	}
	return append(append(tables, postgresDropRedundantPermissionUnique), commonMigrations()...)
}

// compile-time assertion that Store satisfies the contract.
var _ storage.Store = (*Store)(nil)
