// Package certtest builds signed profile certificates for tests. It must be
// imported only from _test.go files: it imports testing, which registers the
// test flags on the default FlagSet when linked into a binary.
package certtest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/auth/certificates"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

const profileType = "SbatVXXssDW3AO0J9bxIljkHGbPBGCVAXg94gXFf0cE="

// ZeroOutpoint is the only revocation outpoint a profile certificate may carry.
const ZeroOutpoint = "0000000000000000000000000000000000000000000000000000000000000000.0"

// Build returns an unsigned self-certificate the caller may tamper with before Sign.
func Build(t *testing.T, key *ec.PrivateKey, fields map[string]string) *certificates.Certificate {
	t.Helper()
	serial := make([]byte, 32)
	if _, err := rand.Read(serial); err != nil {
		t.Fatal(err)
	}
	op, err := transaction.OutpointFromString(ZeroOutpoint)
	if err != nil {
		t.Fatal(err)
	}
	f := map[wallet.CertificateFieldNameUnder50Bytes]wallet.StringBase64{}
	for k, v := range fields {
		f[wallet.CertificateFieldNameUnder50Bytes(k)] = wallet.StringBase64(v)
	}
	pub := key.PubKey()
	return certificates.NewCertificate(
		wallet.StringBase64(profileType),
		wallet.StringBase64(base64.StdEncoding.EncodeToString(serial)),
		*pub, *pub, op, f, nil,
	)
}

// Sign signs c with key and returns its JSON.
func Sign(t *testing.T, key *ec.PrivateKey, c *certificates.Certificate) []byte {
	t.Helper()
	w, err := wallet.NewCompletedProtoWallet(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Sign(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// New returns signed certificate JSON with the given fields.
func New(t *testing.T, key *ec.PrivateKey, fields map[string]string) []byte {
	t.Helper()
	return Sign(t, key, Build(t, key, fields))
}

// Profile returns a valid signed profile for handle@domain.
func Profile(t *testing.T, key *ec.PrivateKey, handle, domain string, issuedAt time.Time, extra map[string]string) []byte {
	t.Helper()
	fields := map[string]string{
		"paymail":  handle + "@" + domain,
		"issuedAt": issuedAt.UTC().Format(time.RFC3339Nano),
	}
	for k, v := range extra {
		fields[k] = v
	}
	return New(t, key, fields)
}
