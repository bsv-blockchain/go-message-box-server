package handlers

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER.
const maxSafeInteger = 1<<53 - 1

// isPositiveSafeInteger is @bsv/message-box-client's check on device IDs:
// Number.isSafeInteger(v) && v >= 1.
func isPositiveSafeInteger(v any) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	i, err := n.Int64()
	return err == nil && i >= 1 && i <= maxSafeInteger
}

// decodeJSON decodes a response body with numbers kept exact, so a test can
// tell 1 from 1.5 the way Number.isSafeInteger does.
func decodeJSON(t *testing.T, body *bytes.Buffer) map[string]any {
	t.Helper()
	dec := json.NewDecoder(body)
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

// otherIdentityKey is a second wallet profile on the same install.
const otherIdentityKey = "03b3f3bd6ee3d2d0d6d1a2b1f4c0a6d1b6a1f3c1e2d4a5b6c7d8e9f001122334455"

func postRegisterDevice(t *testing.T, srv *Server, identityKey string, req map[string]any) map[string]any {
	t.Helper()
	body := mustMarshal(t, req)
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/registerDevice", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.registerDevice(w, r, identityKey)
	if w.Code != 200 {
		t.Fatalf("registerDevice status = %d, want 200; body %s", w.Code, w.Body)
	}
	return decodeJSON(t, w.Body)
}

// TestRegisterDevice_ResponseSatisfiesClient pins the response shape that
// @bsv/message-box-client's registerDevice accepts. Without deviceId the
// client threw "Device-registration response is invalid." after every
// successful registration, so the wallet never recorded it and re-registered
// on every start.
func TestRegisterDevice_ResponseSatisfiesClient(t *testing.T) {
	srv := setupTestServer(t)

	got := postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "tok-1", "platform": "android"})
	if got["status"] != "success" {
		t.Errorf("status = %v, want success", got["status"])
	}
	if _, ok := got["message"].(string); !ok {
		t.Errorf("message = %#v, want a string", got["message"])
	}
	if !isPositiveSafeInteger(got["deviceId"]) {
		t.Fatalf("deviceId = %#v, want a positive safe integer", got["deviceId"])
	}

	// Re-registering the token, as the wallet does on token refresh, reports
	// the same registration.
	again := postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "tok-1", "platform": "android"})
	if again["deviceId"] != got["deviceId"] {
		t.Errorf("re-register deviceId = %v, want %v", again["deviceId"], got["deviceId"])
	}
	other := postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "tok-2"})
	if other["deviceId"] == got["deviceId"] {
		t.Errorf("second token deviceId = %v, want it distinct from %v", other["deviceId"], got["deviceId"])
	}
}

// TestListDevices_ResponseSatisfiesClient pins the record shape that
// @bsv/message-box-client's listRegisteredDevices accepts: a positive integer
// id, and deviceId and platform present as null when unset rather than
// omitted.
func TestListDevices_ResponseSatisfiesClient(t *testing.T) {
	srv := setupTestServer(t)
	reg := postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "token-without-extras"})
	postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "tok-2", "deviceId": "pixel", "platform": "android"})

	r := httptest.NewRequestWithContext(t.Context(), "GET", "/devices", nil)
	w := httptest.NewRecorder()
	srv.listDevices(w, r, mockIdentityKey)
	if w.Code != 200 {
		t.Fatalf("listDevices status = %d, want 200; body %s", w.Code, w.Body)
	}
	got := decodeJSON(t, w.Body)
	if got["status"] != "success" {
		t.Errorf("status = %v, want success", got["status"])
	}
	list, ok := got["devices"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("devices = %#v, want two records", got["devices"])
	}

	byToken := map[string]map[string]any{}
	for _, item := range list {
		d, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("device = %#v, want an object", item)
		}
		if !isPositiveSafeInteger(d["id"]) {
			t.Errorf("id = %#v, want a positive safe integer", d["id"])
		}
		for _, key := range []string{"deviceId", "platform"} {
			v, present := d[key]
			if !present {
				t.Errorf("%s missing, want it present (null when unset)", key)
				continue
			}
			if _, isString := v.(string); v != nil && !isString {
				t.Errorf("%s = %#v, want a string or null", key, v)
			}
		}
		if _, ok := d["active"].(bool); !ok {
			t.Errorf("active = %#v, want a boolean", d["active"])
		}
		for _, key := range []string{"createdAt", "updatedAt", "lastUsed"} {
			s, _ := d[key].(string)
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				t.Errorf("%s = %#v, want a timestamp", key, d[key])
			}
		}
		token, _ := d["fcmToken"].(string)
		byToken[token] = d
	}

	bare := byToken["...out-extras"]
	if bare == nil {
		t.Fatalf("no record for the masked bare token in %v", byToken)
	}
	if bare["id"] != reg["deviceId"] {
		t.Errorf("listed id = %v, want the registered deviceId %v", bare["id"], reg["deviceId"])
	}
	if bare["deviceId"] != nil || bare["platform"] != nil {
		t.Errorf("unset deviceId/platform = %v/%v, want null", bare["deviceId"], bare["platform"])
	}
	if full := byToken["tok-2"]; full == nil || full["deviceId"] != "pixel" || full["platform"] != "android" {
		t.Errorf("tok-2 record = %v, want deviceId pixel and platform android", full)
	}
}

// listedTokens returns the (masked) tokens the identity sees on /devices.
func listedTokens(t *testing.T, srv *Server, identityKey string) []string {
	t.Helper()
	w := httptest.NewRecorder()
	srv.listDevices(w, httptest.NewRequestWithContext(t.Context(), "GET", "/devices", nil), identityKey)
	if w.Code != 200 {
		t.Fatalf("listDevices status = %d, want 200; body %s", w.Code, w.Body)
	}
	devices := decodeJSON(t, w.Body)["devices"].([]any)
	out := make([]string, 0, len(devices))
	for _, item := range devices {
		out = append(out, item.(map[string]any)["fcmToken"].(string))
	}
	return out
}

func postUnregisterDevice(t *testing.T, srv *Server, identityKey, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/unregisterDevice", bytes.NewReader([]byte(rawBody)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.unregisterDevice(w, r, identityKey)
	return w
}

// A wallet with several profiles registers the same FCM token once per profile.
// The second registration must not take the token from the first, or only the
// last profile registered would ever be pushed to.
func TestRegisterDevice_TwoIdentitiesShareOneToken(t *testing.T) {
	srv := setupTestServer(t)

	first := postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "shared-token-0001", "platform": "ios"})
	second := postRegisterDevice(t, srv, otherIdentityKey, map[string]any{"fcmToken": "shared-token-0001", "platform": "ios"})
	if first["deviceId"] == second["deviceId"] {
		t.Errorf("both identities got deviceId %v, want one registration each", first["deviceId"])
	}

	for _, identity := range []string{mockIdentityKey, otherIdentityKey} {
		if got := listedTokens(t, srv, identity); len(got) != 1 || got[0] != "...token-0001" {
			t.Errorf("%s lists %v, want the shared token", identity[:6], got)
		}
	}
}

func TestUnregisterDevice_RemovesOnlyTheCallersRegistration(t *testing.T) {
	srv := setupTestServer(t)
	postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "shared-token-0001"})
	postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "other-token-0002"})
	postRegisterDevice(t, srv, otherIdentityKey, map[string]any{"fcmToken": "shared-token-0001"})

	w := postUnregisterDevice(t, srv, mockIdentityKey, `{"fcmToken":"shared-token-0001"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	got := decodeJSON(t, w.Body)
	if got["status"] != "success" {
		t.Errorf("status = %v, want success", got["status"])
	}

	if tokens := listedTokens(t, srv, mockIdentityKey); len(tokens) != 1 || tokens[0] != "...token-0002" {
		t.Errorf("caller lists %v, want only the other token", tokens)
	}
	if tokens := listedTokens(t, srv, otherIdentityKey); len(tokens) != 1 {
		t.Errorf("the other identity lists %v, want its registration of the shared token left alone", tokens)
	}
}

// Idempotent: the wallet unregisters on profile removal and again on wallet
// deletion, and neither may fail because the row is already gone.
func TestUnregisterDevice_IsIdempotent(t *testing.T) {
	srv := setupTestServer(t)
	postRegisterDevice(t, srv, mockIdentityKey, map[string]any{"fcmToken": "tok-1"})

	for i, body := range []string{
		`{"fcmToken":"tok-1"}`,
		`{"fcmToken":"tok-1"}`,     // already gone
		`{"fcmToken":"never-was"}`, // never registered
	} {
		w := postUnregisterDevice(t, srv, mockIdentityKey, body)
		if w.Code != 200 {
			t.Fatalf("call %d: status = %d, want 200; body %s", i, w.Code, w.Body)
		}
		if got := decodeJSON(t, w.Body); got["status"] != "success" {
			t.Errorf("call %d: status = %v, want success", i, got["status"])
		}
	}
	if tokens := listedTokens(t, srv, mockIdentityKey); len(tokens) != 0 {
		t.Errorf("lists %v after unregistering, want none", tokens)
	}
}

func TestUnregisterDevice_RejectsBadBodies(t *testing.T) {
	srv := setupTestServer(t)

	for name, tc := range map[string]struct {
		body string
		code string
	}{
		"empty token":   {`{"fcmToken":""}`, "ERR_INVALID_FCM_TOKEN"},
		"missing token": {`{}`, "ERR_INVALID_FCM_TOKEN"},
		"not json":      {`nope`, "ERR_INVALID_JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			w := postUnregisterDevice(t, srv, mockIdentityKey, tc.body)
			if w.Code != 400 {
				t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body)
			}
			if got := decodeJSON(t, w.Body); got["code"] != tc.code {
				t.Errorf("code = %v, want %s", got["code"], tc.code)
			}
		})
	}
}

func TestUnregisterDeviceHandler_NoAuth(t *testing.T) {
	srv := setupTestServer(t)

	req := httptest.NewRequestWithContext(t.Context(), "POST", "/unregisterDevice", bytes.NewReader([]byte(`{"fcmToken":"tok-1"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.UnregisterDevice(w, req)

	if w.Code != 401 {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
