package firebase

import (
	"context"
	"fmt"
	"time"

	"firebase.google.com/go/v4/messaging"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// deviceSendMessageTimeout bounds each FCM send, so one slow or unreachable
// device cannot hold up delivery to the recipient's other devices.
var deviceSendMessageTimeout = 5 * time.Second

// storeCallTimeout bounds each device-store call. The caller detaches this work
// from the request, so nothing else would ever stop it: a lock held on
// device_registrations, or a connected but unresponsive Mongo primary, would
// otherwise park a goroutine holding a pool connection for good, one per
// delivery, until the pool is exhausted and foreground sends start failing.
var storeCallTimeout = 5 * time.Second

// sendFCM sends one message. It is a variable so tests can stand in for FCM.
var sendFCM = func(ctx context.Context, msg *messaging.Message) (string, error) {
	return Client().Send(ctx, msg)
}

// FCMPayload contains the notification data to send.
type FCMPayload struct {
	Title      string
	MessageID  string
	Originator string
	// Recipient is the identity key (compressed hex) the message was sent to,
	// and MessageBox the box it landed in. One FCM token serves every identity
	// on an install, so these tell the app which identity the push is for.
	Recipient  string
	MessageBox string
}

// SendFCMNotificationResult contains the result of a send operation.
type SendFCMNotificationResult struct {
	Success bool
	Error   string
}

// SendFCMNotification pushes notification to all registered devices for a recipient.
// Looks up FCM tokens from the device store and sends to all active devices.
//
// It bounds every call it makes — each FCM send and each device-store call gets
// its own timeout — so the caller can pass a context with no deadline and still
// be sure this returns. Cancelling the passed context stops the run.
//
// Nothing here logs the recipient, the message ID or an FCM token, not even the
// tail of one: the log is shipped to a third-party store, and each of them ties
// a delivery to a user or a device, so a line that carried one would say who
// was paid and when. The lines carry the message box, counts and error text.
func SendFCMNotification(ctx context.Context, devices storage.DeviceStore, recipient string, payload FCMPayload) *SendFCMNotificationResult {
	if !IsEnabled() {
		return &SendFCMNotificationResult{Success: false, Error: "FCM not configured"}
	}

	listCtx, cancelList := context.WithTimeout(ctx, storeCallTimeout)
	deviceList, err := devices.ListActiveDevices(listCtx, recipient)
	cancelList()
	if err != nil {
		logger.Error("[FCM] Failed to get devices", "error", err, "messageBox", payload.MessageBox)
		return &SendFCMNotificationResult{Success: false, Error: fmt.Sprintf("failed to get devices: %v", err)}
	}

	if len(deviceList) == 0 {
		logger.Log("[FCM] No active devices found", "messageBox", payload.MessageBox)
		return &SendFCMNotificationResult{Success: false, Error: "No registered devices found for recipient"}
	}

	logger.Log("[FCM] Sending notifications", "messageBox", payload.MessageBox, "deviceCount", len(deviceList))

	var successCount, failureCount int

	for _, device := range deviceList {
		msg := buildMessage(device.FCMToken, payload)

		sendCtx, cancel := context.WithTimeout(ctx, deviceSendMessageTimeout)
		_, err := sendFCM(sendCtx, msg)
		cancel()

		if err != nil {
			logger.Error("[FCM] Failed to send", "error", err)
			failureCount++

			// we only mark devices as disabled when token is invalid
			if isInvalidTokenError(err) {
				logger.Log("[FCM] Deactivating invalid token")
				// Recording the outcome gets its own budget: the send's
				// deadline has already expired in the timeout case, and a token
				// known to be invalid still has to be deactivated.
				deactivateCtx, cancelDeactivate := context.WithTimeout(ctx, storeCallTimeout)
				deactivateErr := devices.DeactivateDevice(deactivateCtx, device.FCMToken)
				cancelDeactivate()
				if deactivateErr != nil {
					logger.Error("[FCM] Failed to deactivate device", "error", deactivateErr)
				}
			}
			continue
		}

		successCount++

		lastUsedCtx, cancelLastUsed := context.WithTimeout(ctx, storeCallTimeout)
		err = devices.UpdateDeviceLastUsed(lastUsedCtx, device.FCMToken)
		cancelLastUsed()
		if err != nil {
			logger.Error("[FCM] Failed to update last_used", "error", err)
		}
	}

	logger.Log("[FCM] Send complete", "messageBox", payload.MessageBox, "success", successCount, "failed", failureCount)

	// if not a single device received a notification consider it a fail
	// otherwise we consider it a success
	if successCount == 0 {
		return &SendFCMNotificationResult{Success: false, Error: fmt.Sprintf("failed to send to all %d registered devices", len(deviceList))}
	}

	return &SendFCMNotificationResult{Success: true}
}

// notificationBody is the text shown under the title. The message ID used to
// go here, which put an opaque identifier on the user's lock screen; the ID
// still travels in the data payload for the app to act on.
const notificationBody = "Open the app to view it."

func buildMessage(token string, payload FCMPayload) *messaging.Message {
	return &messaging.Message{
		// Token is deprecated in favor of Fid, but Fid carries a Firebase
		// Installation ID; devices here register FCM registration tokens, which
		// are addressed through Token.
		Token: token, //nolint:staticcheck // SA1019: registration tokens are not FIDs, see above
		Notification: &messaging.Notification{
			Title: payload.Title,
			Body:  notificationBody,
		},
		// Android configuration for headless service
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Data: map[string]string{
				"messageId":  payload.MessageID,
				"originator": payload.Originator,
				"recipient":  payload.Recipient,
				"messageBox": payload.MessageBox,
			},
		},
		// iOs configuration for mutable content and Notification Service Extension
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{
				"apns-push-type": "alert", // required for iOS 13+
				"apns-priority":  "10",    // deliver immediately
			},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					MutableContent: true,
					Alert: &messaging.ApsAlert{ // include an alert so NSE can modify it
						Title: payload.Title,
						Body:  notificationBody,
					},
				},
				CustomData: map[string]interface{}{
					"messageId":  payload.MessageID,
					"originator": payload.Originator,
					"recipient":  payload.Recipient,
					"messageBox": payload.MessageBox,
				},
			},
		},
	}
}

func isInvalidTokenError(err error) bool {
	return messaging.IsUnregistered(err) || messaging.IsInvalidArgument(err)
}
