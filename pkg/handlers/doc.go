// Package handlers implements the MessageBox HTTP API: sending, listing and
// acknowledging messages, device registration for push notifications, message
// permissions and delivery quotes, and the optional paymail profile lookup.
//
// Handlers run behind the BRC-31 auth middleware, which identifies the caller;
// persistence goes through the storage.Store passed to NewServer.
package handlers
