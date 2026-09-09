package mongostore

import (
	"context"
	"os"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage/storagetest"
)

// newMongo returns a store against MONGO_TEST_URI with an empty database. It
// drops every collection first, so point it at a throwaway database.
func newMongo(t *testing.T, uri, database string) storage.Store {
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
	if err := s.EnsureSchema(ctx); err != nil {
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
