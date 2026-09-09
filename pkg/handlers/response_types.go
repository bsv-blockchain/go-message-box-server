package handlers

// ErrorResponse represents an error response.
// @Description Error response with status, code, and description
type ErrorResponse struct {
	Status      string `json:"status" example:"error"`
	Code        string `json:"code" example:"ERR_INVALID_REQUEST"`
	Description string `json:"description" example:"Invalid request parameters"`
}

// SuccessResponse represents a simple success response.
// @Description Simple success response
type SuccessResponse struct {
	Status string `json:"status" example:"success"`
}

// MessageOut represents a message in responses.
// @Description Message object returned by listMessages
type MessageOut struct {
	MessageID string `json:"messageId" example:"abc123"`
	Body      string `json:"body" example:"{\"text\":\"Hello\"}"`
	Sender    string `json:"sender" example:"03abc..."`
	CreatedAt string `json:"createdAt" example:"2024-01-01T12:00:00.000Z"`
	UpdatedAt string `json:"updatedAt" example:"2024-01-01T12:00:00.000Z"`
}

// ListMessagesResponse represents the response for listMessages.
// @Description Response containing list of messages
type ListMessagesResponse struct {
	Status   string       `json:"status" example:"success"`
	Messages []MessageOut `json:"messages"`
}

// SendMessageResult represents a single send result.
// @Description Result for a single recipient
type SendMessageResult struct {
	Recipient string `json:"recipient" example:"03abc..."`
	MessageID string `json:"messageId" example:"msg-123"`
}

// SendMessageResponse represents the response for sendMessage.
// @Description Response after sending message(s)
type SendMessageResponse struct {
	Status  string              `json:"status" example:"success"`
	Message string              `json:"message" example:"Your message has been sent to 1 recipient(s)."`
	Results []SendMessageResult `json:"results"`
}

// DeviceOut represents a device in responses.
// @Description Device registration object
type DeviceOut struct {
	DeviceID  *string `json:"deviceId,omitempty" example:"device-abc"`
	Platform  *string `json:"platform,omitempty" example:"ios"`
	FCMToken  string  `json:"fcmToken" example:"...abc123"`
	Active    bool    `json:"active" example:"true"`
	CreatedAt string  `json:"createdAt" example:"2024-01-01T12:00:00.000Z"`
	UpdatedAt string  `json:"updatedAt" example:"2024-01-01T12:00:00.000Z"`
	LastUsed  string  `json:"lastUsed,omitempty" example:"2024-01-01T12:00:00.000Z"`
}

// ListDevicesResponse represents the response for listDevices.
// @Description Response containing list of devices
type ListDevicesResponse struct {
	Status  string      `json:"status" example:"success"`
	Devices []DeviceOut `json:"devices"`
}

// RegisterDeviceResponse represents the response for registerDevice.
// @Description Response after registering a device
type RegisterDeviceResponse struct {
	Status  string `json:"status" example:"success"`
	Message string `json:"message" example:"Device registered successfully for push notifications"`
}

// SetPermissionResponse represents the response for setPermission.
// @Description Response after setting a permission
type SetPermissionResponse struct {
	Status      string `json:"status" example:"success"`
	Description string `json:"description" example:"Messages from sender to inbox now require 100 satoshis."`
}

// PermissionDetail is used by GET /permissions/get — client returns it raw, expects camelCase.
// @Description Permission details (camelCase for getPermission endpoint)
type PermissionDetail struct {
	Sender       *string `json:"sender" example:"03abc..."`
	MessageBox   string  `json:"messageBox" example:"inbox"`
	RecipientFee int     `json:"recipientFee" example:"100"`
	Status       string  `json:"status,omitempty" example:"payment_required"`
	CreatedAt    string  `json:"createdAt" example:"2024-01-01T12:00:00.000Z"`
	UpdatedAt    string  `json:"updatedAt" example:"2024-01-01T12:00:00.000Z"`
}

// PermissionDetailList is used by GET /permissions/list — client maps explicitly from snake_case.
// @Description Permission details (snake_case for listPermissions endpoint)
type PermissionDetailList struct {
	Sender       *string `json:"sender" example:"03abc..."`
	MessageBox   string  `json:"message_box" example:"inbox"`
	RecipientFee int     `json:"recipient_fee" example:"100"`
	CreatedAt    string  `json:"created_at" example:"2024-01-01T12:00:00.000Z"`
	UpdatedAt    string  `json:"updated_at" example:"2024-01-01T12:00:00.000Z"`
}

// GetPermissionResponse represents the response for getPermission.
// @Description Response containing permission details
type GetPermissionResponse struct {
	Status      string            `json:"status" example:"success"`
	Description string            `json:"description" example:"Permission setting found."`
	Permission  *PermissionDetail `json:"permission,omitempty"`
}

// ListPermissionsResponse represents the response for listPermissions.
// @Description Response containing list of permissions
type ListPermissionsResponse struct {
	Status      string                 `json:"status" example:"success"`
	Permissions []PermissionDetailList `json:"permissions"`
	TotalCount  int                    `json:"totalCount" example:"10"`
}

// QuoteSingle represents a single-recipient quote.
// @Description Quote for single recipient
type QuoteSingle struct {
	DeliveryFee  int `json:"deliveryFee" example:"10"`
	RecipientFee int `json:"recipientFee" example:"100"`
}

// QuoteSingleResponse represents the response for single-recipient quote.
// @Description Response containing quote for single recipient
type QuoteSingleResponse struct {
	Status      string      `json:"status" example:"success"`
	Description string      `json:"description" example:"Message delivery quote generated."`
	Quote       QuoteSingle `json:"quote"`
}

// QuoteEntry represents a quote for one recipient in multi-recipient quotes.
// @Description Quote for one recipient in batch
type QuoteEntry struct {
	Recipient    string `json:"recipient" example:"03abc..."`
	MessageBox   string `json:"messageBox" example:"inbox"`
	DeliveryFee  int    `json:"deliveryFee" example:"10"`
	RecipientFee int    `json:"recipientFee" example:"100"`
	Status       string `json:"status" example:"payment_required"`
}

// QuoteTotals represents totals for multi-recipient quotes.
// @Description Totals for batch quote
type QuoteTotals struct {
	DeliveryFees              int `json:"deliveryFees" example:"20"`
	RecipientFees             int `json:"recipientFees" example:"200"`
	TotalForPayableRecipients int `json:"totalForPayableRecipients" example:"220"`
}

// QuoteMultiResponse represents the response for multi-recipient quote.
// @Description Response containing quotes for multiple recipients
type QuoteMultiResponse struct {
	Status            string       `json:"status" example:"success"`
	Description       string       `json:"description" example:"Message delivery quotes generated for 2 recipients."`
	QuotesByRecipient []QuoteEntry `json:"quotesByRecipient"`
	Totals            QuoteTotals  `json:"totals"`
	BlockedRecipients []string     `json:"blockedRecipients"`
}

// DeliveryBlockedError represents an error when recipients are blocked.
// @Description Error response when delivery is blocked for some recipients
type DeliveryBlockedError struct {
	Status            string   `json:"status" example:"error"`
	Code              string   `json:"code" example:"ERR_DELIVERY_BLOCKED"`
	Description       string   `json:"description" example:"Blocked recipients: 03abc..."`
	BlockedRecipients []string `json:"blockedRecipients"`
}
