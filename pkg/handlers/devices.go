package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// RegisterDevice godoc
// @Summary      Register a device for push notifications
// @Description  Registers a device with an FCM token for receiving push notifications. Supports iOS, Android, and web platforms. A registration belongs to the authenticated identity and the token together: several identities can register the same token (a wallet with several profiles on one install), and each is pushed to separately. Registering again is idempotent and returns the same deviceId.
// @Tags         Devices
// @Accept       json
// @Produce      json
// @Param        request body RegisterDeviceRequest true "Device registration details"
// @Success      200  {object}  RegisterDeviceResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /registerDevice [post]
func (s *Server) RegisterDevice(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_AUTHENTICATION_REQUIRED", "Authentication required.")
		return
	}
	s.registerDevice(w, r, identityKey)
}

// registerDevice is RegisterDevice for an already-authenticated identity.
func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request, identityKey string) {
	var req RegisterDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "ERR_INVALID_JSON", "Invalid JSON body")
		return
	}

	if req.FCMToken == "" {
		writeError(w, 400, "ERR_INVALID_FCM_TOKEN", "fcmToken is required and must be a non-empty string.")
		return
	}

	validPlatforms := map[string]bool{"ios": true, "android": true, "web": true}
	if req.Platform != nil && !validPlatforms[*req.Platform] {
		writeError(w, 400, "ERR_INVALID_PLATFORM", "platform must be one of: ios, android, web")
		return
	}

	newDevice := storage.NewDevice{
		IdentityKey: identityKey,
		FCMToken:    req.FCMToken,
		DeviceID:    req.DeviceID,
		Platform:    req.Platform,
	}
	id, err := s.Store.RegisterDevice(r.Context(), newDevice)
	if err != nil {
		logger.Error("failed to register device", "error", err)
		writeError(w, 500, "ERR_DATABASE_ERROR", "Failed to register device.")
		return
	}

	writeJSON(w, 200, RegisterDeviceResponse{
		Status:   statusSuccess,
		Message:  "Device registered successfully for push notifications",
		DeviceID: id,
	})
}

// UnregisterDevice godoc
// @Summary      Unregister a device from push notifications
// @Description  Removes the authenticated identity's registration of an FCM token, and only that one: other identities that registered the same token keep theirs. Idempotent: it succeeds whether or not the identity had registered the token.
// @Tags         Devices
// @Accept       json
// @Produce      json
// @Param        request body UnregisterDeviceRequest true "Token to unregister"
// @Success      200  {object}  UnregisterDeviceResponse
// @Failure      400  {object}  ErrorResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /unregisterDevice [post]
func (s *Server) UnregisterDevice(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_AUTHENTICATION_REQUIRED", "Authentication required.")
		return
	}
	s.unregisterDevice(w, r, identityKey)
}

// unregisterDevice is UnregisterDevice for an already-authenticated identity.
func (s *Server) unregisterDevice(w http.ResponseWriter, r *http.Request, identityKey string) {
	var req UnregisterDeviceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "ERR_INVALID_JSON", "Invalid JSON body")
		return
	}

	if req.FCMToken == "" {
		writeError(w, 400, "ERR_INVALID_FCM_TOKEN", "fcmToken is required and must be a non-empty string.")
		return
	}

	// The caller's own identity scopes the delete, never the body's: the token
	// is shared between a wallet's profiles, and one profile must not be able
	// to unregister another's.
	if err := s.Store.UnregisterDevice(r.Context(), identityKey, req.FCMToken); err != nil {
		logger.Error("failed to unregister device", "error", err)
		writeError(w, 500, "ERR_DATABASE_ERROR", "Failed to unregister device.")
		return
	}

	writeJSON(w, 200, UnregisterDeviceResponse{
		Status:  statusSuccess,
		Message: "Device unregistered from push notifications",
	})
}

// ListDevices godoc
// @Summary      List registered devices
// @Description  Returns all devices registered for push notifications for the authenticated identity.
// @Tags         Devices
// @Produce      json
// @Success      200  {object}  ListDevicesResponse
// @Failure      401  {object}  ErrorResponse
// @Failure      500  {object}  ErrorResponse
// @Security     BSVAuth
// @Router       /devices [get]
func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	identityKey := getIdentityKey(r)
	if identityKey == "" {
		writeError(w, 401, "ERR_AUTHENTICATION_REQUIRED", "Authentication required.")
		return
	}
	s.listDevices(w, r, identityKey)
}

// listDevices is ListDevices for an already-authenticated identity.
func (s *Server) listDevices(w http.ResponseWriter, r *http.Request, identityKey string) {
	devices, err := s.Store.ListDevices(r.Context(), identityKey)
	if err != nil {
		logger.Error("failed to list devices", "error", err)
		writeError(w, 500, "ERR_DATABASE_ERROR", "Failed to retrieve devices.")
		return
	}

	var out []DeviceOut
	for _, d := range devices {
		token := d.FCMToken
		if len(token) > 10 {
			token = "..." + token[len(token)-10:]
		}
		dev := DeviceOut{
			ID:        d.ID,
			FCMToken:  token,
			Active:    d.Active,
			CreatedAt: formatTime(d.CreatedAt),
			UpdatedAt: formatTime(d.UpdatedAt),
		}
		dev.DeviceID = d.DeviceID
		dev.Platform = d.Platform
		if d.LastUsed != nil {
			dev.LastUsed = formatTime(*d.LastUsed)
		}
		out = append(out, dev)
	}

	if out == nil {
		out = []DeviceOut{}
	}

	writeJSON(w, 200, ListDevicesResponse{
		Status:  statusSuccess,
		Devices: out,
	})
}
