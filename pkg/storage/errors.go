package storage

import "errors"

// ErrDuplicateMessage is returned when a message with the same MessageID already exists.
var ErrDuplicateMessage = errors.New("duplicate message")

// FeeBlocked is the Permission.RecipientFee sentinel meaning the sender is blocked.
const FeeBlocked = -1
