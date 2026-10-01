package main

import (
	"testing"

	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs"
)

func TestParseNetwork(t *testing.T) {
	for in, want := range map[string]defs.BSVNetwork{
		"mainnet": defs.NetworkMainnet,
		"main":    defs.NetworkMainnet,
		"Mainnet": defs.NetworkMainnet,
		"testnet": defs.NetworkTestnet,
		"test":    defs.NetworkTestnet,
		"ttn":     defs.NetworkTTN,
		"tstn":    defs.NetworkTSTN,
		"TSTN":    defs.NetworkTSTN,
	} {
		got, err := parseNetwork(in)
		if err != nil {
			t.Errorf("parseNetwork(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseNetwork(%q) = %q, want %q", in, got, want)
		}
	}

	for _, in := range []string{"", "regtest", "teratestnet"} {
		if got, err := parseNetwork(in); err == nil {
			t.Errorf("parseNetwork(%q) = %q, want an error", in, got)
		}
	}
}
