package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad_Lookup(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PaymailDomain != "" || cfg.HandleCooldown != 30*24*time.Hour || cfg.LookupRatePerMin != 60 || cfg.TrustProxy || cfg.TrustedProxyHops != 1 || len(cfg.AdminIdentityKeys) != 0 {
		t.Errorf("defaults = %+v", cfg)
	}

	t.Setenv("PAYMAIL_DOMAIN", "Example.COM")
	if _, err := Load(); err == nil {
		t.Error("PAYMAIL_DOMAIN without PAYMAIL_HOST must fail")
	}

	t.Setenv("PAYMAIL_HOST", "https://mb.example.com/")
	t.Setenv("HANDLE_COOLDOWN_DAYS", "7")
	t.Setenv("ADMIN_IDENTITY_KEYS", " "+adminKeyA+" , "+strings.ToUpper(adminKeyB)+",")
	t.Setenv("LOOKUP_RATE_PER_MIN", "5")
	t.Setenv("TRUST_PROXY", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PaymailDomain != "example.com" || cfg.PaymailHost != "https://mb.example.com" {
		t.Errorf("domain/host = %q %q", cfg.PaymailDomain, cfg.PaymailHost)
	}
	if cfg.HandleCooldown != 7*24*time.Hour || cfg.LookupRatePerMin != 5 || !cfg.TrustProxy {
		t.Errorf("cfg = %+v", cfg)
	}
	if len(cfg.AdminIdentityKeys) != 2 || cfg.AdminIdentityKeys[0] != adminKeyA || cfg.AdminIdentityKeys[1] != adminKeyB {
		t.Errorf("admins = %v", cfg.AdminIdentityKeys)
	}
}

// The limiter is the only control in front of the public routes, so a setting
// it cannot make sense of has to leave the limit on. Exactly zero is the one
// value that turns it off, and the README says so in those words.
func TestLoad_LookupRatePerMin(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")

	for _, c := range []struct {
		value string
		want  int
	}{
		{"5", 5},
		{"0", 0},
		{"-1", 60},
		{"-100", 60},
		{"off", 60},
		{"", 60},
	} {
		t.Setenv("LOOKUP_RATE_PER_MIN", c.value)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.LookupRatePerMin != c.want {
			t.Errorf("LOOKUP_RATE_PER_MIN=%q gave %d, want %d", c.value, cfg.LookupRatePerMin, c.want)
		}
	}
}

// A hop count below one would count from the right end of a header the client
// controls outright, so it is read as one proxy rather than none.
func TestLoad_TrustedProxyHops(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")

	for _, c := range []struct {
		value string
		want  int
	}{
		{"2", 2},
		{"1", 1},
		{"0", 1},
		{"-3", 1},
		{"two", 1},
		{"", 1},
	} {
		t.Setenv("TRUSTED_PROXY_HOPS", c.value)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TrustedProxyHops != c.want {
			t.Errorf("TRUSTED_PROXY_HOPS=%q gave %d, want %d", c.value, cfg.TrustedProxyHops, c.want)
		}
	}
}

// Compressed public keys in the form BRC-104 identity keys take.
const (
	adminKeyA = "02aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	adminKeyB = "03112233445566778899aabbccddeeff112233445566778899aabbccddeeff1122"
)

func TestLoad_HandleCooldownClamp(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")

	for _, days := range []string{"200000", "9223372036854775807"} {
		t.Setenv("HANDLE_COOLDOWN_DAYS", days)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.HandleCooldown <= 0 {
			t.Errorf("HANDLE_COOLDOWN_DAYS=%s: cooldown = %v, want positive", days, cfg.HandleCooldown)
		}
		if cfg.HandleCooldown != maxCooldownDays*24*time.Hour {
			t.Errorf("HANDLE_COOLDOWN_DAYS=%s: cooldown = %v, want clamped to %d days", days, cfg.HandleCooldown, maxCooldownDays)
		}
	}
}

func TestLoad_TrustProxyParsing(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")

	cases := map[string]bool{
		"true":   true,
		"TRUE":   true,
		"True":   true,
		" true ": true,
		"false":  false,
		"":       false,
		"yes":    false,
	}
	for v, want := range cases {
		t.Setenv("TRUST_PROXY", v)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.TrustProxy != want {
			t.Errorf("TRUST_PROXY=%q: TrustProxy = %v, want %v", v, cfg.TrustProxy, want)
		}
	}
}

func TestLoad_PaymailDomainMustBeBare(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")
	t.Setenv("PAYMAIL_HOST", "https://mb.example.com")

	for _, domain := range []string{"https://Example.com/", "example.com:8080", "me@example.com", "example .com"} {
		t.Setenv("PAYMAIL_DOMAIN", domain)
		if _, err := Load(); err == nil {
			t.Errorf("PAYMAIL_DOMAIN=%q must be rejected", domain)
		}
	}

	t.Setenv("PAYMAIL_DOMAIN", "sub.Example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PaymailDomain != "sub.example.com" {
		t.Errorf("domain = %q", cfg.PaymailDomain)
	}
}

func TestLoad_AdminIdentityKeys(t *testing.T) {
	t.Setenv("SERVER_PRIVATE_KEY", "01")

	for _, keys := range []string{"02aa", "not-hex", adminKeyA + ",zz" + adminKeyB[2:], "04" + adminKeyA[2:]} {
		t.Setenv("ADMIN_IDENTITY_KEYS", keys)
		if _, err := Load(); err == nil {
			t.Errorf("ADMIN_IDENTITY_KEYS=%q must be rejected", keys)
		}
	}

	t.Setenv("ADMIN_IDENTITY_KEYS", adminKeyA+", "+strings.ToUpper(adminKeyA)+" ,"+adminKeyB)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AdminIdentityKeys) != 2 || cfg.AdminIdentityKeys[0] != adminKeyA || cfg.AdminIdentityKeys[1] != adminKeyB {
		t.Errorf("admins = %v, want deduplicated pair", cfg.AdminIdentityKeys)
	}
}
