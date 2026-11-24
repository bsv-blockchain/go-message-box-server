package p2p

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/stretchr/testify/require"
)

// The vector comes from @bsv/sdk 4.0.1, the library the wallet uses to
// internalize the payment, so a mismatch here means the wallet could not
// spend what the server pays to.
func TestDeriveWithMatchesTSVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/vector.json")
	require.NoError(t, err)
	var v struct {
		RecipientIdentityKey string `json:"recipientIdentityKey"`
		Prefix               string `json:"prefix"`
		Suffix               string `json:"suffix"`
		Script               string `json:"script"`
	}
	require.NoError(t, json.Unmarshal(raw, &v))

	got, err := DeriveWith(v.RecipientIdentityKey, v.Prefix, v.Suffix)
	require.NoError(t, err)
	require.Equal(t, v.Script, got)
}

func TestRecipientCanSpend(t *testing.T) {
	priv, err := ec.NewPrivateKey()
	require.NoError(t, err)

	d, err := Derive(priv.PubKey().ToDERHex(), rand.Reader)
	require.NoError(t, err)

	_, anyonePub := wallet.AnyoneKey()
	key, err := wallet.NewKeyDeriver(priv).DerivePrivateKey(Protocol, d.Prefix+" "+d.Suffix,
		wallet.Counterparty{Type: wallet.CounterpartyTypeOther, Counterparty: anyonePub})
	require.NoError(t, err)
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), true)
	require.NoError(t, err)
	lock, err := p2pkh.Lock(addr)
	require.NoError(t, err)
	require.Equal(t, lock.String(), d.Script)
}

func TestDeriveRandomness(t *testing.T) {
	priv, err := ec.NewPrivateKey()
	require.NoError(t, err)
	key := priv.PubKey().ToDERHex()

	a, err := Derive(key, rand.Reader)
	require.NoError(t, err)
	b, err := Derive(key, rand.Reader)
	require.NoError(t, err)
	require.NotEqual(t, a.Prefix, b.Prefix)
	require.NotEqual(t, a.Suffix, b.Suffix)
	require.NotEqual(t, a.Script, b.Script)
	for _, s := range []string{a.Prefix, a.Suffix} {
		raw, err := base64.StdEncoding.DecodeString(s)
		require.NoError(t, err)
		require.Len(t, raw, 16)
	}
}

func TestDeriveRejectsBadKey(t *testing.T) {
	_, err := Derive("02zz", rand.Reader)
	require.Error(t, err)
	// 33 bytes with a valid prefix byte that is not a point on the curve.
	notPoint := "02" + hex.EncodeToString(make([]byte, 32))
	_, err = Derive(notPoint, rand.Reader)
	require.Error(t, err)
}

func TestAnonymousSenderKey(t *testing.T) {
	require.Equal(t, "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", AnyonePublicKeyHex())
}
