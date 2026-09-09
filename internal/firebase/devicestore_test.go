package firebase

import (
	"context"
	"testing"

	"firebase.google.com/go/v4/messaging"

	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// fakeDeviceStore records the calls SendFCMNotification makes, so the device
// side of the notification path can be exercised without a real backend.
type fakeDeviceStore struct {
	devices     []storage.Device
	listErr     error
	deactivated []string
	lastUsed    []string
}

func (f *fakeDeviceStore) RegisterDevice(context.Context, storage.NewDevice) error { return nil }

func (f *fakeDeviceStore) ListDevices(context.Context, string) ([]storage.Device, error) {
	return f.devices, f.listErr
}

func (f *fakeDeviceStore) ListActiveDevices(_ context.Context, _ string) ([]storage.Device, error) {
	return f.devices, f.listErr
}

func (f *fakeDeviceStore) UpdateDeviceLastUsed(_ context.Context, fcmToken string) error {
	f.lastUsed = append(f.lastUsed, fcmToken)
	return nil
}

func (f *fakeDeviceStore) DeactivateDevice(_ context.Context, fcmToken string) error {
	f.deactivated = append(f.deactivated, fcmToken)
	return nil
}

var _ storage.DeviceStore = (*fakeDeviceStore)(nil)

// withClient installs a non-nil messaging client so IsEnabled() reports true.
// It is a zero value, so it must not be used to actually send: only the paths
// that return before the send loop are covered here. Testing the send loop
// itself needs an injectable sender, which the firebase package does not have.
func withClient(t *testing.T) {
	t.Helper()
	original := client
	client = &messaging.Client{}
	t.Cleanup(func() { client = original })
}

func TestSendFCMNotification_NoDevices(t *testing.T) {
	withClient(t)

	store := &fakeDeviceStore{}
	result := SendFCMNotification(context.Background(), store, "02recipient", FCMPayload{
		Title:     "New Message",
		MessageID: "m1",
	})

	if result.Success {
		t.Error("Success = true, want false when the recipient has no devices")
	}
	if result.Error != "No registered devices found for recipient" {
		t.Errorf("Error = %q, want the no-devices message", result.Error)
	}
	if len(store.lastUsed) != 0 || len(store.deactivated) != 0 {
		t.Errorf("store was written to: lastUsed=%v deactivated=%v", store.lastUsed, store.deactivated)
	}
}

func TestSendFCMNotification_ListError(t *testing.T) {
	withClient(t)

	store := &fakeDeviceStore{listErr: context.DeadlineExceeded}
	result := SendFCMNotification(context.Background(), store, "02recipient", FCMPayload{})

	if result.Success {
		t.Error("Success = true, want false when the device lookup fails")
	}
	if result.Error == "" {
		t.Error("Error is empty, want the lookup failure described")
	}
}
