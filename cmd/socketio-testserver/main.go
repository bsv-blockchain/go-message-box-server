// Command socketio-testserver serves only the livesocket Socket.IO endpoint,
// with no storage, payments or HTTP API, so external clients (the TypeScript
// socket.io-client and @bsv/authsocket-client) can check wire compatibility
// against the Go server. It signs with a fresh random key each run.
//
// Raw Socket.IO events (no BRC-103):
//
//	echo (with ack) -> ack with the same args
//
// Authenticated events, as signed BRC-103 general messages, shaped like the
// MessageBox ones:
//
//	authenticated            -> authenticationSuccess {status}
//	joinRoom roomId          -> joinedRoom {roomId}
//	sendMessage {roomId,...} -> sendMessage-<roomId> to every socket in the room,
//	                            then sendMessageAck-<roomId> {status, messageId} to the sender
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/zishang520/socket.io/servers/socket/v3"

	"github.com/bsv-blockchain/go-message-box-server/pkg/livesocket"
)

// rooms tracks which authenticated sockets joined which room.
type rooms struct {
	mu sync.Mutex
	m  map[string]map[*livesocket.AuthSocket]struct{}
}

func (r *rooms) join(room string, a *livesocket.AuthSocket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[room] == nil {
		r.m[room] = map[*livesocket.AuthSocket]struct{}{}
	}
	r.m[room][a] = struct{}{}
}

func (r *rooms) leaveAll(a *livesocket.AuthSocket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, members := range r.m {
		delete(members, a)
	}
}

func (r *rooms) members(room string) []*livesocket.AuthSocket {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*livesocket.AuthSocket, 0, len(r.m[room]))
	for a := range r.m[room] {
		out = append(out, a)
	}
	return out
}

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()

	key, err := ec.NewPrivateKey()
	if err != nil {
		slog.Error("key", "error", err)
		os.Exit(1)
	}
	w, err := wallet.NewCompletedProtoWallet(key)
	if err != nil {
		slog.Error("wallet", "error", err)
		os.Exit(1)
	}

	rs := &rooms{m: map[string]map[*livesocket.AuthSocket]struct{}{}}
	io := livesocket.NewServer(livesocket.Options{})
	_ = io.On("connection", func(args ...any) {
		s := args[0].(*socket.Socket)
		slog.Info("connected", "sid", s.Id(), "transport", s.Conn().Transport().Name())
		_ = s.Conn().Once("upgrade", func(...any) {
			slog.Info("upgraded", "sid", s.Id(), "transport", s.Conn().Transport().Name())
		})

		_ = s.On("echo", func(args ...any) {
			if ack, ok := args[len(args)-1].(socket.Ack); ok {
				ack(args[:len(args)-1], nil)
			}
		})

		a, err := livesocket.NewAuthSocket(s, w, nil)
		if err != nil {
			slog.Error("auth socket", "sid", s.Id(), "error", err)
			s.Disconnect(true)
			return
		}
		emit := func(event string, data any) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.Emit(ctx, event, data); err != nil {
				slog.Warn("emit", "sid", s.Id(), "event", event, "error", err)
			}
		}

		a.On("authenticated", func(any) {
			slog.Info("authenticated", "sid", s.Id(), "identityKey", a.IdentityKey().ToDERHex())
			emit("authenticationSuccess", map[string]any{"status": "success"})
		})
		a.On("joinRoom", func(data any) {
			room, _ := data.(string)
			rs.join(room, a)
			emit("joinedRoom", map[string]any{"roomId": room})
		})
		a.On("sendMessage", func(data any) {
			msg, _ := data.(map[string]any)
			room, _ := msg["roomId"].(string)
			for _, m := range rs.members(room) {
				// Each recipient has its own Peer session, so each gets its own signed copy.
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := m.Emit(ctx, "sendMessage-"+room, msg); err != nil {
						slog.Warn("deliver", "sid", m.Socket().Id(), "error", err)
					}
				}()
			}
			var messageID any
			if inner, ok := msg["message"].(map[string]any); ok {
				messageID = inner["messageId"]
			}
			emit("sendMessageAck-"+room, map[string]any{"status": "success", "messageId": messageID})
		})
		_ = s.On("disconnect", func(args ...any) {
			rs.leaveAll(a)
			slog.Info("disconnected", "sid", s.Id(), "reason", args[0])
		})
	})

	mux := http.NewServeMux()
	mux.Handle(livesocket.Path, io.ServeHandler(nil))
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("socket.io test server listening", "addr", *addr, "path", livesocket.Path,
		"serverIdentityKey", key.PubKey().ToDERHex())
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
