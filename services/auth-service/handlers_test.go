package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	app := &App{}
	req, err := http.NewRequest("GET", "/health", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := telemetryMiddleware("auth-service", http.HandlerFunc(app.healthHandler))
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}
}

func TestChaosAndValidateHandler(t *testing.T) {
	app := &App{}

	// Ativa Chaos Mode
	reqChaos, _ := http.NewRequest("POST", "/chaos?enable=true", nil)
	rrChaos := httptest.NewRecorder()
	app.chaosHandler(rrChaos, reqChaos)
	if !app.ChaosMode.Load() {
		t.Errorf("expected ChaosMode to be true")
	}

	// Quando Chaos Mode está ativo, /validate deve retornar 500
	reqVal, _ := http.NewRequest("GET", "/validate", nil)
	rrVal := httptest.NewRecorder()
	handler := telemetryMiddleware("auth-service", http.HandlerFunc(app.validateKeyHandler))
	handler.ServeHTTP(rrVal, reqVal)

	if rrVal.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 when chaos mode is active, got %d", rrVal.Code)
	}
}

func TestHashAPIKey(t *testing.T) {
	key := "test-api-key-value"
	hash1 := hashAPIKey(key)
	hash2 := hashAPIKey(key)

	if hash1 != hash2 {
		t.Errorf("hashAPIKey is not deterministic: %s != %s", hash1, hash2)
	}

	if len(hash1) != 64 {
		t.Errorf("expected SHA256 hex string of 64 chars, got length %d", len(hash1))
	}
}
