// Package logger is a toggleable wrapper around log/slog for the server's debug
// output. Log and Warn are silent until Enable is called (main enables them in
// development); Error always logs.
package logger

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
)

var enabled atomic.Bool

// Enable turns on logging.
func Enable() {
	enabled.Store(true)
}

// Disable turns off logging.
func Disable() {
	enabled.Store(false)
}

// IsEnabled returns whether logging is enabled.
func IsEnabled() bool {
	return enabled.Load()
}

var std = newStd()

func newStd() *atomic.Pointer[slog.Logger] {
	p := new(atomic.Pointer[slog.Logger])
	p.Store(newLogger(os.Stdout))
	return p
}

func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// SetOutput sends the log to w instead of stdout and returns a function that
// puts back what was there. It lets a test read what the server would have
// shipped to the log store.
func SetOutput(w io.Writer) (restore func()) {
	prev := std.Swap(newLogger(w))
	return func() { std.Store(prev) }
}

// Log logs a message if logging is enabled.
func Log(msg string, args ...any) {
	if enabled.Load() {
		std.Load().Info(msg, args...)
	}
}

// Warn logs a warning if logging is enabled.
func Warn(msg string, args ...any) {
	if enabled.Load() {
		std.Load().Warn(msg, args...)
	}
}

// Error always logs errors.
func Error(msg string, args ...any) {
	std.Load().Error(msg, args...)
}
