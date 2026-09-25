// Package storage defines the persistence contract for the message box server.
//
// Implementations live in subpackages (sqlstore, mongostore). The contract is
// deliberately free of backend concepts: no numeric surrogate keys, no
// database/sql types, no SQL fragments. Every implementation must pass the
// conformance suite in pkg/storage/storagetest.
//
// HandleStore, in handles.go, is a second and optional contract: it is not part
// of Store, because it asks for uniqueness guarantees only some backends can
// give. Only mongostore implements it, and only the paymail handle registry
// needs it.
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

// MessagePager is an optional extension of MessageStore for backends that can
// answer a MessagePageQuery directly, rather than the handler paging in
// memory over ListMessages's full result. It is not part of Store: adding a
// required method there would break every external Store implementation
// (README's plug-in-your-own-Store contract), so POST /listMessages type-
// asserts for this interface and falls back to correct, if less efficient,
// in-memory pagination when a backend does not implement it.
//
// Both sqlstore (SQLite and PostgreSQL) and mongostore implement it.
type MessagePager interface {
	// PageMessages returns up to q.FetchLimit messages ordered by CreatedAt
	// ascending, then MessageID ascending, starting at the q.Offset'th matching
	// message (0-based) — the same order ListMessages guarantees. An unknown
	// recipient or box, or a MessageID filter that matches nothing, returns no
	// messages and no error, matching ListMessages.
	//
	// FetchLimit is ordinarily the caller's requested page size plus one:
	// fetching one extra row lets the caller tell whether another page exists
	// without a second round trip, and POST /listMessages's own caller always
	// passes at least 1. An implementation must still treat FetchLimit <= 0 as
	// "return no rows" rather than "no limit": SQL's LIMIT 0 already means
	// that, but MongoDB's driver treats a zero SetLimit as unbounded, so a
	// MongoDB-backed implementation must special-case it explicitly to agree
	// with a SQL-backed one on this out-of-contract input. Offset is always
	// zero or more.
	PageMessages(ctx context.Context, q MessagePageQuery) ([]Message, error)
}

// PermissionStore stores per-recipient delivery permissions.
type PermissionStore interface {
	// SetPermission upserts on (recipient, sender, messageBox); a nil sender is the
	// box-wide row. An existing row keeps its CreatedAt and gets a new UpdatedAt.
	SetPermission(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error

	// SetPermissionIfAbsent creates the permission only if it does not already
	// exist. An existing row is never modified. This is what the fee fallback's
	// default write-back needs: it must not overwrite a permission the recipient
	// set in the meantime.
	//
	// Both writes are safe to race, against themselves and each other, for the
	// box-wide row as much as any other: concurrent callers leave exactly one
	// row. mongostore gets that from a unique index covering null. A SQL UNIQUE
	// constraint treats NULLs as distinct, so sqlstore serialises box-wide
	// writers itself; see sqlstore.withBoxWideLock.
	SetPermissionIfAbsent(ctx context.Context, recipient string, sender *string, messageBox string, recipientFee int) error

	// GetPermission returns (nil, nil) when no such permission exists.
	GetPermission(ctx context.Context, recipient string, sender *string, messageBox string) (*Permission, error)

	// ListPermissions returns one page plus the unpaginated total, ordered by
	// MessageBox ascending, box-wide rows first, Sender ascending, then CreatedAt
	// per q.Order. It returns ErrInvalidQuery if q.Validate does.
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
