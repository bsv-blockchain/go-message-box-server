package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/bsv-blockchain/go-message-box-server/pkg/handles"
	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert"
	"github.com/bsv-blockchain/go-message-box-server/pkg/profilecert/certtest"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

const testDomain = "example.com"

// lookupEnv is a server with the lookup feature on, its registry the same fake
// store, and a clock the test moves by hand so cooldowns need no sleeping.
type lookupEnv struct {
	t   *testing.T
	srv *Server
	mux *http.ServeMux

	mu sync.Mutex
	// clock is read by handlers on whatever goroutine serves the request, so it
	// is guarded: read it with now, move it with set or advance, and never
	// touch the field directly.
	clock time.Time
}

func newLookupEnv(t *testing.T) *lookupEnv {
	t.Helper()
	e := &lookupEnv{t: t, srv: setupTestServer(t), clock: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	e.srv.EnableLookup(LookupConfig{Domain: testDomain, Host: "https://mb.example.com", Cooldown: 30 * 24 * time.Hour, AdminKeys: []string{mockIdentityKey}}, e.srv.Store.(*fakeStore))
	e.srv.now = e.now
	e.mux = e.srv.LookupRoutes()
	return e
}

// now is the server's clock.
func (e *lookupEnv) now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clock
}

// set moves the clock to t and returns it.
func (e *lookupEnv) set(t time.Time) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = t
	return e.clock
}

// advance moves the clock forward by d and returns the new time.
func (e *lookupEnv) advance(d time.Duration) time.Time {
	return e.set(e.now().Add(d))
}

func (e *lookupEnv) do(method, path string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.doReader(method, path, bytes.NewReader(body))
}

// doReader is do for bodies that are not a plain byte slice, so a test can fail
// the read part-way through.
func (e *lookupEnv) doReader(method, path string, body io.Reader) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, body))
	return w
}

func (e *lookupEnv) put(key *ec.PrivateKey, handle string, issuedAt time.Time, extra map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("PUT", "/api/handle", certtest.Profile(e.t, key, handle, testDomain, issuedAt, extra))
}

// errReader hands over one byte and then fails, the way a client that resets
// the connection mid-upload does.
type errReader struct{ sent bool }

func (r *errReader) Read(p []byte) (int, error) {
	if !r.sent && len(p) > 0 {
		r.sent = true
		p[0] = '{'
		return 1, nil
	}
	return 0, errors.New("connection reset by peer")
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body %s", w.Code, status, w.Body)
	}
	if code != "" {
		var e ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &e)
		if e.Code != code {
			t.Fatalf("code = %q, want %q", e.Code, code)
		}
	}
}

// wantDescription checks the human text as well as the code, for the codes two
// different operations share.
func wantDescription(t *testing.T, w *httptest.ResponseRecorder, substr string) {
	t.Helper()
	var e ErrorResponse
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	if !strings.Contains(e.Description, substr) {
		t.Errorf("description = %q, want it to contain %q", e.Description, substr)
	}
}

func testKey(t *testing.T) *ec.PrivateKey {
	t.Helper()
	k, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestWellKnown(t *testing.T) {
	e := newLookupEnv(t)
	w := e.do("GET", "/.well-known/bsvalias", nil)
	wantStatus(t, w, 200, "")
	var doc struct {
		Bsvalias     string            `json:"bsvalias"`
		Capabilities map[string]string `json:"capabilities"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Bsvalias != "1.0" ||
		doc.Capabilities["0ace65da5987"] != "https://mb.example.com/api/handle/{query}" ||
		doc.Capabilities["43dcf83ddc5f"] != "https://mb.example.com/api/identityKey/{pubkey}" {
		t.Errorf("doc = %+v", doc)
	}
}

func TestPutHandle_Lifecycle(t *testing.T) {
	e := newLookupEnv(t)
	alice, bob := testKey(t), testKey(t)
	t0 := e.now()

	wantStatus(t, e.put(alice, "deggen", t0, map[string]string{"displayName": "Deggen"}), 201, "")

	// A newer certificate updates; resubmitting it is a no-op.
	same := certtest.Profile(t, alice, "deggen", testDomain, t0.Add(time.Second), nil)
	wantStatus(t, e.do("PUT", "/api/handle", same), 200, "")
	wantStatus(t, e.do("PUT", "/api/handle", same), 200, "")

	// Conflicts. "deg.gen" folds to the skeleton of "deggen".
	wantStatus(t, e.put(bob, "deggen", t0, nil), 409, "ERR_HANDLE_TAKEN")
	wantStatus(t, e.put(bob, "deg.gen", t0, nil), 409, "ERR_HANDLE_TOO_SIMILAR")
	wantStatus(t, e.put(alice, "another", t0.Add(2*time.Second), nil), 409, "ERR_KEY_HAS_HANDLE")
	wantStatus(t, e.put(alice, "deggen", t0, nil), 409, "ERR_STALE_CERTIFICATE")

	// Releasing a handle held by someone else shares ERR_HANDLE_TAKEN with a
	// registration conflict, so the text has to say which one happened.
	notOwner := e.put(bob, "deggen", t0.Add(2*time.Second), map[string]string{"released": "true"})
	wantStatus(t, notOwner, 409, "ERR_HANDLE_TAKEN")
	wantDescription(t, notOwner, "only its owner can release it")

	// Forward lookup returns the bare certificate array.
	w := e.do("GET", "/api/handle/deggen@example.com", nil)
	wantStatus(t, w, 200, "")
	var certs []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &certs); err != nil || len(certs) != 1 {
		t.Fatalf("certs = %s", w.Body)
	}
	if certs[0]["subject"] != alice.PubKey().ToDERHex() || certs[0]["signature"] == nil {
		t.Errorf("cert = %v", certs[0])
	}
	if certs[0]["fields"].(map[string]any)["paymail"] != "deggen@example.com" {
		t.Errorf("fields = %v", certs[0]["fields"])
	}

	// Reverse lookup returns the same single document.
	w = e.do("GET", "/api/identityKey/"+alice.PubKey().ToDERHex(), nil)
	wantStatus(t, w, 200, "")
	var one map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || one["serialNumber"] != certs[0]["serialNumber"] {
		t.Errorf("reverse = %s", w.Body)
	}
	wantStatus(t, e.do("GET", "/api/identityKey/"+bob.PubKey().ToDERHex(), nil), 404, "ERR_HANDLE_NOT_FOUND")
	wantStatus(t, e.do("GET", "/api/identityKey/nothex", nil), 400, "ERR_INVALID_LOOKUP")

	// Tombstone. Replaying it is the no-op a replayed registration is: a client
	// whose response was lost retries, and must not be told its handle never
	// existed.
	tombstone := certtest.Profile(t, alice, "deggen", testDomain, e.set(t0.Add(time.Hour)), map[string]string{"released": "true"})
	wantStatus(t, e.do("PUT", "/api/handle", tombstone), 200, "")
	wantStatus(t, e.do("PUT", "/api/handle", tombstone), 200, "")
	wantStatus(t, e.do("GET", "/api/identityKey/"+alice.PubKey().ToDERHex(), nil), 404, "ERR_HANDLE_NOT_FOUND")
	if w := e.do("GET", "/api/handle/deggen", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("search after release = %s", w.Body)
	}
	// Tombstone for a handle the key does not hold.
	wantStatus(t, e.put(bob, "nobody", e.now(), map[string]string{"released": "true"}), 404, "ERR_HANDLE_NOT_FOUND")

	// Cooldown blocks bob, not alice; the old cert cannot be replayed.
	wantStatus(t, e.put(bob, "deggen", e.now().Add(time.Minute), nil), 409, "ERR_HANDLE_COOLDOWN")
	wantStatus(t, e.do("PUT", "/api/handle", same), 409, "ERR_STALE_CERTIFICATE")
	wantStatus(t, e.put(bob, "deggen", e.advance(31*24*time.Hour), nil), 201, "")
}

// TestPutHandle_ReleaseIgnoresClaimTimeValidation models a handle that was
// valid when claimed but would fail today's format/reserved-word check — e.g.
// the reserved list grew after it was registered. Release must still work:
// Validate is a claim-time gate, not a precondition for giving a handle back.
func TestPutHandle_ReleaseIgnoresClaimTimeValidation(t *testing.T) {
	e := newLookupEnv(t)
	alice := testKey(t)
	t0 := e.now()

	// Seeded directly in the registry, bypassing PutHandle's Validate call, the
	// way a handle claimed under an older, looser rule set would already exist.
	cert := certtest.Profile(t, alice, "admin", testDomain, t0, nil)
	p, err := profilecert.Parse(t.Context(), cert, testDomain, t0)
	if err != nil {
		t.Fatal(err)
	}
	fake := e.srv.Store.(*fakeStore)
	if _, err := fake.ClaimHandle(t.Context(), storage.HandleClaim{
		Handle: p.Handle, Skeleton: handles.Skeleton(p.Handle), IdentityKey: p.IdentityKey,
		Certificate: p.JSON, SerialNumber: p.SerialNumber, IssuedAt: p.IssuedAt, Now: t0,
	}); err != nil {
		t.Fatal(err)
	}

	tombstone := certtest.Profile(t, alice, "admin", testDomain, t0.Add(time.Second), map[string]string{"released": "true"})
	w := e.do("PUT", "/api/handle", tombstone)
	wantStatus(t, w, 200, "")
}

func TestLookupRoutes_RequiresEnable(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("LookupRoutes without EnableLookup did not panic")
		}
	}()
	setupTestServer(t).LookupRoutes()
}

func TestPutHandle_Rejects(t *testing.T) {
	e := newLookupEnv(t)
	key := testKey(t)
	now := e.now()

	wantStatus(t, e.do("PUT", "/api/handle", []byte("{")), 400, "ERR_INVALID_CERTIFICATE")
	wantStatus(t, e.do("PUT", "/api/handle", bytes.Repeat([]byte("a"), 20000)), 413, "ERR_BODY_TOO_LARGE")
	// A body that dies mid-read is a bad request, not an oversized one.
	wantStatus(t, e.doReader("PUT", "/api/handle", &errReader{}), 400, "ERR_INVALID_CERTIFICATE")
	wantStatus(t, e.do("PUT", "/api/handle", certtest.Profile(t, key, "deggen", "evil.com", now, nil)), 400, "ERR_WRONG_DOMAIN")
	wantStatus(t, e.put(key, "ab", now, nil), 400, "ERR_INVALID_HANDLE")
	wantStatus(t, e.put(key, "deg+gen", now, nil), 400, "ERR_INVALID_HANDLE")
	wantStatus(t, e.put(key, "adm1n", now, nil), 409, "ERR_HANDLE_RESERVED")

	// issuedAt is the replay guard every later write has to beat, so a
	// certificate from the future would settle this handle's claims for good.
	// The allowance itself still registers: a client's clock is never exact.
	wantStatus(t, e.put(key, "future", now.Add(time.Hour), nil), 400, "ERR_INVALID_CERTIFICATE")
	wantStatus(t, e.put(key, "future", now.AddDate(100, 0, 0), nil), 400, "ERR_INVALID_CERTIFICATE")
	wantStatus(t, e.put(key, "future", now.Add(profilecert.MaxClockSkew), nil), 201, "")

	var m map[string]any
	_ = json.Unmarshal(certtest.Profile(t, key, "deggen", testDomain, now, nil), &m)
	m["fields"].(map[string]any)["paymail"] = "victim@example.com"
	forged, _ := json.Marshal(m)
	wantStatus(t, e.do("PUT", "/api/handle", forged), 400, "ERR_INVALID_CERTIFICATE")
}

func TestSearch(t *testing.T) {
	e := newLookupEnv(t)
	for _, h := range []string{"deggen", "deg", "degas", "a_deg", "d3gx", "zed"} {
		wantStatus(t, e.put(testKey(t), h, e.now(), nil), 201, "")
	}
	for i := 0; i < 12; i++ {
		wantStatus(t, e.put(testKey(t), fmt.Sprintf("many%02d", i), e.now(), nil), 201, "")
	}

	search := func(q string) []string {
		t.Helper()
		w := e.do("GET", "/api/handle/"+q, nil)
		wantStatus(t, w, 200, "")
		var certs []struct {
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &certs); err != nil {
			t.Fatalf("%s: %v", w.Body, err)
		}
		out := []string{}
		for _, c := range certs {
			out = append(out, strings.TrimSuffix(c.Fields["paymail"], "@"+testDomain))
		}
		return out
	}
	eq := func(label string, got, want []string) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
	}

	// Tiers: exact, handle prefix, skeleton prefix, substring (3+ chars).
	eq("deg", search("deg"), []string{"deg", "degas", "deggen", "a_deg"})
	eq("upper + domain", search("DEG@Example.com"), []string{"deg", "degas", "deggen", "a_deg"})
	eq("skeleton typo", search("de9gen"), []string{})
	eq("skeleton fold", search("d.e.g.g.e.n"), []string{"deggen"})
	eq("lookalike digits", search("z3d"), []string{})
	eq("fold 2->z", search("2ed"), []string{"zed"})
	eq("too short", search("d"), []string{})
	eq("other domain", search("deg@evil.com"), []string{})
	// A domain still being typed is a prefix of ours, so it does not suppress
	// results; only a domain that cannot become ours does.
	eq("partial domain", search("deg@e"), []string{"deg", "degas", "deggen", "a_deg"})
	eq("bare @", search("deg@"), []string{"deg", "degas", "deggen", "a_deg"})
	eq("wrong first letter", search("deg@f"), []string{})
	eq("bad chars", search("de%25"), []string{})
	if got := search("many"); len(got) != 10 {
		t.Errorf("limit: %d results, want 10", len(got))
	}
	// Substring tier needs three characters.
	eq("two-char substring", search("_d"), []string{})

	// The longest handle there can be is still a query; anything longer cannot
	// match one, and without the bound it would reach the registry as an
	// unanchored pattern scanned against every row.
	longest := strings.Repeat("a", maxQueryLength)
	wantStatus(t, e.put(testKey(t), longest, e.now(), nil), 201, "")
	eq("longest allowed query", search(longest), []string{longest})
	eq("over-long query", search(longest+"a"), []string{})
	eq("far over-long query", search(strings.Repeat("b", 2000)), []string{})
}

func TestAvailable(t *testing.T) {
	e := newLookupEnv(t)
	key := testKey(t)
	wantStatus(t, e.put(key, "deggen", e.now(), nil), 201, "")

	check := func(handle string, available bool, reason string) {
		t.Helper()
		w := e.do("GET", "/api/handle/available/"+handle, nil)
		wantStatus(t, w, 200, "")
		var r AvailabilityResponse
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		if r.Available != available || r.Reason != reason {
			t.Errorf("%s = %+v, want %v %q", handle, r, available, reason)
		}
	}
	check("free", true, "")
	check("deggen", false, "taken")
	check("deg.gen", false, "too_similar")
	check("adm1n", false, "reserved")
	check("x", false, "invalid")

	// Dropping the handle is a mistake, not a search for the handle
	// "available", which is reserved and can never be registered.
	for _, p := range []string{"/api/handle/available", "/api/handle/available/"} {
		wantStatus(t, e.do("GET", p, nil), 400, "ERR_INVALID_LOOKUP")
	}

	wantStatus(t, e.put(key, "deggen", e.advance(time.Hour), map[string]string{"released": "true"}), 200, "")
	check("deggen", false, "cooldown")
	check("deg.gen", false, "too_similar")
	e.advance(31 * 24 * time.Hour)
	check("deggen", true, "")
}

// The advisory answer has to agree with PUT. While a released row's issuedAt is
// not yet in the past, a certificate dated now cannot beat it, so the handle is
// not claimable however empty the row looks — and a caller told it is free has
// no way to act on that.
func TestAvailable_StaleWhileTheReplayGuardStands(t *testing.T) {
	e := newLookupEnv(t)
	e.srv.lookup.Cooldown = 0 // a cooldown would answer first and mask the guard
	owner := testKey(t)

	check := func(want bool, reason string) {
		t.Helper()
		w := e.do("GET", "/api/handle/available/deggen", nil)
		wantStatus(t, w, 200, "")
		var r AvailabilityResponse
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		if r.Available != want || r.Reason != reason {
			t.Errorf("available = %+v, want %v %q", r, want, reason)
		}
	}

	wantStatus(t, e.put(owner, "deggen", e.now(), nil), 201, "")
	wantStatus(t, e.put(owner, "deggen", e.advance(time.Hour), map[string]string{"released": "true"}), 200, "")

	// The tombstone's own issuedAt is the row's guard, so at this instant
	// nothing can be issued that beats it.
	check(false, "stale")
	wantStatus(t, e.put(testKey(t), "deggen", e.now(), nil), 409, "ERR_STALE_CERTIFICATE")

	e.advance(time.Millisecond)
	check(true, "")
	wantStatus(t, e.put(testKey(t), "deggen", e.now(), nil), 201, "")
}

func TestPutHandle_ConcurrentOneWinner(t *testing.T) {
	e := newLookupEnv(t)
	const n = 12
	codes := make(chan int, n)
	// Twelve distinct handles, one skeleton ("paypal").
	variants := []string{"paypal", "pay.pal", "pay-pal", "pay_pal", "p.aypal", "pa.ypal",
		"payp.al", "paypa.l", "paypa1", "paypai", "p-aypal", "pa-ypal"}
	for _, h := range variants {
		body := certtest.Profile(t, testKey(t), h, testDomain, e.now(), nil)
		go func() { codes <- e.do("PUT", "/api/handle", body).Code }()
	}
	created := 0
	for i := 0; i < n; i++ {
		if c := <-codes; c == 201 {
			created++
		} else if c != 409 {
			t.Errorf("unexpected status %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("%d created, want 1", created)
	}
}
