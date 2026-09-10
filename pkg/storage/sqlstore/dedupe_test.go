package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// legacyStore opens an empty SQLite store carrying the pre-uniqueness schema:
// the table-level UNIQUE(recipient, sender, message_box), which does not
// constrain NULL senders, and no expression index.
func legacyStore(t *testing.T) *Store {
	t.Helper()

	s, err := New("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s.db.SetMaxOpenConns(1)
	t.Cleanup(func() { s.Close() })

	_, err = s.db.ExecContext(context.Background(), `CREATE TABLE message_permissions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		recipient TEXT NOT NULL,
		sender TEXT,
		message_box TEXT NOT NULL,
		recipient_fee INTEGER NOT NULL,
		UNIQUE(recipient, sender, message_box)
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// insertLegacyPermission writes a row directly, bypassing the store, so a test
// can build the duplicate state the old code was able to produce.
func insertLegacyPermission(t *testing.T, s *Store, id int, recipient string, sender *string, messageBox string, fee int, updatedAt string) {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO message_permissions (id, created_at, updated_at, recipient, sender, message_box, recipient_fee)
		 VALUES (?, '2026-01-01 00:00:00', ?, ?, ?, ?, ?)`,
		id, updatedAt, recipient, sender, messageBox, fee,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func permissionRows(t *testing.T, s *Store) []storage.Permission {
	t.Helper()
	rows, err := s.query(context.Background(), `SELECT `+permissionColumns+` FROM message_permissions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out []storage.Permission
	for rows.Next() {
		p, err := scanPermission(rows)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// A dedupe that kept the lowest id would delete the recipient's block and leave
// the stale free-delivery row, which is the same hole SetPermissionIfAbsent
// exists to close. The surviving row must be the most restrictive one.
func TestDedupePermissions_KeepsTheBlock(t *testing.T) {
	s := legacyStore(t)
	ctx := context.Background()

	// id=1 is older and allows delivery; id=2 is newer and blocks the sender.
	insertLegacyPermission(t, s, 1, "02alice", nil, "inbox", 0, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 2, "02alice", nil, "inbox", storage.FeeBlocked, "2026-06-01 00:00:00")

	deleted, err := s.DedupePermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	got := permissionRows(t, s)
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].RecipientFee != storage.FeeBlocked {
		t.Errorf("surviving RecipientFee = %d, want %d (the block must win)", got[0].RecipientFee, storage.FeeBlocked)
	}
}

// With no block in play, the recipient's most recent intent survives.
func TestDedupePermissions_KeepsTheMostRecent(t *testing.T) {
	s := legacyStore(t)

	insertLegacyPermission(t, s, 1, "02alice", nil, "inbox", 5, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 2, "02alice", nil, "inbox", 25, "2026-06-01 00:00:00")

	if _, err := s.DedupePermissions(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := permissionRows(t, s)
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].RecipientFee != 25 {
		t.Errorf("surviving RecipientFee = %d, want 25 (the latest update)", got[0].RecipientFee)
	}
}

func TestDedupePermissions_LeavesDistinctRowsAlone(t *testing.T) {
	s := legacyStore(t)
	bob := "02bob"

	insertLegacyPermission(t, s, 1, "02alice", nil, "inbox", 0, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 2, "02alice", &bob, "inbox", 7, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 3, "02alice", nil, "notifications", 10, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 4, "02bob", nil, "inbox", 0, "2026-01-01 00:00:00")

	deleted, err := s.DedupePermissions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if got := permissionRows(t, s); len(got) != 4 {
		t.Errorf("got %d rows, want 4", len(got))
	}
}

func TestDedupePermissions_Idempotent(t *testing.T) {
	s := legacyStore(t)
	ctx := context.Background()

	insertLegacyPermission(t, s, 1, "02alice", nil, "inbox", 0, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 2, "02alice", nil, "inbox", 0, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 3, "02alice", nil, "inbox", 0, "2026-01-01 00:00:00")

	first, err := s.DedupePermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != 2 {
		t.Errorf("first run deleted %d, want 2", first)
	}

	second, err := s.DedupePermissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Errorf("second run deleted %d, want 0", second)
	}
}

// EnsureSchema must refuse to proceed rather than delete rows behind the
// operator's back, and must say what to do about it.
func TestEnsureSchema_RefusesDuplicates(t *testing.T) {
	s := legacyStore(t)
	ctx := context.Background()

	insertLegacyPermission(t, s, 1, "02alice", nil, "inbox", 0, "2026-01-01 00:00:00")
	insertLegacyPermission(t, s, 2, "02alice", nil, "inbox", storage.FeeBlocked, "2026-06-01 00:00:00")

	err := s.EnsureSchema(ctx)
	if !errors.Is(err, ErrDuplicatePermissions) {
		t.Fatalf("EnsureSchema error = %v, want ErrDuplicatePermissions", err)
	}
	if got := permissionRows(t, s); len(got) != 2 {
		t.Errorf("EnsureSchema deleted rows: %d remain, want both 2 left untouched", len(got))
	}

	// After the operator runs the dedupe, startup proceeds.
	if _, err := s.DedupePermissions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema after dedupe: %v", err)
	}

	// And the constraint now holds, on a database that still carries the old
	// table-level UNIQUE alongside the new expression index.
	if err := s.SetPermissionIfAbsent(ctx, "02alice", nil, "inbox", 0); err != nil {
		t.Fatalf("SetPermissionIfAbsent on a migrated database: %v", err)
	}
	if err := s.SetPermission(ctx, "02alice", nil, "inbox", 99); err != nil {
		t.Fatalf("SetPermission on a migrated database: %v", err)
	}
	got := permissionRows(t, s)
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if got[0].RecipientFee != 99 {
		t.Errorf("RecipientFee = %d, want 99", got[0].RecipientFee)
	}
}
