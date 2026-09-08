package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// SetPermission godoc
// @Summary      Set a message permission
// @Description  Sets fee requirements for receiving messages. Use recipientFee=0 for free, recipientFee=-1 to block, or a positive value for required payment in satoshis. Omit sender for box-wide defaults.
// @Tags         Permissions
// @Accept       json
// @Produce      json
// @Param        request body SetPermissionRequest true "Permission settings"
// @Success      200  {object}  SetPermissionResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /permissions/set [post]
func (s *Server) SetPermission(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_AUTHENTICATION_REQUIRED", "Authentication required.")
		return
	}

	var req SetPermissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "ERR_INVALID_JSON", "Invalid JSON body")
		return
	}

	if req.MessageBox == "" || req.RecipientFee == nil {
		writeError(w, 400, "ERR_INVALID_REQUEST", "messageBox (string) and recipientFee (number) are required. sender (string) is optional for box-wide settings.")
		return
	}

	if req.Sender != nil && !isValidPubKey(*req.Sender) {
		writeError(w, 400, "ERR_INVALID_PUBLIC_KEY", "Invalid sender public key format.")
		return
	}

	fee := *req.RecipientFee

	if err := s.Store.SetPermission(r.Context(), identityKey, req.Sender, req.MessageBox, fee); err != nil {
		logger.Error("failed to set permission", "error", err)
		writeError(w, 500, "ERR_DATABASE_ERROR", "Failed to update message permission.")
		return
	}

	isBoxWide := req.Sender == nil
	var senderText, actionText, description string
	if isBoxWide {
		senderText = "all senders"
		actionText = "Box-wide default for"
	} else {
		senderText = *req.Sender
		actionText = "Messages from"
	}

	switch {
	case fee == -1:
		if isBoxWide {
			description = fmt.Sprintf("%s %s to %s is now blocked.", actionText, senderText, req.MessageBox)
		} else {
			description = fmt.Sprintf("%s %s to %s are now blocked.", actionText, senderText, req.MessageBox)
		}
	case fee == 0:
		if isBoxWide {
			description = fmt.Sprintf("%s %s to %s is now always allowed.", actionText, senderText, req.MessageBox)
		} else {
			description = fmt.Sprintf("%s %s to %s are now always allowed.", actionText, senderText, req.MessageBox)
		}
	default:
		if isBoxWide {
			description = fmt.Sprintf("%s %s to %s now requires %d satoshis.", actionText, senderText, req.MessageBox, fee)
		} else {
			description = fmt.Sprintf("%s %s to %s now require %d satoshis.", actionText, senderText, req.MessageBox, fee)
		}
	}

	writeJSON(w, 200, SetPermissionResponse{
		Status:      "success",
		Description: description,
	})
}

// GetPermission godoc
// @Summary      Get a message permission
// @Description  Retrieves the permission setting for a specific sender or box-wide default for the authenticated identity.
// @Tags         Permissions
// @Produce      json
// @Param        messageBox query string true "Name of the message box"
// @Param        sender query string false "Sender's public key (omit for box-wide setting)"
// @Success      200  {object}  GetPermissionResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /permissions/get [get]
func (s *Server) GetPermission(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_AUTHENTICATION_REQUIRED", "Authentication required.")
		return
	}

	messageBox := r.URL.Query().Get("messageBox")
	if messageBox == "" {
		writeError(w, 400, "ERR_MISSING_PARAMETERS", "messageBox parameter is required.")
		return
	}

	senderParam := r.URL.Query().Get("sender")
	var sender *string
	if senderParam != "" {
		if !isValidPubKey(senderParam) {
			writeError(w, 400, "ERR_INVALID_PUBLIC_KEY", "Invalid sender public key format.")
			return
		}
		sender = &senderParam
	}

	perm, err := s.Store.GetPermission(r.Context(), identityKey, sender, messageBox)
	if err != nil {
		logger.Error("failed to get permission", "error", err)
		writeError(w, 500, "ERR_INTERNAL", "An internal error has occurred.")
		return
	}

	if perm != nil {
		status := "always_allow"
		if perm.RecipientFee == storage.FeeBlocked {
			status = "blocked"
		} else if perm.RecipientFee > 0 {
			status = "payment_required"
		}

		var desc string
		if sender != nil {
			desc = fmt.Sprintf("Permission setting found for sender %s to %s.", *sender, messageBox)
		} else {
			desc = fmt.Sprintf("Box-wide permission setting found for %s.", messageBox)
		}

		senderVal := perm.Sender

		writeJSON(w, 200, GetPermissionResponse{
			Status:      "success",
			Description: desc,
			Permission: &PermissionDetail{
				Sender:       senderVal,
				MessageBox:   messageBox,
				RecipientFee: perm.RecipientFee,
				Status:       status,
				CreatedAt:    perm.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
				UpdatedAt:    perm.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
			},
		})
	} else {
		var desc string
		if sender != nil {
			desc = fmt.Sprintf("No permission setting found for sender %s to %s.", *sender, messageBox)
		} else {
			desc = fmt.Sprintf("No box-wide permission setting found for %s.", messageBox)
		}
		writeJSON(w, 200, GetPermissionResponse{
			Status:      "success",
			Description: desc,
		})
	}
}

// ListPermissions godoc
// @Summary      List message permissions
// @Description  Returns all permission settings for the authenticated identity, optionally filtered by message box.
// @Tags         Permissions
// @Produce      json
// @Param        messageBox query string false "Filter by message box name"
// @Param        limit query int false "Maximum number of results (1-1000, default 100)"
// @Param        offset query int false "Number of results to skip (default 0)"
// @Param        createdAtOrder query string false "Sort order: 'asc' or 'desc' (default 'desc')"
// @Success      200  {object}  ListPermissionsResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /permissions/list [get]
func (s *Server) ListPermissions(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_UNAUTHORIZED", "Authentication required")
		return
	}

	messageBoxParam := r.URL.Query().Get("messageBox")
	var messageBox *string
	if messageBoxParam != "" {
		messageBox = &messageBoxParam
	}

	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	sortOrderParam := r.URL.Query().Get("createdAtOrder")

	limit := 100
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v >= 1 && v <= 1000 {
			limit = v
		} else {
			writeError(w, 400, "ERR_INVALID_LIMIT", "Limit must be a number between 1 and 1000")
			return
		}
	}

	offset := 0
	if offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			offset = v
		} else {
			writeError(w, 400, "ERR_INVALID_OFFSET", "Offset must be a non-negative number")
			return
		}
	}

	page, err := s.Store.ListPermissions(r.Context(), storage.PermissionQuery{
		Recipient:  identityKey,
		MessageBox: messageBox,
		Limit:      limit,
		Offset:     offset,
		Order:      sortOrder(sortOrderParam),
	})
	if err != nil {
		logger.Error("failed to list permissions", "error", err)
		writeError(w, 500, "ERR_LIST_PERMISSIONS_FAILED", "Failed to list permissions")
		return
	}

	var out []PermissionDetailList
	for _, p := range page.Items {
		out = append(out, PermissionDetailList{
			Sender:       p.Sender,
			MessageBox:   p.MessageBox,
			RecipientFee: p.RecipientFee,
			CreatedAt:    p.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			UpdatedAt:    p.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		})
	}

	if out == nil {
		out = []PermissionDetailList{}
	}

	writeJSON(w, 200, ListPermissionsResponse{
		Status:      "success",
		Permissions: out,
		TotalCount:  page.Total,
	})
}

// GetQuote godoc
// @Summary      Get a delivery quote
// @Description  Returns fee information for sending a message to one or more recipients. Single recipient returns QuoteSingleResponse, multiple recipients returns QuoteMultiResponse.
// @Tags         Permissions
// @Produce      json
// @Param        recipient query string true "Recipient public key (can be repeated for multiple recipients)"
// @Param        messageBox query string true "Name of the message box"
// @Success      200  {object}  QuoteSingleResponse "Single recipient quote"
// @Success      200  {object}  QuoteMultiResponse "Multiple recipient quote"
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /permissions/quote [get]
func (s *Server) GetQuote(w http.ResponseWriter, r *http.Request) {
	senderKey := getIdentityKey(r)
	if senderKey == "" {
		writeError(w, 401, "ERR_AUTHENTICATION_REQUIRED", "Authentication required.")
		return
	}

	messageBox := r.URL.Query().Get("messageBox")
	if messageBox == "" {
		writeError(w, 400, "ERR_MISSING_PARAMETERS", "recipient and messageBox parameters are required.")
		return
	}

	// recipients can be repeated query params
	recipients := r.URL.Query()["recipient"]
	if len(recipients) == 0 {
		writeError(w, 400, "ERR_MISSING_PARAMETERS", "recipient and messageBox parameters are required.")
		return
	}

	// Validate keys
	var invalidIdx []int
	for i, rec := range recipients {
		if !isValidPubKey(strings.TrimSpace(rec)) {
			invalidIdx = append(invalidIdx, i)
		}
	}
	if len(invalidIdx) > 0 {
		idxStrs := make([]string, len(invalidIdx))
		for i, idx := range invalidIdx {
			idxStrs[i] = strconv.Itoa(idx)
		}
		writeError(w, 400, "ERR_INVALID_PUBLIC_KEY",
			fmt.Sprintf("Invalid recipient public key at index(es): %s.", strings.Join(idxStrs, ", ")))
		return
	}

	deliveryFee, err := s.Store.GetServerDeliveryFee(r.Context(), messageBox)
	if err != nil {
		logger.Error("failed to get delivery fee", "error", err)
		writeError(w, 500, "ERR_INTERNAL", "An internal error has occurred.")
		return
	}

	// Single recipient: legacy response
	if len(recipients) == 1 {
		recipientFee, err := s.recipientFee(r.Context(), recipients[0], senderKey, messageBox)
		if err != nil {
			logger.Error("failed to get recipient fee", "error", err)
			writeError(w, 500, "ERR_INTERNAL", "An internal error has occurred.")
			return
		}
		writeJSON(w, 200, QuoteSingleResponse{
			Status:      "success",
			Description: "Message delivery quote generated.",
			Quote: QuoteSingle{
				DeliveryFee:  deliveryFee,
				RecipientFee: recipientFee,
			},
		})
		return
	}

	var quotes []QuoteEntry
	var blockedRecipients []string
	totalRecipientFees := 0
	totalDeliveryFees := 0

	for _, rec := range recipients {
		rf, err := s.recipientFee(r.Context(), rec, senderKey, messageBox)
		if err != nil {
			logger.Error("failed to get recipient fee", "error", err)
			writeError(w, 500, "ERR_INTERNAL", "An internal error has occurred.")
			return
		}

		status := "always_allow"
		if rf == -1 {
			status = "blocked"
			blockedRecipients = append(blockedRecipients, rec)
		} else if rf > 0 {
			status = "payment_required"
			totalRecipientFees += rf
		}
		totalDeliveryFees += deliveryFee

		quotes = append(quotes, QuoteEntry{
			Recipient:    rec,
			MessageBox:   messageBox,
			DeliveryFee:  deliveryFee,
			RecipientFee: rf,
			Status:       status,
		})
	}

	if quotes == nil {
		quotes = []QuoteEntry{}
	}
	if blockedRecipients == nil {
		blockedRecipients = []string{}
	}

	writeJSON(w, 200, QuoteMultiResponse{
		Status:            "success",
		Description:       fmt.Sprintf("Message delivery quotes generated for %d recipients.", len(recipients)),
		QuotesByRecipient: quotes,
		Totals: QuoteTotals{
			DeliveryFees:              totalDeliveryFees,
			RecipientFees:             totalRecipientFees,
			TotalForPayableRecipients: totalDeliveryFees + totalRecipientFees,
		},
		BlockedRecipients: blockedRecipients,
	})
}
