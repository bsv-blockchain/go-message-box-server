package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
)

// replyTimeout bounds one signed reply to the socket that sent an event.
const replyTimeout = 10 * time.Second

// maxRoomBytes is an identity key, a dash and the longest messageBox.
const maxRoomBytes = 66 + 1 + maxMessageBoxBytes

// AttachWebSockets serves the MessageBox live-delivery events on io, ported
// from the TypeScript server's attachMessageBoxWebSockets (src/compose.ts):
//
//	authenticated {identityKey?}       -> authenticationSuccess | authenticationFailed
//	joinRoom "<identityKey>-<box>"     -> joinedRoom {roomId} | joinFailed
//	leaveRoom "<identityKey>-<box>"    -> leftRoom {roomId} | leaveFailed
//	sendMessage {roomId, message}      -> sendMessageAck-<roomId> | messageFailed
//
// Every event, in and out, is a signed BRC-103 general message. A socket must
// emit authenticated before it can join rooms or send. Messages stored by
// sendMessage, over the socket or over HTTP, are pushed to the recipient's
// joined sockets as sendMessage-<roomId>. A socket.io reconnect may resume its
// BRC-103 session for livesocket.DefaultResumeWindow (livesocket.SessionStore).
func (s *Server) AttachWebSockets(io *socket.Server, reg *livesocket.Registry) {
	s.live = reg
	sessions := livesocket.NewSessionStore(livesocket.DefaultResumeWindow)
	_ = io.On("connection", func(args ...any) {
		sock, ok := args[0].(*socket.Socket)
		if !ok {
			return
		}
		a, err := livesocket.NewAuthSocket(sock, s.wallet, sessions)
		if err != nil {
			slog.Error("live socket setup failed", "error", err)
			livesocket.Drop(sock)
			return
		}
		if !reg.Register(a) {
			slog.Warn("rejected live socket: at the process connection limit, or already gone")
			livesocket.Drop(sock)
			return
		}
		c := &liveConn{
			s: s, a: a, reg: reg, id: string(sock.Id()),
			sendLimit:    livesocket.NewMinuteLimiter(reg.Limits().SendRatePerMinute),
			controlLimit: livesocket.NewMinuteLimiter(reg.Limits().ControlRatePerMinute),
		}
		a.On("authenticated", c.authenticated)
		a.On("joinRoom", c.joinRoom)
		a.On("leaveRoom", c.leaveRoom)
		a.On("sendMessage", c.sendMessage)
	})
}

// liveConn handles one socket's events. The AuthSocket calls its handlers one
// at a time, in arrival order.
type liveConn struct {
	s            *Server
	a            *livesocket.AuthSocket
	reg          *livesocket.Registry
	id           string
	sendLimit    *livesocket.MinuteLimiter
	controlLimit *livesocket.MinuteLimiter
	authHandled  bool
}

func (c *liveConn) reply(event string, data any) {
	ctx, cancel := context.WithTimeout(context.Background(), replyTimeout)
	defer cancel()
	if err := c.a.Emit(ctx, event, data); err != nil {
		slog.Warn("live socket reply failed", "sid", c.id, "event", event, "error", err)
	}
}

// authenticated binds the identity the BRC-103 handshake proved. A claimed
// identityKey in the payload may confirm that identity but never replace it.
// Only the first authenticated event counts.
func (c *liveConn) authenticated(data any) {
	if c.authHandled {
		return
	}
	c.authHandled = true

	identity := c.a.IdentityKey().ToDERHex()
	reason := ""
	if m, ok := data.(map[string]any); ok {
		if claim, ok := m["identityKey"].(string); ok && strings.TrimFunc(claim, isJSWhitespace) != identity {
			reason = "Identity claim does not match authenticated peer"
		}
	}
	if reason == "" && !c.reg.Authenticate(c.id, identity) {
		reason = "WebSocket identity connection limit exceeded"
	}
	if reason != "" {
		c.reply("authenticationFailed", map[string]any{"reason": reason})
		c.a.Socket().Disconnect(false)
		return
	}
	c.reply("authenticationSuccess", map[string]any{"status": "success"})
}

func (c *liveConn) joinRoom(data any) {
	room, _ := data.(string)
	named := map[string]any{}
	if !jsBlank(room) {
		named["roomId"] = room
	}
	fail := func(reason, code string) {
		named["reason"], named["code"] = reason, code
		c.reply("joinFailed", named)
	}
	switch identity := c.reg.Identity(c.id); {
	case !c.controlLimit.Allow():
		fail("WebSocket control-event rate limit exceeded", "ERR_WEBSOCKET_CONTROL_RATE_LIMITED")
	case identity == "":
		fail("Unauthorized: WebSocket not authenticated", "ERR_WEBSOCKET_NOT_AUTHENTICATED")
	case jsBlank(room):
		c.reply("joinFailed", map[string]any{"reason": "Invalid room ID", "code": "ERR_WEBSOCKET_INVALID_ROOM"})
	case !isIdentityOwnedRoom(identity, room):
		fail("Room is not owned by authenticated identity", "ERR_WEBSOCKET_ROOM_NOT_OWNED")
	case !c.reg.Join(c.id, room):
		fail("WebSocket room limit exceeded", "ERR_WEBSOCKET_ROOM_LIMIT")
	default:
		c.reply("joinedRoom", map[string]any{"roomId": room})
	}
}

func (c *liveConn) leaveRoom(data any) {
	room, _ := data.(string)
	fail := func(reason, code string) {
		m := map[string]any{"reason": reason}
		if code != "" {
			m["code"] = code
		}
		c.reply("leaveFailed", m)
	}
	switch identity := c.reg.Identity(c.id); {
	case !c.controlLimit.Allow():
		fail("WebSocket control-event rate limit exceeded", "ERR_WEBSOCKET_CONTROL_RATE_LIMITED")
	case identity == "":
		fail("Unauthorized: WebSocket not authenticated", "")
	case jsBlank(room):
		fail("Invalid room ID", "")
	case !isIdentityOwnedRoom(identity, room):
		fail("Room is not owned by authenticated identity", "")
	default:
		c.reg.Leave(c.id, room)
		c.reply("leftRoom", map[string]any{"roomId": room})
	}
}

// sendMessage stores a message through the HTTP route's own policy
// (permissions, fees, duplicates), then acks the sender. Storing it announces
// it to the recipient's sockets, as an HTTP send does. A recipient fee makes
// the route ask for a payment the socket event cannot carry, so such a send
// is acked as an error and the client falls back to HTTP.
func (c *liveConn) sendMessage(data any) {
	if !c.sendLimit.Allow() {
		c.reply("messageFailed", map[string]any{"reason": "WebSocket send rate limit exceeded", "code": "ERR_WEBSOCKET_RATE_LIMITED"})
		return
	}
	d, ok := data.(map[string]any)
	if !ok {
		c.reply("messageFailed", map[string]any{"reason": "Invalid data object"})
		return
	}
	sender := c.reg.Identity(c.id)
	if sender == "" {
		c.reply("paymentFailed", map[string]any{"reason": "Unauthorized: WebSocket not authenticated"})
		return
	}
	room, msg, reason := validateLiveMessage(d["roomId"], d["message"], c.reg.Limits().MaxMessageBodyBytes)
	if reason != "" {
		c.reply("messageFailed", map[string]any{"reason": reason})
		return
	}

	reqBody, err := json.Marshal(SendMessageRequest{Message: &SendMessageBody{
		MessageID:  mustJSON(msg.messageID),
		Recipient:  mustJSON(msg.recipient),
		MessageBox: msg.messageBox,
		Body:       mustJSON(msg.body),
	}})
	if err != nil {
		c.reply("messageFailed", map[string]any{"reason": "Unexpected error occurred"})
		return
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/sendMessage", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	c.s.sendMessage(rec, r, sender)

	var res struct {
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Status != statusSuccess {
		code := res.Code
		if code == "" {
			code = "ERR_MESSAGE_REJECTED"
		}
		c.reply("sendMessageAck-"+room, map[string]any{"status": "error", "code": code})
		return
	}
	c.reply("sendMessageAck-"+room, map[string]any{"status": "success", "messageId": msg.messageID})
}

type liveMessage struct {
	messageID, recipient, messageBox, body string
}

// validateLiveMessage is the TypeScript server's validateWebSocketMessage: the
// room must be "<recipient>-<messageBox>" for the message's canonical
// recipient, and body and messageId must be well-formed strings. It returns a
// non-empty reason on failure.
func validateLiveMessage(roomID, message any, maxBodyBytes int) (string, liveMessage, string) {
	room, _ := roomID.(string)
	if jsBlank(room) {
		return "", liveMessage{}, "Invalid room ID"
	}
	m, ok := message.(map[string]any)
	if !ok {
		return "", liveMessage{}, "Invalid message object"
	}
	body, ok := m["body"].(string)
	if !ok || jsBlank(body) || (maxBodyBytes != -1 && len(body) > maxBodyBytes) {
		return "", liveMessage{}, "Invalid message body"
	}
	recipient, ok := m["recipient"].(string)
	if !ok {
		return "", liveMessage{}, "Invalid recipient identity key"
	}
	canonical := canonicalIdentityKey(recipient)
	if canonical == "" {
		return "", liveMessage{}, "Invalid recipient identity key"
	}
	if canonical != recipient {
		return "", liveMessage{}, "Recipient identity key must use canonical encoding"
	}
	id, _ := m["messageId"].(string)
	if !isCanonicalMessageID(id) {
		return "", liveMessage{}, "Invalid message ID"
	}
	if !isIdentityOwnedRoom(recipient, room) {
		return "", liveMessage{}, "Room does not match canonical recipient and message box"
	}
	return room, liveMessage{messageID: id, recipient: recipient, messageBox: room[len(recipient)+1:], body: body}, ""
}

// isIdentityOwnedRoom reports whether room is "<identityKey>-<messageBox>"
// with a canonical messageBox, the TypeScript server's isIdentityOwnedRoom.
func isIdentityOwnedRoom(identityKey, room string) bool {
	if jsBlank(room) || len(room) > maxRoomBytes {
		return false
	}
	box, ok := strings.CutPrefix(room, identityKey+"-")
	return ok && box != "" && isCanonicalMessageBox(box) && !containsControlCharacter(identityKey)
}

// canonicalIdentityKey returns key as a compressed hex public key, or "" if
// it does not parse.
func canonicalIdentityKey(key string) string {
	if jsBlank(key) {
		return ""
	}
	pk, err := ec.PublicKeyFromString(key)
	if err != nil {
		return ""
	}
	return pk.ToDERHex()
}

// jsBlank reports whether s is empty after JS String.prototype.trim().
func jsBlank(s string) bool { return strings.TrimFunc(s, isJSWhitespace) == "" }

func mustJSON(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // unreachable: a string always encodes
	}
	return b
}

// announceStored pushes each stored copy of an unpaid send to the recipient's
// joined sockets, off the request goroutine. A paid send is left to
// listMessages, which internalizes the payment; the push carries no payment.
// Only string bodies are pushed, as in the TypeScript server.
func (s *Server) announceStored(ctx context.Context, sender, messageBox string, body json.RawMessage, paid bool, results []SendMessageResult) {
	if s.live == nil || paid {
		return
	}
	var text string
	if json.Unmarshal(body, &text) != nil {
		return
	}
	go func() {
		for _, r := range results {
			s.live.Deliver(ctx, sender, r.Recipient, messageBox, r.MessageID, text)
		}
	}()
}
