package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminReleaseHandle(t *testing.T) {
	e := newLookupEnv(t) // AdminKeys = [mockIdentityKey]
	alice, bob := testKey(t), testKey(t)
	wantStatus(t, e.put(alice, "deggen", e.now(), nil), 201, "")

	call := func(caller, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		e.srv.adminReleaseHandle(w, httptest.NewRequest("POST", "/admin/handle/release", bytes.NewBufferString(body)), caller)
		return w
	}

	wantStatus(t, call("", `{"handle":"deggen"}`), 401, "ERR_AUTH_REQUIRED")
	wantStatus(t, call(bob.PubKey().ToDERHex(), `{"handle":"deggen"}`), 403, "ERR_NOT_ADMIN")
	wantStatus(t, call(mockIdentityKey, `{`), 400, "ERR_INVALID_REQUEST")
	wantStatus(t, call(mockIdentityKey, `{"handle":"nobody"}`), 404, "ERR_HANDLE_NOT_FOUND")

	// Default skips the cooldown: the user's new key claims at once. The body
	// reports the normalised handle, not the one the operator typed.
	released := call(mockIdentityKey, `{"handle":"Deggen"}`)
	wantStatus(t, released, 200, "")
	var body map[string]string
	if err := json.Unmarshal(released.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", released.Body, err)
	}
	if body["status"] != "success" || body["handle"] != "deggen" {
		t.Errorf("body = %v, want status success and handle deggen", body)
	}
	wantStatus(t, e.put(bob, "deggen", e.advance(time.Minute), nil), 201, "")

	// skipCooldown:false applies it, and a mixed-case admin key is canonicalised
	// once: it is authorised, and releasedBy records the lowercase spelling every
	// later lookup by identity key uses.
	wantStatus(t, call(strings.ToUpper(mockIdentityKey), `{"handle":"deggen","skipCooldown":false}`), 200, "")
	rec, err := e.srv.handles.GetHandle(context.Background(), "deggen")
	if err != nil || rec == nil || rec.ReleasedBy == nil || *rec.ReleasedBy != mockIdentityKey {
		t.Errorf("releasedBy = %v (err %v), want %s", rec, err, mockIdentityKey)
	}
	wantStatus(t, e.put(alice, "deggen", e.advance(time.Minute), nil), 409, "ERR_HANDLE_COOLDOWN")
	// bob is the key the operator just removed. A cooldown an operator asks for
	// is a quarantine on the handle, so it binds bob too — the alternative is a
	// window in which the only party who may take the handle back is the one
	// being removed from it.
	wantStatus(t, e.put(bob, "deggen", e.advance(time.Minute), nil), 409, "ERR_HANDLE_COOLDOWN")

	// The no-auth route wrapper rejects.
	w := httptest.NewRecorder()
	e.srv.AdminReleaseHandle(w, httptest.NewRequest("POST", "/admin/handle/release", bytes.NewBufferString(`{"handle":"deggen"}`)))
	wantStatus(t, w, 401, "ERR_AUTH_REQUIRED")
}
