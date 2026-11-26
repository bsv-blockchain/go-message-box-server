// Package p2p holds the pure parts of paymail P2P receive: the BRC-29
// destination derivation and the inbox token a wallet reads the payment from.
package p2p

import (
	"encoding/base64"
	"fmt"
	"io"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/bsv-blockchain/go-sdk/wallet"
)

// Protocol is the BRC-29 wallet payment protocol.
var Protocol = wallet.Protocol{SecurityLevel: 2, Protocol: "3241645161d8"}

// AnonymousSender is the sender of every inbox message this feature writes.
// The legacy paymail sender has no BRC-100 identity, so the payment is
// derived from the well-known "anyone" key (private key 1).
func AnonymousSender() *ec.PublicKey {
	_, pub := wallet.AnyoneKey()
	return pub
}

// AnyonePublicKeyHex is the compressed hex of the anyone public key.
func AnyonePublicKeyHex() string { return AnonymousSender().ToDERHex() }

// Derived is one destination: the locking script and the BRC-29 key ID parts
// the recipient needs to find the matching private key.
type Derived struct{ Script, Prefix, Suffix string }

// Derive makes a fresh destination for the recipient. The prefix and suffix
// are 16 random bytes each so two destinations never share a script.
func Derive(recipientIdentityKey string, rand io.Reader) (Derived, error) {
	prefix, err := randomB64(rand)
	if err != nil {
		return Derived{}, err
	}
	suffix, err := randomB64(rand)
	if err != nil {
		return Derived{}, err
	}
	s, err := DeriveWith(recipientIdentityKey, prefix, suffix)
	if err != nil {
		return Derived{}, err
	}
	return Derived{Script: s, Prefix: prefix, Suffix: suffix}, nil
}

// DeriveWith returns the P2PKH script hex for given prefix and suffix. It is
// deterministic, so the wallet and the server agree on the script.
func DeriveWith(recipientIdentityKey, prefix, suffix string) (string, error) {
	recipient, err := ec.PublicKeyFromString(recipientIdentityKey)
	if err != nil {
		return "", fmt.Errorf("recipient identity key: %w", err)
	}
	anyonePriv, _ := wallet.AnyoneKey()
	pub, err := wallet.NewKeyDeriver(anyonePriv).DerivePublicKey(Protocol, prefix+" "+suffix,
		wallet.Counterparty{Type: wallet.CounterpartyTypeOther, Counterparty: recipient}, false)
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	addr, err := script.NewAddressFromPublicKey(pub, true)
	if err != nil {
		return "", err
	}
	lock, err := p2pkh.Lock(addr)
	if err != nil {
		return "", err
	}
	return lock.String(), nil
}

func randomB64(r io.Reader) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
