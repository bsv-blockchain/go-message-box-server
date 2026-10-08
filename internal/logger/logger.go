// Package logger is a toggleable wrapper around log/slog for the server's debug
// output. Log and Warn are silent until Enable is called (main enables them in
// development); Error always logs.
package logger

import (
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

var std = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

// Log logs a message if logging is enabled.
func Log(msg string, args ...any) {
	if enabled.Load() {
		std.Info(msg, args...)
	}
}

// Warn logs a warning if logging is enabled.
func Warn(msg string, args ...any) {
	if enabled.Load() {
		std.Warn(msg, args...)
	}
}

// Error always logs errors.
func Error(msg string, args ...any) {
	std.Error(msg, args...)
}
