package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
)

// AcknowledgeMessage godoc
// @Summary      Acknowledge receipt of messages
// @Description  Removes acknowledged messages from the database for the authenticated identity. Used after a client has received and processed messages.
// @Tags         Messages
// @Accept       json
// @Produce      json
// @Param        request body AcknowledgeMessageRequest true "Message IDs to acknowledge"
// @Success      200  {object}  SuccessResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /acknowledgeMessage [post]
func (s *Server) AcknowledgeMessage(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_AUTH_REQUIRED", "Authentication required")
		return
	}

	var req AcknowledgeMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "ERR_INVALID_JSON", "Invalid JSON body")
		return
	}

	if len(req.MessageIDs) == 0 {
		writeError(w, 400, "ERR_MESSAGE_ID_REQUIRED", "Please provide the ID of the message(s) to acknowledge!")
		return
	}

	for _, id := range req.MessageIDs {
		if _, ok := id, true; !ok {
			writeError(w, 400, "ERR_INVALID_MESSAGE_ID", "Message IDs must be formatted as an array of strings!")
			return
		}
	}

	deleted, err := s.Store.AcknowledgeMessages(r.Context(), identityKey, req.MessageIDs)
	if err != nil {
		logger.Error("failed to acknowledge messages", "error", err)
		writeError(w, 500, "ERR_INTERNAL_ERROR", "An internal error has occurred while acknowledging the message")
		return
	}

	if deleted == 0 {
		writeError(w, 400, "ERR_INVALID_ACKNOWLEDGMENT", "Message not found!")
		return
	}

	writeJSON(w, 200, SuccessResponse{Status: "success"})
}
