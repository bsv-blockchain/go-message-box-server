package sqlstore

import (
	"context"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// newSQLite returns a fresh in-memory SQLite store with the schema applied.
func newSQLite(t *testing.T) storage.Store {
	t.Helper()
	s, err := New("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Each connection to ":memory:" is its own empty database, so the pool has
	// to be pinned to one for a test to see its own writes.
	s.db.SetMaxOpenConns(1)
	if err := s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestConformance_SQLite(t *testing.T) {
	storagetest.RunStoreTests(t, newSQLite)
}

// TestSQLiteSchema checks the SQLite DDL actually created every table. It uses
// sqlite_master, so it is deliberately not part of the shared suite.
func TestSQLiteSchema(t *testing.T) {
	s := newSQLite(t).(*Store)

	for _, table := range []string{"messageBox", "messages", "message_permissions", "server_fees", "device_registrations"} {
		var name string
		err := s.queryRow(context.Background(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}
