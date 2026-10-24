package firebase

import (
	"sync"
	"testing"
)

func TestInitialize_EmptyProjectID(t *testing.T) {
	// Reset state for test
	resetState()

	cfg := Config{
		ProjectID:          "",
		ServiceAccountJSON: "some-json",
		ServiceAccountPath: "",
	}

	err := Initialize(cfg)
	if err != nil {
		t.Errorf("Initialize with empty ProjectID should return nil, got %v", err)
	}
	if IsEnabled() {
		t.Error("IsEnabled() should return false when ProjectID is empty")
	}
}

func TestInitialize_NoCredentials(t *testing.T) {
	// Reset state for test
	resetState()

	cfg := Config{
		ProjectID:          "test-project",
		ServiceAccountJSON: "",
		ServiceAccountPath: "",
	}

	err := Initialize(cfg)

	if err == nil {
		t.Error("Initialize without credentials should return an error")
	}
	if IsEnabled() {
		t.Error("IsEnabled() should return false when initialization fails")
	}
}

func TestInitialize_InvalidJSONCredentials(t *testing.T) {
	// Reset state for test
	resetState()

	cfg := Config{
		ProjectID:          "test-project",
		ServiceAccountJSON: "invalid-json",
		ServiceAccountPath: "",
	}

	err := Initialize(cfg)

	if err == nil {
		t.Error("Initialize with invalid JSON should return an error")
	}
	if IsEnabled() {
		t.Error("IsEnabled() should return false when initialization fails")
	}
}

func TestInitialize_InvalidFilePath(t *testing.T) {
	// Reset state for test
	resetState()

	cfg := Config{
		ProjectID:          "test-project",
		ServiceAccountJSON: "",
		ServiceAccountPath: "/nonexistent/path/to/credentials.json",
	}

	err := Initialize(cfg)

	if err == nil {
		t.Error("Initialize with invalid file path should return an error")
	}
	if IsEnabled() {
		t.Error("IsEnabled() should return false when initialization fails")
	}
}

func TestClient_ReturnsNilWhenNotInitialized(t *testing.T) {
	// Reset state for test
	resetState()

	c := Client()
	if c != nil {
		t.Error("Client() should return nil when not initialized")
	}
}

// fakeServiceAccountJSON is a credential-shaped placeholder; it holds no key.
const fakeServiceAccountJSON = `{"type": "service_account"}` //nolint:gosec // G101: placeholder, not a credential

func TestConfig_Fields(t *testing.T) {
	cfg := Config{
		ProjectID:          "my-project",
		ServiceAccountJSON: fakeServiceAccountJSON,
		ServiceAccountPath: "/path/to/creds.json",
	}

	if cfg.ProjectID != "my-project" {
		t.Errorf("ProjectID = %q, expected %q", cfg.ProjectID, "my-project")
	}
	if cfg.ServiceAccountJSON != fakeServiceAccountJSON {
		t.Errorf("ServiceAccountJSON not set correctly")
	}
	if cfg.ServiceAccountPath != "/path/to/creds.json" {
		t.Errorf("ServiceAccountPath = %q, expected %q", cfg.ServiceAccountPath, "/path/to/creds.json")
	}
}

// resetState resets the package-level state for testing
// This is needed because sync.Once only executes once
func resetState() {
	client = nil
	clientOnce = sync.Once{}
	errInit = nil
}
