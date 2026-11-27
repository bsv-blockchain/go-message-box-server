package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
)

// captureLog sends the server log to a buffer for the length of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	t.Cleanup(logger.SetOutput(&buf))
	return &buf
}

func TestPaymailRequestLog_LogsSuccess(t *testing.T) {
	buf := captureLog(t)
	h := PaymailRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"name": "rosa"})
	}))
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/bsvalias/public-profile/rosa@example.com", nil)
	r.Header.Set("User-Agent", "HandCash/1.0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "rosa") {
		t.Fatalf("response changed: %d %q", w.Code, w.Body.String())
	}
	out := buf.String()
	for _, want := range []string{
		`msg="paymail request"`, "method=GET",
		"path=/api/bsvalias/public-profile/rosa@example.com",
		"status=200", "user_agent=HandCash/1.0", "duration_ms=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q has no %q", out, want)
		}
	}
	if strings.Contains(out, "code=") {
		t.Errorf("log %q has an error code for a 200", out)
	}
}

// Production does not call logger.Enable, so the record must not depend on it.
func TestPaymailRequestLog_LogsWhenDebugLogIsOff(t *testing.T) {
	buf := captureLog(t)
	was := logger.IsEnabled()
	logger.Disable()
	t.Cleanup(func() {
		if was {
			logger.Enable()
		}
	})
	PaymailRequestLog(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), "GET", "/.well-known/bsvalias", nil))
	if !strings.Contains(buf.String(), "status=404") {
		t.Errorf("log %q, want the 404 record", buf.String())
	}
}

func TestPaymailRequestLog_LogsErrorCode(t *testing.T) {
	buf := captureLog(t)
	h := PaymailRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "ERR_UNKNOWN_REFERENCE", "unknown or expired reference")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "POST",
		"/api/bsvalias/receive-transaction/rosa@example.com", strings.NewReader(`{"hex":"00","reference":"secret-note"}`)))

	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "ERR_UNKNOWN_REFERENCE") {
		t.Fatalf("response changed: %d %q", w.Code, w.Body.String())
	}
	out := buf.String()
	if !strings.Contains(out, "status=404") || !strings.Contains(out, "code=ERR_UNKNOWN_REFERENCE") {
		t.Errorf("log %q, want status 404 and the error code", out)
	}
	if strings.Contains(out, "secret-note") || strings.Contains(out, "expired reference") {
		t.Errorf("log %q copies the request or response body", out)
	}
}

func TestPaymailRequestLog_BoundsWhatItCopies(t *testing.T) {
	buf := captureLog(t)
	h := PaymailRequestLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4*maxLoggedErrorBody))
	}))
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/.well-known/bsvalias", nil)
	r.Header.Set("User-Agent", strings.Repeat("u", 1000))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Body.Len() != 4*maxLoggedErrorBody {
		t.Errorf("body = %d bytes, the log must not cut the response", w.Body.Len())
	}
	if strings.Contains(buf.String(), strings.Repeat("u", maxLoggedUserAgent+1)) {
		t.Error("user agent is not cut to the limit")
	}
	if strings.Contains(buf.String(), "code=") {
		t.Errorf("log %q has a code from a body that is not an error document", buf.String())
	}
}

func TestPaymailLogWriter_KeepsFirstStatus(t *testing.T) {
	w := &paymailLogWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	w.WriteHeader(http.StatusTooManyRequests)
	w.WriteHeader(http.StatusOK)
	if w.status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the first one, 429", w.status)
	}
}
