package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bsv-blockchain/go-message-box-server/internal/firebase"
	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
)

// SendMessage godoc
// @Summary      Send a message to recipient(s)
// @Description  Inserts a message into the target recipient's message box. Supports single or multiple recipients. Payment may be required depending on recipient's fee settings.
// @Tags         Messages
// @Accept       json
// @Produce      json
// @Param        request body SendMessageRequest true "Message to send"
// @Success      200  {object}  SendMessageResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      403  {object}  DeliveryBlockedError
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /sendMessage [post]
func (s *Server) SendMessage(w http.ResponseWriter, r *http.Request) {
	logger.Log("[DEBUG] Processing /sendMessage request...")

	senderKey := getIdentityKey(r)
	if senderKey == "" {
		writeError(w, 401, "ERR_AUTH_REQUIRED", "Authentication required")
		return
	}

	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "ERR_INVALID_JSON", "Invalid JSON body")
		return
	}

	msg := req.Message
	if msg == nil {
		writeError(w, 400, "ERR_MESSAGE_REQUIRED", "Please provide a valid message to send!")
		return
	}

	if strings.TrimSpace(msg.MessageBox) == "" {
		writeError(w, 400, "ERR_INVALID_MESSAGEBOX", "Invalid message box.")
		return
	}

	// Validate body
	if len(msg.Body) == 0 || string(msg.Body) == `""` || string(msg.Body) == "null" {
		writeError(w, 400, "ERR_INVALID_MESSAGE_BODY", "Invalid message body.")
		return
	}

	// Normalize recipients
	recipientsRaw := msg.Recipients
	if len(recipientsRaw) == 0 || string(recipientsRaw) == "null" {
		recipientsRaw = msg.Recipient
	}
	if len(recipientsRaw) == 0 || string(recipientsRaw) == "null" {
		writeError(w, 400, "ERR_RECIPIENT_REQUIRED", `Missing recipient(s). Provide "recipient" or "recipients".`)
		return
	}

	var recipients []string
	// Try array first
	if err := json.Unmarshal(recipientsRaw, &recipients); err != nil {
		// Try single string
		var single string
		if err2 := json.Unmarshal(recipientsRaw, &single); err2 != nil {
			writeError(w, 400, "ERR_INVALID_RECIPIENT_KEY", "Invalid recipient format")
			return
		}
		recipients = []string{single}
	}

	// Normalize messageIds
	var messageIDs []string
	if err := json.Unmarshal(msg.MessageID, &messageIDs); err != nil {
		var single string
		if err2 := json.Unmarshal(msg.MessageID, &single); err2 != nil {
			writeError(w, 400, "ERR_MESSAGEID_REQUIRED", "Missing messageId.")
			return
		}
		messageIDs = []string{single}
	}

	// Validate counts
	if len(recipients) > 1 && len(messageIDs) == 1 {
		writeError(w, 400, "ERR_MESSAGEID_COUNT_MISMATCH",
			fmt.Sprintf("Provided 1 messageId for %d recipients. Provide one messageId per recipient (same order).", len(recipients)))
		return
	}
	if len(messageIDs) != len(recipients) {
		writeError(w, 400, "ERR_MESSAGEID_COUNT_MISMATCH",
			fmt.Sprintf("Recipients (%d) and messageId count (%d) must match.", len(recipients), len(messageIDs)))
		return
	}

	// Validate each messageId
	for _, id := range messageIDs {
		if strings.TrimSpace(id) == "" {
			writeError(w, 400, "ERR_INVALID_MESSAGEID", "Each messageId must be a non-empty string.")
			return
		}
	}

	// Validate recipient keys
	for _, r := range recipients {
		if !isValidPubKey(strings.TrimSpace(r)) {
			writeError(w, 400, "ERR_INVALID_RECIPIENT_KEY", fmt.Sprintf("Invalid recipient key: %s", r))
			return
		}
	}

	boxType := strings.TrimSpace(msg.MessageBox)

	// Fee evaluation
	deliveryFee, err := s.Store.GetServerDeliveryFee(r.Context(), boxType)
	if err != nil {
		logger.Error("failed to get delivery fee", "error", err)
		writeError(w, 500, "ERR_INTERNAL", "An internal error has occurred.")
		return
	}

	var feeRows []feeRow
	for _, recip := range recipients {
		recip = strings.TrimSpace(recip)
		rf, err := s.recipientFee(r.Context(), recip, senderKey, boxType)
		if err != nil {
			logger.Error("failed to get recipient fee", "error", err)
			writeError(w, 500, "ERR_INTERNAL", "An internal error has occurred.")
			return
		}
		feeRows = append(feeRows, feeRow{
			recipient:    recip,
			recipientFee: rf,
			allowed:      rf != storage.FeeBlocked,
		})
	}

	// Check blocked
	var blocked []string
	for _, fr := range feeRows {
		if !fr.allowed {
			blocked = append(blocked, fr.recipient)
		}
	}
	if len(blocked) > 0 {
		writeJSON(w, 403, DeliveryBlockedError{
			Status:            "error",
			Code:              "ERR_DELIVERY_BLOCKED",
			Description:       fmt.Sprintf("Blocked recipients: %s", strings.Join(blocked, ", ")),
			BlockedRecipients: blocked,
		})
		return
	}

	// Check if payment is required
	anyRecipientFee := false
	for _, fr := range feeRows {
		if fr.recipientFee > 0 {
			anyRecipientFee = true
			break
		}
	}
	requiresPayment := deliveryFee > 0 || anyRecipientFee
	perRecipientOutputs := make(map[string][]PaymentOutput)

	// payments internalization
	if requiresPayment {
		if req.Payment == nil || len(req.Payment.Tx) == 0 || len(req.Payment.Outputs) == 0 {
			writeError(w, 400, "ERR_MISSING_PAYMENT_TX", "Payment transaction data is required for payable delivery.")
			return
		}

		if deliveryFee > 0 {
			serverOutput := req.Payment.Outputs[0] // server delivery fee is the output at index 0

			sdkOutput, err := toSDKInternalizeOutput(serverOutput)
			if err != nil {
				writeError(w, 400, "ERR_INVALID_PAYMENT_OUTPUT", fmt.Sprintf("Invalid payment output: %v", err))
				return
			}

			description := req.Payment.Description
			if description == "" {
				description = "MessageBox delivery payment"
			}

			internalizeArgs := sdk.InternalizeActionArgs{
				Tx:          req.Payment.Tx,
				Outputs:     []sdk.InternalizeOutput{sdkOutput},
				Description: description,
				Labels:      req.Payment.Labels,
			}

			result, err := s.wallet.InternalizeAction(r.Context(), internalizeArgs, "messagebox-server")
			if err != nil {
				logger.Error("failed to internalize delivery fee", "error", err)
				writeError(w, 500, "ERR_INTERNALIZE_FAILED", fmt.Sprintf("Failed to internalize payment: %v", err))
				return
			}
			if !result.Accepted {
				writeError(w, 400, "ERR_INSUFFICIENT_PAYMENT", "Payment was not accepted by the server.")
				return
			}
			logger.Log("[DEBUG] Internalized server delivery output at index 0")
		}

		perRecipientOutputs, err = buildPerRecipientOutputs(req.Payment.Outputs, deliveryFee, feeRows)
		if err != nil {
			if omErr, ok := err.(*OutputMappingError); ok {
				logger.Error("output mapping failed", "code", omErr.Code, "description", omErr.Description)
				writeError(w, 400, omErr.Code, omErr.Description)
			} else {
				logger.Error("output mapping failed", "error", err)
				writeError(w, 500, "ERR_INTERNAL", fmt.Sprintf("Failed to map payment outputs to recipients: %v", err))
			}
			return
		}
	}

	var results []SendMessageResult
	for i, fr := range feeRows {
		msgID := messageIDs[i]

		// Build stored body
		storedBody := map[string]any{
			"message": json.RawMessage(msg.Body),
		}

		// Include per-recipient payment (only their outputs, not full payment)
		if recipientOutputs, ok := perRecipientOutputs[fr.recipient]; ok && req.Payment != nil {
			perRecipientPayment := Payment{
				Tx:             req.Payment.Tx,
				Outputs:        recipientOutputs,
				Description:    req.Payment.Description,
				Labels:         req.Payment.Labels,
				SeekPermission: req.Payment.SeekPermission,
			}
			storedBody["payment"] = perRecipientPayment
		}

		bodyBytes, _ := json.Marshal(storedBody)

		newMsg := storage.NewMessage{
			MessageID:  msgID,
			Recipient:  fr.recipient,
			MessageBox: boxType,
			Sender:     senderKey,
			Body:       string(bodyBytes),
		}
		if err := s.Store.InsertMessage(r.Context(), newMsg); err != nil {
			if errors.Is(err, storage.ErrDuplicateMessage) {
				logger.Error("duplicate message rejected", "messageId", msgID)
				writeError(w, 400, "ERR_DUPLICATE_MESSAGE", "Duplicate message.")
				return
			}
			logger.Error("failed to insert message", "error", err)
			writeError(w, 500, "ERR_INTERNAL", "An internal error has occurred.")
			return
		}

		if shouldUseFCMDelivery(boxType) {
			// r.Context() is cancelled the moment the response is written, so the
			// detached send is decoupled from it while keeping request values.
			// It carries no overall deadline on purpose: SendFCMNotification
			// bounds each device individually, and a shared budget here would
			// starve the tail of a long device list.
			fcmCtx := context.WithoutCancel(r.Context())
			go func(recipient, messageID string) {
				firebase.SendFCMNotification(fcmCtx, s.Store, recipient, firebase.FCMPayload{
					Title:     "New Message",
					MessageID: messageID,
				})
			}(fr.recipient, msgID)
		}

		results = append(results, SendMessageResult{Recipient: fr.recipient, MessageID: msgID})
	}

	if results == nil {
		results = []SendMessageResult{}
	}
	writeJSON(w, 200, SendMessageResponse{
		Status:  "success",
		Message: fmt.Sprintf("Your message has been sent to %d recipient(s).", len(results)),
		Results: results,
	})
}
