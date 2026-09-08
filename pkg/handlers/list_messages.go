package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
)

// ListMessages godoc
// @Summary      Retrieve messages from a message box
// @Description  Returns all stored messages for the specified messageBox belonging to the authenticated identity. If the box does not exist or has no messages, an empty array is returned.
// @Tags         Messages
// @Accept       json
// @Produce      json
// @Param        request body ListMessagesRequest true "Message box to list messages from"
// @Success      200  {object}  ListMessagesResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /listMessages [post]
func (s *Server) ListMessages(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_AUTH_REQUIRED", "Authentication required")
		return
	}

	var req ListMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "ERR_INVALID_JSON", "Invalid JSON body")
		return
	}

	if req.MessageBox == "" {
		writeError(w, 400, "ERR_MESSAGEBOX_REQUIRED", "Please provide the name of a valid MessageBox!")
		return
	}

	msgs, err := s.Store.ListMessages(r.Context(), identityKey, req.MessageBox)
	if err != nil {
		logger.Error("failed to list messages", "error", err)
		writeError(w, 500, "ERR_INTERNAL_ERROR", "An internal error has occurred while listing messages.")
		return
	}

	var out []MessageOut
	for _, m := range msgs {
		out = append(out, MessageOut{
			MessageID: m.MessageID,
			Body:      m.Body,
			Sender:    m.Sender,
			CreatedAt: m.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			UpdatedAt: m.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		})
	}

	if out == nil {
		out = []MessageOut{}
	}

	writeJSON(w, 200, ListMessagesResponse{
		Status:   "success",
		Messages: out,
	})
}
