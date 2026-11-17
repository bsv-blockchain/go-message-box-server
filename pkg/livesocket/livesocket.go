// Package livesocket serves the Socket.IO endpoint that MessageBox clients
// (@bsv/authsocket-client, @bsv/message-box-client) connect to for live
// delivery, matching the TypeScript server's socket.io 4.x setup.
package livesocket

import (
	"github.com/zishang520/socket.io/servers/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"
)

// Path is the ServeMux pattern the handler must be mounted on. socket.io-client
// connects to /socket.io/ by default.
const Path = "/socket.io/"

// Options mirrors the knobs the TS server sets in createMessageBoxWebSocketOptions.
type Options struct {
	// MaxHTTPBufferSize caps one message in bytes. Zero means 1 MiB, the TS default.
	MaxHTTPBufferSize int64
	// CorsOrigin is the allowed Origin. Empty means "*".
	CorsOrigin string
}

// NewServer returns a Socket.IO server. Mount it with
// mux.Handle(Path, srv.ServeHandler(nil)) and register handlers with
// srv.On("connection", ...).
func NewServer(o Options) *socket.Server {
	if o.MaxHTTPBufferSize == 0 {
		o.MaxHTTPBufferSize = 1 << 20
	}
	if o.CorsOrigin == "" {
		o.CorsOrigin = "*"
	}
	opts := socket.DefaultServerOptions()
	opts.SetMaxHttpBufferSize(o.MaxHTTPBufferSize)
	opts.SetCors(&types.Cors{
		Origin:  o.CorsOrigin,
		Methods: []string{"GET", "POST"},
	})
	return socket.NewServer(nil, opts)
}
