package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

// TestConformance_SQLiteFile runs the suite against a file database with the
// default connection pool, which is how the server runs. The in-memory store
// above is pinned to one connection, so nothing in it can ever race.
func TestConformance_SQLiteFile(t *testing.T) {
	storagetest.RunStoreTests(t, func(t *testing.T) storage.Store {
		t.Helper()
		s, err := New("sqlite3", filepath.Join(t.TempDir(), "messagebox.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

// testPageMessagesTieBreakIsByteWise inserts two messages with an identical
// created_at (forced with a direct UPDATE, since InsertMessage always stamps
// its own now(), and nanosecond-resolution SQL backends will not naturally
// collide on two back-to-back calls) and messageId values that only differ by
// ASCII case, then asserts PageMessages breaks the tie byte-wise ('B' before
// 'a') rather than by the database's default text collation. PostgreSQL's
// default locale (for example the official postgres:16 image's en_US.utf8)
// sorts 'a' before 'B', which would silently violate the documented "then
// MessageID ascending" tie-break for Postgres specifically while leaving
// SQLite's default BINARY collation unaffected — see (*Store).messagesOrderBy.
func testPageMessagesTieBreakIsByteWise(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	const recipient = "02tiebreak"
	for _, id := range []string{"B", "a"} {
		if err := s.InsertMessage(ctx, storage.NewMessage{
			MessageID: id, Recipient: recipient, MessageBox: "inbox", Sender: "02sender", Body: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	tie := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.exec(ctx, `UPDATE messages SET created_at = ? WHERE messageId IN (?, ?)`, tie, "B", "a"); err != nil {
		t.Fatal(err)
	}

	got, err := s.PageMessages(ctx, storage.MessagePageQuery{Recipient: recipient, MessageBox: "inbox", FetchLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.MessageID)
	}
	want := []string{"B", "a"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("tie-break order = %v, want %v (byte-wise, matching SQLite's default and MongoDB's default sort)", ids, want)
	}
}

func TestPageMessages_TieBreakIsByteWise_SQLite(t *testing.T) {
	testPageMessagesTieBreakIsByteWise(t, newSQLite(t).(*Store))
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
