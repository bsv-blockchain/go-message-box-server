package firebase

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"firebase.google.com/go/v4/messaging"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// The log is shipped to a third-party store, so a delivery must not say who it
// was for. These are the values that identify a user or a device.
var (
	logRecipient  = "02" + strings.Repeat("ab", 32)
	logOriginator = "03" + strings.Repeat("cd", 32)
)

const (
	logMessageID = "msg-secret-id"
	logToken     = "fcm-registration-token-0123456789-TAIL123456"
	logTokenTail = "TAIL123456"
)

// captureLog turns the server's logger on and returns what it writes.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	restore := logger.SetOutput(&out)
	logger.Enable()
	t.Cleanup(func() {
		logger.Disable()
		restore()
	})
	return &out
}

// withSender replaces the FCM call for the length of the test.
func withSender(t *testing.T, send func(context.Context, *messaging.Message) (string, error)) {
	t.Helper()
	original := sendFCM
	sendFCM = send
	t.Cleanup(func() { sendFCM = original })
}

func TestSendFCMNotification_LogsNoIdentifiers(t *testing.T) {
	payload := FCMPayload{
		Title:      "Payment received",
		MessageID:  logMessageID,
		Originator: logOriginator,
		Recipient:  logRecipient,
		MessageBox: "payment_inbox",
	}
	device := storage.Device{FCMToken: logToken, Active: true}
	delivered := func(context.Context, *messaging.Message) (string, error) { return "projects/p/messages/1", nil }
	refused := func(context.Context, *messaging.Message) (string, error) { return "", errors.New("fcm unavailable") }

	tests := []struct {
		name        string
		store       *fakeDeviceStore
		send        func(context.Context, *messaging.Message) (string, error)
		wantLogged  string
		wantSuccess bool
	}{
		{"no devices", &fakeDeviceStore{}, nil, "No active devices found", false},
		{"device lookup fails", &fakeDeviceStore{listErr: errors.New("db down")}, nil, "Failed to get devices", false},
		{"send succeeds", &fakeDeviceStore{devices: []storage.Device{device}}, delivered, "Send complete", true},
		{"send fails", &fakeDeviceStore{devices: []storage.Device{device}}, refused, "Failed to send", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withClient(t)
			if tt.send != nil {
				withSender(t, tt.send)
			}
			out := captureLog(t)

			result := SendFCMNotification(context.Background(), tt.store, logRecipient, payload)
			if result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v (error %q)", result.Success, tt.wantSuccess, result.Error)
			}

			got := out.String()
			// A run that logged nothing would pass the checks below for the wrong reason.
			if !strings.Contains(got, tt.wantLogged) {
				t.Fatalf("log = %q, want it to contain %q", got, tt.wantLogged)
			}
			if !strings.Contains(got, "messageBox=payment_inbox") {
				t.Errorf("log = %q, want the message box kept", got)
			}

			for what, value := range map[string]string{
				"recipient":      logRecipient,
				"originator":     logOriginator,
				"message ID":     logMessageID,
				"FCM token":      logToken,
				"FCM token tail": logTokenTail,
			} {
				if strings.Contains(got, value) {
					t.Errorf("log contains the %s %q:\n%s", what, value, got)
				}
			}
		})
	}
}
