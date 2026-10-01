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

func postRegisterDevice(t *testing.T, srv *Server, identityKey string, req map[string]any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/registerDevice", bytes.NewReader(body))
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

	r := httptest.NewRequest("GET", "/devices", nil)
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
