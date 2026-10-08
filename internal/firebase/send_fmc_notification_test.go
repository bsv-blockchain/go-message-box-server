package firebase

import (
	"context"
	"testing"

	"firebase.google.com/go/v4/messaging"
)

func TestLastN(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		n        int
		expected string
	}{
		{"empty string", "", 5, ""},
		{"n is zero", "hello", 0, ""},
		{"n negative", "hello", -1, ""},
		{"n greater than length", "hello", 10, "hello"},
		{"n equal to length", "hello", 5, "hello"},
		{"n less than length", "hello", 3, "llo"},
		{"single char", "a", 1, "a"},
		{"unicode string", "héllo", 3, "llo"},
		{"unicode truncate", "世界你好", 2, "你好"}, //nolint:gosmopolitan // multi-byte runes are the point of this case
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := lastN(tt.input, tt.n)
			if result != tt.expected {
				t.Errorf("lastN(%q, %d) = %q, expected %q", tt.input, tt.n, result, tt.expected)
			}
		})
	}
}

func TestIsInvalidTokenError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"generic error", &testError{msg: "some error"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInvalidTokenError(tt.err)
			if result != tt.expected {
				t.Errorf("isInvalidTokenError(%v) = %v, expected %v", tt.err, result, tt.expected)
			}
		})
	}
}

// testError is a simple error implementation for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestBuildMessage(t *testing.T) {
	token := "test-token-abc123"
	payload := FCMPayload{
		Title:      "Test Title",
		MessageID:  "msg-456",
		Originator: "sender-key-789",
		Recipient:  "02recipient-key",
		MessageBox: "payment_inbox",
	}

	msg := buildMessage(token, payload)

	t.Run("token is set", func(t *testing.T) {
		if msg.Token != token { //nolint:staticcheck // SA1019: buildMessage must set Token, see buildMessage
			t.Errorf("Token = %q, expected %q", msg.Token, token) //nolint:staticcheck // SA1019: as above
		}
	})

	t.Run("notification is set", func(t *testing.T) {
		if msg.Notification == nil {
			t.Fatal("Notification should not be nil")
		}
		if msg.Notification.Title != payload.Title {
			t.Errorf("Notification.Title = %q, expected %q", msg.Notification.Title, payload.Title)
		}
		if msg.Notification.Body != notificationBody {
			t.Errorf("Notification.Body = %q, expected %q", msg.Notification.Body, notificationBody)
		}
	})

	t.Run("android config is set", func(t *testing.T) {
		if msg.Android == nil {
			t.Fatal("Android config should not be nil")
		}
		if msg.Android.Priority != "high" {
			t.Errorf("Android.Priority = %q, expected %q", msg.Android.Priority, "high")
		}
		if msg.Android.Data == nil {
			t.Fatal("Android.Data should not be nil")
		}
		if msg.Android.Data["messageId"] != payload.MessageID {
			t.Errorf("Android.Data[messageId] = %q, expected %q", msg.Android.Data["messageId"], payload.MessageID)
		}
		if msg.Android.Data["originator"] != payload.Originator {
			t.Errorf("Android.Data[originator] = %q, expected %q", msg.Android.Data["originator"], payload.Originator)
		}
		if msg.Android.Data["recipient"] != payload.Recipient {
			t.Errorf("Android.Data[recipient] = %q, expected %q", msg.Android.Data["recipient"], payload.Recipient)
		}
		if msg.Android.Data["messageBox"] != payload.MessageBox {
			t.Errorf("Android.Data[messageBox] = %q, expected %q", msg.Android.Data["messageBox"], payload.MessageBox)
		}
	})

	t.Run("apns config is set", func(t *testing.T) {
		if msg.APNS == nil {
			t.Fatal("APNS config should not be nil")
		}
		if msg.APNS.Headers == nil {
			t.Fatal("APNS.Headers should not be nil")
		}
		if msg.APNS.Headers["apns-push-type"] != "alert" {
			t.Errorf("APNS.Headers[apns-push-type] = %q, expected %q", msg.APNS.Headers["apns-push-type"], "alert")
		}
		if msg.APNS.Headers["apns-priority"] != "10" {
			t.Errorf("APNS.Headers[apns-priority] = %q, expected %q", msg.APNS.Headers["apns-priority"], "10")
		}
	})

	t.Run("apns payload is set", func(t *testing.T) {
		if msg.APNS.Payload == nil {
			t.Fatal("APNS.Payload should not be nil")
		}
		if msg.APNS.Payload.Aps == nil {
			t.Fatal("APNS.Payload.Aps should not be nil")
		}
		if !msg.APNS.Payload.Aps.MutableContent {
			t.Error("APNS.Payload.Aps.MutableContent should be true")
		}
		if msg.APNS.Payload.Aps.Alert == nil {
			t.Fatal("APNS.Payload.Aps.Alert should not be nil")
		}
		if msg.APNS.Payload.Aps.Alert.Title != payload.Title {
			t.Errorf("APNS.Payload.Aps.Alert.Title = %q, expected %q", msg.APNS.Payload.Aps.Alert.Title, payload.Title)
		}
	})

	t.Run("apns custom data is set", func(t *testing.T) {
		if msg.APNS.Payload.CustomData == nil {
			t.Fatal("APNS.Payload.CustomData should not be nil")
		}
		if msg.APNS.Payload.CustomData["messageId"] != payload.MessageID {
			t.Errorf("CustomData[messageId] = %v, expected %q", msg.APNS.Payload.CustomData["messageId"], payload.MessageID)
		}
		if msg.APNS.Payload.CustomData["originator"] != payload.Originator {
			t.Errorf("CustomData[originator] = %v, expected %q", msg.APNS.Payload.CustomData["originator"], payload.Originator)
		}
		if msg.APNS.Payload.CustomData["recipient"] != payload.Recipient {
			t.Errorf("CustomData[recipient] = %v, expected %q", msg.APNS.Payload.CustomData["recipient"], payload.Recipient)
		}
		if msg.APNS.Payload.CustomData["messageBox"] != payload.MessageBox {
			t.Errorf("CustomData[messageBox] = %v, expected %q", msg.APNS.Payload.CustomData["messageBox"], payload.MessageBox)
		}
	})
}

// One FCM token serves every identity on an install, so the push has to say
// which identity it is for. The app reads these two fields to switch to the
// right profile; without them it can only guess.
func TestBuildMessage_CarriesRoutingFields(t *testing.T) {
	const recipient = "028d37b941208cd6b8a4c28288eda5f2f16c2b3ab0fcb6d13c18b47fe37b971fc1"
	msg := buildMessage("token", FCMPayload{
		Title:      "Payment received",
		MessageID:  "msg-1",
		Recipient:  recipient,
		MessageBox: "mandala-payments",
	})

	// The same keys on both platforms: the app reads them from one code path.
	want := map[string]string{"recipient": recipient, "messageBox": "mandala-payments", "messageId": "msg-1"}
	for key, value := range want {
		if got := msg.Android.Data[key]; got != value {
			t.Errorf("Android.Data[%s] = %q, want %q", key, got, value)
		}
		if got := msg.APNS.Payload.CustomData[key]; got != value {
			t.Errorf("APNS CustomData[%s] = %v, want %q", key, got, value)
		}
	}

	// The visible text is untouched: the identity key must not reach the lock
	// screen, and the title and body stay what they were.
	if msg.Notification.Title != "Payment received" || msg.Notification.Body != notificationBody {
		t.Errorf("Notification = (%q, %q), want (%q, %q)", msg.Notification.Title, msg.Notification.Body, "Payment received", notificationBody)
	}
	alert := msg.APNS.Payload.Aps.Alert
	if alert.Title != "Payment received" || alert.Body != notificationBody {
		t.Errorf("APNS alert = (%q, %q), want (%q, %q)", alert.Title, alert.Body, "Payment received", notificationBody)
	}
}

func TestBuildMessage_EmptyPayload(t *testing.T) {
	msg := buildMessage("token", FCMPayload{})

	if msg.Token != "token" { //nolint:staticcheck // SA1019: buildMessage must set Token, see buildMessage
		t.Errorf("Token should be set even with empty payload")
	}
	if msg.Notification == nil {
		t.Error("Notification should not be nil even with empty payload")
	}
	if msg.Android == nil {
		t.Error("Android config should not be nil even with empty payload")
	}
	if msg.APNS == nil {
		t.Error("APNS config should not be nil even with empty payload")
	}
}

func TestSendFCMNotification_NotEnabled(t *testing.T) {
	// Save original client and reset after test
	originalClient := client
	client = nil
	defer func() { client = originalClient }()

	result := SendFCMNotification(context.Background(), nil, "test-recipient", FCMPayload{
		Title:      "Test",
		MessageID:  "123",
		Originator: "sender",
	})

	if result.Success {
		t.Error("Should return failure when FCM not enabled")
	}
	if result.Error != "FCM not configured" {
		t.Errorf("Error = %q, expected %q", result.Error, "FCM not configured")
	}
}

func TestIsEnabled(t *testing.T) {
	// Save original client
	originalClient := client

	t.Run("returns false when client is nil", func(t *testing.T) {
		client = nil
		if IsEnabled() {
			t.Error("IsEnabled() should return false when client is nil")
		}
	})

	t.Run("returns true when client is set", func(t *testing.T) {
		client = &messaging.Client{}
		if !IsEnabled() {
			t.Error("IsEnabled() should return true when client is set")
		}
	})

	// Restore original client
	client = originalClient
}

func TestFCMPayload(t *testing.T) {
	payload := FCMPayload{
		Title:      "New Message",
		MessageID:  "abc-123",
		Originator: "sender-456",
	}

	if payload.Title != "New Message" {
		t.Errorf("Title = %q, expected %q", payload.Title, "New Message")
	}
	if payload.MessageID != "abc-123" {
		t.Errorf("MessageID = %q, expected %q", payload.MessageID, "abc-123")
	}
	if payload.Originator != "sender-456" {
		t.Errorf("Originator = %q, expected %q", payload.Originator, "sender-456")
	}
}

func TestSendFCMNotificationResult(t *testing.T) {
	result := &SendFCMNotificationResult{
		Success: true,
		Error:   "",
	}

	if !result.Success {
		t.Error("Success should be true")
	}
	if result.Error != "" {
		t.Errorf("Error should be empty, got %q", result.Error)
	}

	failResult := &SendFCMNotificationResult{
		Success: false,
		Error:   "some error",
	}

	if failResult.Success {
		t.Error("Success should be false")
	}
	if failResult.Error != "some error" {
		t.Errorf("Error = %q, expected %q", failResult.Error, "some error")
	}
}

func TestBuildMessage_BodyNeverCarriesMessageID(t *testing.T) {
	msg := buildMessage("token", FCMPayload{Title: "Payment received", MessageID: "msg-secret-id"})

	if msg.Notification.Body != notificationBody {
		t.Errorf("Notification.Body = %q, expected %q", msg.Notification.Body, notificationBody)
	}
	alert := msg.APNS.Payload.Aps.Alert
	if alert.Title != "Payment received" || alert.Body != notificationBody {
		t.Errorf("APNS alert = (%q, %q), expected (%q, %q)", alert.Title, alert.Body, "Payment received", notificationBody)
	}
	if msg.Android.Data["messageId"] != "msg-secret-id" {
		t.Errorf("messageId should still travel in the data payload")
	}
}
