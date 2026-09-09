package storage

import "time"

// NewMessage is a message to be stored. MessageBox is the box type; there is no
// numeric box id in the contract.
type NewMessage struct {
	MessageID  string
	Recipient  string
	MessageBox string
	Sender     string
	Body       string // opaque JSON, built by the caller
}

// Message is a stored message as returned to a recipient.
type Message struct {
	MessageID string
	Sender    string
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Permission is a delivery permission. A nil Sender is the box-wide default.
// RecipientFee is FeeBlocked to block, 0 to allow, or the satoshis required.
type Permission struct {
	Recipient    string
	Sender       *string
	MessageBox   string
	RecipientFee int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SortOrder selects an ascending or descending sort.
type SortOrder uint8

// Sort orders. SortDesc is the zero value, matching the handler default.
const (
	SortDesc SortOrder = iota
	SortAsc
)

// PermissionQuery selects and pages a recipient's permissions.
type PermissionQuery struct {
	Recipient  string
	MessageBox *string // nil means all boxes
	Limit      int
	Offset     int
	Order      SortOrder // applied to CreatedAt
}

// PermissionPage is one page of permissions plus the unpaginated total.
type PermissionPage struct {
	Items []Permission
	Total int
}

// NewDevice is a device registration to be stored.
type NewDevice struct {
	IdentityKey string
	FCMToken    string
	DeviceID    *string
	Platform    *string
}

// Device is a stored device registration.
type Device struct {
	IdentityKey string
	FCMToken    string
	DeviceID    *string
	Platform    *string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastUsed    *time.Time
}
