package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/bsv-blockchain/go-message-box-server/internal/logger"
	"github.com/bsv-blockchain/go-message-box-server/pkg/storage"
)

// RegisterDevice godoc
// @Summary      Register a device for push notifications
// @Description  Registers a device with an FCM token for receiving push notifications. Supports iOS, Android, and web platforms.
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
	if err := s.Store.RegisterDevice(r.Context(), newDevice); err != nil {
		logger.Error("failed to register device", "error", err)
		writeError(w, 500, "ERR_DATABASE_ERROR", "Failed to register device.")
		return
	}

	writeJSON(w, 200, RegisterDeviceResponse{
		Status:  "success",
		Message: "Device registered successfully for push notifications",
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
			FCMToken:  token,
			Active:    d.Active,
			CreatedAt: d.CreatedAt.Format("2006-01-02T15:04:05.000Z"),
			UpdatedAt: d.UpdatedAt.Format("2006-01-02T15:04:05.000Z"),
		}
		dev.DeviceID = d.DeviceID
		dev.Platform = d.Platform
		if d.LastUsed != nil {
			dev.LastUsed = d.LastUsed.Format("2006-01-02T15:04:05.000Z")
		}
		out = append(out, dev)
	}

	if out == nil {
		out = []DeviceOut{}
	}

	writeJSON(w, 200, ListDevicesResponse{
		Status:  "success",
		Devices: out,
	})
}
