package sqlstore

import (
	"context"
	"os"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// newPostgres returns a store against POSTGRES_TEST_DSN with a freshly created
// schema. It drops the tables first, so point it at a throwaway database.
func newPostgres(t *testing.T, dsn string) storage.Store {
	t.Helper()
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
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConformance_Postgres(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	storagetest.RunStoreTests(t, func(t *testing.T) storage.Store {
		return newPostgres(t, dsn)
	})
}
