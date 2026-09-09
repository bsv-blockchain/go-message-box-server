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
		migrations = sqliteMigrations()
	}

	for _, m := range migrations {
		if _, err := s.db.ExecContext(ctx, m); err != nil {
			return fmt.Errorf("migration failed: %s: %w", m[:min(60, len(m))], err)
		}
	}
	return nil
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
			fcm_token TEXT NOT NULL UNIQUE,
			device_id TEXT,
			platform TEXT,
			last_used TIMESTAMP,
			active BOOLEAN DEFAULT TRUE
		)`,
	}
	return append(tables, commonMigrations()...)
}

// compile-time assertion that Store satisfies the contract.
var _ storage.Store = (*Store)(nil)
