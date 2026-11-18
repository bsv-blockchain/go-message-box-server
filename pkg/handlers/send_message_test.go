package handlers

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
)

// postSendMessage sends one message to recipient as sender.
func postSendMessage(t *testing.T, srv *Server, sender, recipient, messageID string) *httptest.ResponseRecorder {
	t.Helper()
	body := mustMarshal(t, map[string]any{"message": map[string]any{
		"recipient":  recipient,
		"messageBox": "payment_inbox",
		"messageId":  messageID,
		"body":       "ciphertext",
	}})
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/sendMessage", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.sendMessage(w, r, sender)
	return w
}

// The server's log is shipped to a third-party store, and logger.Error is never
// switched off, so a rejected message must not name who sent it, who it was for
// or its ID.
func TestSendMessage_DuplicateLogsNoIdentifiers(t *testing.T) {
	srv := setupTestServer(t)
	var out bytes.Buffer
	t.Cleanup(logger.SetOutput(&out))

	sender := testKey(t).PubKey().ToDERHex()
	const messageID = "msg-secret-id"

	wantStatus(t, postSendMessage(t, srv, sender, mockIdentityKey, messageID), 200, "")
	wantStatus(t, postSendMessage(t, srv, sender, mockIdentityKey, messageID), 400, "ERR_DUPLICATE_MESSAGE")

	got := out.String()
	// A run that logged nothing would pass the checks below for the wrong reason.
	if !strings.Contains(got, "duplicate message rejected") {
		t.Fatalf("log = %q, want the rejection logged", got)
	}
	for what, value := range map[string]string{
		"sender":     sender,
		"recipient":  mockIdentityKey,
		"message ID": messageID,
	} {
		if strings.Contains(got, value) {
			t.Errorf("log contains the %s %q:\n%s", what, value, got)
		}
	}
}

// MAX_MESSAGE_BODY_BYTES caps HTTP sends as the TS route does: 413
// ERR_MESSAGE_BODY_TOO_LARGE, measured in UTF-8 bytes of the string body.
func TestSendMessage_BodyLimit(t *testing.T) {
	srv := setupTestServer(t)
	srv.SetMaxMessageBodyBytes(4)
	sender, recipient := testKey(t).PubKey().ToDERHex(), testKey(t).PubKey().ToDERHex()
	send := func(id string, body any) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/sendMessage", bytes.NewReader(mustMarshal(t, map[string]any{"message": map[string]any{
			"recipient": recipient, "messageBox": "payment_inbox", "messageId": id, "body": body,
		}})))
		w := httptest.NewRecorder()
		srv.sendMessage(w, r, sender)
		return w
	}

	wantStatus(t, send("at-limit", "abcd"), 200, "")
	wantStatus(t, send("ascii", "abcde"), 413, "ERR_MESSAGE_BODY_TOO_LARGE")
	wantStatus(t, send("utf8", "ééé"), 413, "ERR_MESSAGE_BODY_TOO_LARGE") // 3 runes, 6 bytes
	wantStatus(t, send("object", map[string]any{"k": "v"}), 413, "ERR_MESSAGE_BODY_TOO_LARGE")

	srv.SetMaxMessageBodyBytes(-1)
	wantStatus(t, send("unlimited", strings.Repeat("x", 2<<20)), 200, "")
}
