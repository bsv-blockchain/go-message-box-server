package storage

import "errors"

// ErrDuplicateMessage is returned when a message with the same MessageID already exists.
var ErrDuplicateMessage = errors.New("duplicate message")

// ErrInvalidQuery is returned when a query's paging arguments are out of range.
var ErrInvalidQuery = errors.New("invalid query")

// FeeBlocked is the Permission.RecipientFee sentinel meaning the sender is blocked.
const FeeBlocked = -1
