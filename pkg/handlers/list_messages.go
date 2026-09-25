package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// ListMessagesConfig bounds POST /listMessages pagination, mirroring the TS
// reference server's "standard" resource profile. Any of the four fields may
// be -1, meaning no cap. When DefaultLimit is -1, parseListPagination
// substitutes safeIntegerLimit for it before bounding, mirroring TS's own
// `listDefaultLimit === -1 ? Number.MAX_SAFE_INTEGER : listDefaultLimit`.
type ListMessagesConfig struct {
	DefaultLimit     int
	MaxLimit         int
	MaxOffset        int
	MaxResponseBytes int
}

// defaultListMessagesConfig matches the TS reference server's "standard"
// resource profile defaults (config/resources.ts): a 1000-message page, an
// 8 MiB response budget, and offsets bounded to 100,000.
func defaultListMessagesConfig() ListMessagesConfig {
	return ListMessagesConfig{
		DefaultLimit:     1000,
		MaxLimit:         1000,
		MaxOffset:        100_000,
		MaxResponseBytes: 8 * 1024 * 1024,
	}
}

// routeFailure is a validation or budget failure to report as a JSON error
// response, mirroring the TS reference server's RouteFailure.
type routeFailure struct {
	status int
	code   string
	desc   string
}

func (rf *routeFailure) write(w http.ResponseWriter) {
	writeError(w, rf.status, rf.code, rf.desc)
}

// listMessagesResponseByteOverhead approximates the fixed envelope bytes
// (status, limit, offset, nextOffset, hasMore, and the messages array's own
// brackets/commas) that TS's readMessagePage seeds its byte counter with.
const listMessagesResponseByteOverhead = 256

// ListMessages godoc
// @Summary      Retrieve messages from a message box
// @Description  Returns one deterministic, bounded page of stored messages for the specified messageBox belonging to the authenticated identity, most recently paginated with limit/offset (skip is a compatibility alias for offset) and optionally filtered to a single messageId. If the box does not exist or has no messages, an empty page is returned. Existing clients that send only messageBox get the first page of up to the server's default limit.
// @Tags         Messages
// @Accept       json
// @Produce      json
// @Param        request body ListMessagesRequest true "Message box to list messages from, with optional pagination"
// @Success      200  {object}  ListMessagesResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      413  {object}  ErrorResponse "the oldest message in the page exceeds the configured response byte budget"
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /listMessages [post]
func (s *Server) ListMessages(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, http.StatusUnauthorized, "ERR_AUTH_REQUIRED", "Authentication required")
		return
	}

	req, rf := decodeListMessagesRequest(r)
	if rf != nil {
		rf.write(w)
		return
	}

	messageBox, rf := normalizeMessageBoxName(req.MessageBox)
	if rf != nil {
		rf.write(w)
		return
	}

	messageID, rf := parseOptionalMessageID(req.MessageID)
	if rf != nil {
		rf.write(w)
		return
	}

	pag, rf := parseListPagination(req.Limit, req.Offset, req.Skip, s.listMessages)
	if rf != nil {
		rf.write(w)
		return
	}

	resp, rf, err := s.readMessagePage(r.Context(), identityKey, messageBox, pag, messageID, s.listMessages.MaxResponseBytes)
	if err != nil {
		logger.Error("failed to list messages", "error", err)
		writeError(w, http.StatusInternalServerError, "ERR_INTERNAL_ERROR", "An internal error has occurred while listing messages.")
		return
	}
	if rf != nil {
		rf.write(w)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// decodeListMessagesRequest decodes r's JSON body into a ListMessagesRequest.
// An empty body decodes as io.EOF from json.Decoder, not a malformed-JSON
// error; the TS reference server's body-parser.json() middleware defaults an
// empty body to `{}` and falls through to ordinary field validation (which
// reports ERR_MESSAGEBOX_REQUIRED for the resulting missing messageBox), so an
// empty body here is treated the same way rather than as a bespoke
// ERR_INVALID_JSON with no TS counterpart. Any other decode failure
// (malformed, non-empty JSON) is still ERR_INVALID_JSON.
func decodeListMessagesRequest(r *http.Request) (ListMessagesRequest, *routeFailure) {
	var req ListMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return ListMessagesRequest{}, &routeFailure{http.StatusBadRequest, "ERR_INVALID_JSON", "Invalid JSON body"}
	}
	return req, nil
}

// normalizeMessageBoxName validates the messageBox field, mirroring the TS
// reference server's normalizeMessageBoxName exactly: a missing, null, or
// blank/whitespace-only value is "required"; a value of any other JSON type,
// or one that fails the canonical-text bounds, is "invalid".
func normalizeMessageBoxName(raw json.RawMessage) (string, *routeFailure) {
	missing := &routeFailure{http.StatusBadRequest, "ERR_MESSAGEBOX_REQUIRED", "Please provide the name of a valid MessageBox!"}
	invalidType := &routeFailure{http.StatusBadRequest, "ERR_INVALID_MESSAGEBOX", "MessageBox name must be a string!"}

	if !jsonRawPresent(raw) {
		return "", missing
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", invalidType
	}
	s, ok := v.(string)
	if !ok {
		return "", invalidType
	}
	if trimmedEmpty(s) {
		return "", missing
	}
	if !isCanonicalMessageBox(s) {
		return "", &routeFailure{
			http.StatusBadRequest, "ERR_INVALID_MESSAGEBOX",
			fmt.Sprintf("MessageBox names must be exact, control-free strings of at most %d bytes.", maxMessageBoxBytes),
		}
	}
	return s, nil
}

// parseOptionalMessageID validates the optional messageId field, mirroring
// the TS reference server's `messageId !== undefined && !isCanonicalMessageId(messageId)`
// exactly: only a field that is genuinely absent from the JSON body means "no
// filter". An explicit JSON null is present (JS's `!==` is strict, unlike the
// `== null`/`??` checks normalizeMessageBoxName and parseListPagination use
// elsewhere), and isCanonicalMessageId(null) is false, so it is an error, not
// "no filter", the same as any other non-string or non-canonical value.
func parseOptionalMessageID(raw json.RawMessage) (*string, *routeFailure) {
	if len(raw) == 0 {
		return nil, nil
	}
	invalid := &routeFailure{http.StatusBadRequest, "ERR_INVALID_MESSAGE_ID", "messageId must be an exact bounded control-free string."}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, invalid
	}
	s, ok := v.(string)
	if !ok || !isCanonicalMessageID(s) {
		return nil, invalid
	}
	return &s, nil
}

// listPagination is the resolved, validated limit/offset a page is fetched
// with.
type listPagination struct {
	Limit  int
	Offset int
}

// parseListPagination validates and resolves limit/offset/skip, mirroring the
// TS reference server's parseListPagination:
//  1. If both offset and skip are given, they must be equal (compared as raw
//     JSON values, so a type mismatch such as offset:5 vs skip:"5" is itself a
//     mismatch, independent of whether either would parse as a valid integer).
//  2. limit defaults to cfg.DefaultLimit when omitted; offset defaults to skip,
//     then to 0.
//  3. Both are then bounds-checked as safe integers.
func parseListPagination(limitRaw, offsetRaw, skipRaw json.RawMessage, cfg ListMessagesConfig) (listPagination, *routeFailure) {
	offsetProvided, offsetVal := decodeJSONValue(offsetRaw)
	skipProvided, skipVal := decodeJSONValue(skipRaw)
	limitProvided, limitVal := decodeJSONValue(limitRaw)

	if offsetProvided && skipProvided && !reflect.DeepEqual(offsetVal, skipVal) {
		return listPagination{}, &routeFailure{http.StatusBadRequest, "ERR_INVALID_OFFSET", "offset and skip must match when both are provided."}
	}

	limitAny := limitVal
	if !limitProvided {
		// Mirror TS's `resources.listDefaultLimit === -1 ? Number.MAX_SAFE_INTEGER
		// : resources.listDefaultLimit`: an operator-configured "unlimited"
		// default (-1) must still pass boundedInteger's minimum-1 check, which a
		// literal -1 never would.
		configuredDefault := cfg.DefaultLimit
		if configuredDefault == -1 {
			configuredDefault = safeIntegerLimit
		}
		limitAny = float64(configuredDefault)
	}

	var offsetAny any = float64(0)
	switch {
	case offsetProvided:
		offsetAny = offsetVal
	case skipProvided:
		offsetAny = skipVal
	}

	limit, ok := boundedInteger(limitAny, 1, cfg.MaxLimit)
	if !ok {
		return listPagination{}, &routeFailure{
			http.StatusBadRequest, "ERR_INVALID_LIMIT",
			fmt.Sprintf("limit must be an integer between 1 and %s.", boundDisplay(cfg.MaxLimit)),
		}
	}
	offset, ok := boundedInteger(offsetAny, 0, cfg.MaxOffset)
	if !ok {
		return listPagination{}, &routeFailure{
			http.StatusBadRequest, "ERR_INVALID_OFFSET",
			fmt.Sprintf("offset must be an integer between 0 and %s.", boundDisplay(cfg.MaxOffset)),
		}
	}

	return listPagination{Limit: limit, Offset: offset}, nil
}

// boundDisplay renders a MaxLimit/MaxOffset bound for an error message; -1
// means no cap. It matches the TS reference server's own rendering exactly
// (`resources.listMaxLimit === -1 ? 'the JavaScript safe-integer maximum' :
// String(resources.listMaxLimit)`, and the offset equivalent), so the two
// servers' ERR_INVALID_LIMIT/ERR_INVALID_OFFSET description text is
// byte-for-byte identical for the same configuration.
func boundDisplay(max int) string {
	if max == -1 {
		return "the JavaScript safe-integer maximum"
	}
	return strconv.Itoa(max)
}

// jsonRawPresent reports whether raw holds a JSON value other than an absent
// field or a literal null — the Go equivalent of a JS value being non-null.
func jsonRawPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// decodeJSONValue decodes raw into an any for comparison and bounds-checking.
// It reports false if the field was absent or explicitly null, matching how
// JS's ?? treats undefined and null alike.
func decodeJSONValue(raw json.RawMessage) (present bool, value any) {
	if !jsonRawPresent(raw) {
		return false, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Malformed JSON for this one field: treat it as present with a value no
		// bounds check can accept, so the caller reports the field's own
		// ERR_INVALID_* code rather than a generic decode failure.
		return true, v
	}
	return true, v
}

// safeIntegerLimit is JavaScript's Number.MAX_SAFE_INTEGER, the bound
// Number.isSafeInteger enforces and which boundedInteger mirrors.
const safeIntegerLimit = 1<<53 - 1

// boundedInteger reports whether v is a whole number within [min, max],
// mirroring the TS reference server's isBoundedInteger. max == -1 means no
// upper bound. Only float64 (what encoding/json decodes a JSON number as into
// an any) can ever satisfy it, matching Number.isSafeInteger's own rejection
// of non-numbers.
func boundedInteger(v any, min, max int) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	if f != float64(int64(f)) {
		return 0, false
	}
	if f < -safeIntegerLimit || f > safeIntegerLimit {
		return 0, false
	}
	iv := int64(f)
	if iv < int64(min) {
		return 0, false
	}
	if max != -1 && iv > int64(max) {
		return 0, false
	}
	return int(iv), true
}

// readMessagePage fetches and formats one page, mirroring the TS reference
// server's readMessagePage/appendMessage/appendMessageRows: it fetches one
// row beyond pag.Limit so it can tell whether another page exists, then walks
// the rows in order, stopping at the limit or at the first row that would
// exceed maxResponseBytes. A row that alone exceeds the budget is reported as
// 413 only when it is the very first row of the page (an oversized message
// deeper in the page just ends the page early with HasMore true, since a
// client can still make progress by acknowledging what came before it).
//
// It returns a non-nil error only for a storage failure; a validation-style
// problem (413) comes back as a *routeFailure with a nil error.
func (s *Server) readMessagePage(ctx context.Context, recipient, messageBox string, pag listPagination, messageID *string, maxResponseBytes int) (ListMessagesResponse, *routeFailure, error) {
	rows, err := s.fetchMessagePageRows(ctx, recipient, messageBox, pag.Offset, pag.Limit+1, messageID)
	if err != nil {
		return ListMessagesResponse{}, nil, err
	}

	out := make([]MessageOut, 0, min(len(rows), pag.Limit))
	encodedBytes := listMessagesResponseByteOverhead
	hasMore := false

	for _, m := range rows {
		if len(out) >= pag.Limit {
			hasMore = true
			break
		}

		formatted := MessageOut{
			MessageID: m.MessageID,
			Body:      m.Body,
			Sender:    m.Sender,
			CreatedAt: formatTime(m.CreatedAt),
			UpdatedAt: formatTime(m.UpdatedAt),
		}
		// marshalJSONNoEscape (not json.Marshal) so that a body/sender/messageId
		// containing '<', '>' or '&' is counted the way TS's
		// Buffer.byteLength(JSON.stringify(formatted), 'utf8') would count it —
		// JSON.stringify never HTML-escapes those characters — rather than the 6
		// bytes each apiece encoding/json's default HTML-safe escaping produces.
		encoded, err := marshalJSONNoEscape(formatted)
		if err != nil {
			return ListMessagesResponse{}, nil, err
		}
		itemBytes := len(encoded) + 1 // +1 approximates the array separator.

		if maxResponseBytes != -1 && encodedBytes+itemBytes > maxResponseBytes {
			if len(out) == 0 {
				return ListMessagesResponse{}, &routeFailure{
					http.StatusRequestEntityTooLarge, "ERR_MESSAGE_RESPONSE_TOO_LARGE",
					"The oldest message exceeds the configured listing response budget.",
				}, nil
			}
			hasMore = true
			break
		}

		out = append(out, formatted)
		encodedBytes += itemBytes
	}

	return ListMessagesResponse{
		Status:     "success",
		Messages:   out,
		Limit:      pag.Limit,
		Offset:     pag.Offset,
		NextOffset: pag.Offset + len(out),
		HasMore:    hasMore,
	}, nil, nil
}

// fetchMessagePageRows fetches the raw candidate rows for one page: up to
// fetchLimit messages starting at offset, ordered by CreatedAt then
// MessageID, optionally filtered to a single messageID. It prefers
// storage.MessagePager when the backend implements it, and otherwise pages in
// memory over the full ListMessages result — correct for any Store, including
// one plugged in from outside this module that only implements the required
// interfaces.
func (s *Server) fetchMessagePageRows(ctx context.Context, recipient, messageBox string, offset, fetchLimit int, messageID *string) ([]storage.Message, error) {
	if pager, ok := s.Store.(storage.MessagePager); ok {
		return pager.PageMessages(ctx, storage.MessagePageQuery{
			Recipient:  recipient,
			MessageBox: messageBox,
			Offset:     offset,
			FetchLimit: fetchLimit,
			MessageID:  messageID,
		})
	}
	return fallbackPageMessages(ctx, s.Store, recipient, messageBox, offset, fetchLimit, messageID)
}

// fallbackPageMessages implements the storage.MessagePager contract in memory
// on top of the required MessageStore.ListMessages, for a Store that does not
// implement the optional pager. ListMessages already returns the box fully
// sorted, so filtering and slicing it is enough to be correct; it is simply
// O(box size) per call rather than pushed down to the backend.
func fallbackPageMessages(ctx context.Context, store storage.MessageStore, recipient, messageBox string, offset, fetchLimit int, messageID *string) ([]storage.Message, error) {
	all, err := store.ListMessages(ctx, recipient, messageBox)
	if err != nil {
		return nil, err
	}

	if messageID != nil {
		filtered := make([]storage.Message, 0, 1)
		for _, m := range all {
			if m.MessageID == *messageID {
				filtered = append(filtered, m)
			}
		}
		all = filtered
	}

	if offset >= len(all) {
		return nil, nil
	}
	end := offset + fetchLimit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

// trimmedEmpty reports whether s is empty or made only of whitespace, the Go
// equivalent of the TS reference server's value.trim() === "" check.
func trimmedEmpty(s string) bool {
	for _, r := range s {
		if !isJSWhitespace(r) {
			return false
		}
	}
	return true
}

// isJSWhitespace is the set of runes JS's String.prototype.trim() treats as
// trimmable whitespace: WhiteSpace and LineTerminator per the ECMAScript
// spec. It deliberately excludes U+0085 (NEL) and the other C1 controls,
// which JS's trim() leaves untouched but which containsControlCharacter (and
// so isCanonicalMessageBox) rejects as controls instead — a messageBox of just
// "\u0085" must fail as ERR_INVALID_MESSAGEBOX, not be read as blank and fail
// as ERR_MESSAGEBOX_REQUIRED.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', ' ', 0xA0, 0xFEFF, '\n', '\r', 0x2028, 0x2029, 0x1680, 0x202F, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
