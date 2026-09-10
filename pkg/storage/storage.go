// Package storage defines the persistence contract for the message box server.
//
// Implementations live in subpackages (sqlstore, mongostore). The contract is
// deliberately free of backend concepts: no numeric surrogate keys, no
// database/sql types, no SQL fragments. Every implementation must pass the
// conformance suite in pkg/storage/storagetest.
package storage

import "context"

// Store is the full persistence contract. Every backend must satisfy it.
type Store interface {
	MessageStore
	PermissionStore
	DeviceStore
	FeeStore

	// EnsureSchema prepares the backend for use. It must be idempotent; it runs
	// on every boot.
	EnsureSchema(ctx context.Context) error

	// Close releases the backend's resources.
	Close() error
}

// MessageStore stores and retrieves messages.
type MessageStore interface {
	// InsertMessage stores m, creating the recipient's message box if the backend
	// needs one. It returns ErrDuplicateMessage if m.MessageID already exists, in
	// which case no record is written. CreatedAt and UpdatedAt are assigned by the
	// store.
	InsertMessage(ctx context.Context, m NewMessage) error

	// ListMessages returns the box's messages ordered by CreatedAt ascending, then
	// MessageID ascending. An unknown recipient or box returns no messages and no
	// error.
	ListMessages(ctx context.Context, recipient, messageBox string) ([]Message, error)

	// AcknowledgeMessages deletes the named messages owned by recipient and returns
	// the number actually deleted. It never deletes another recipient's message.
	AcknowledgeMessages(ctx context.Context, recipient string, messageIDs []string) (int64, error)
}

// PermissionStore stores per-recipient delivery permissions.
type PermissionStore interface {
	// SetPermission upserts on (recipient, sender, messageBox); a nil sender is the
	// box-wide row. An existing row keeps its CreatedAt and gets a new UpdatedAt.
	SetPermission(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error

	// SetPermissionIfAbsent creates the permission only if it does not already
	// exist. An existing row is never modified, and concurrent callers converge
	// on a single row rather than each inserting their own. This is what the fee
	// fallback's default write-back needs: it must not overwrite a permission
	// the recipient set in the meantime, and it runs on the hot send path where
	// callers do race.
	SetPermissionIfAbsent(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error

	// GetPermission returns (nil, nil) when no such permission exists.
	GetPermission(ctx context.Context, recipient string, sender *string, messageBox string) (*Permission, error)

	// ListPermissions returns one page plus the unpaginated total, ordered by
	// MessageBox ascending, box-wide rows first, Sender ascending, then CreatedAt
	// per q.Order.
	ListPermissions(ctx context.Context, q PermissionQuery) (PermissionPage, error)
}

// DeviceStore stores push notification device registrations.
type DeviceStore interface {
	// RegisterDevice upserts on d.FCMToken, reactivating the device.
	RegisterDevice(ctx context.Context, d NewDevice) error

	// ListDevices returns all devices for identityKey, most recently updated first.
	ListDevices(ctx context.Context, identityKey string) ([]Device, error)

	// ListActiveDevices is ListDevices restricted to active devices.
	ListActiveDevices(ctx context.Context, identityKey string) ([]Device, error)

	// UpdateDeviceLastUsed records that a notification was delivered to the token.
	// An unknown token is a no-op, not an error.
	UpdateDeviceLastUsed(ctx context.Context, fcmToken string) error

	// DeactivateDevice marks a token as invalid. An unknown token is a no-op.
	DeactivateDevice(ctx context.Context, fcmToken string) error
}

// FeeStore reads the server's configured delivery fees.
type FeeStore interface {
	// GetServerDeliveryFee returns 0 for a message box with no configured fee.
	GetServerDeliveryFee(ctx context.Context, messageBox string) (int, error)
}
