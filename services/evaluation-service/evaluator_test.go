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
	handler := telemetryMiddleware("evaluation-service", http.HandlerFunc(app.healthHandler))
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}
}

func TestChaosAndEvaluateHandler(t *testing.T) {
	app := &App{}

	reqChaos, _ := http.NewRequest("POST", "/chaos?enable=true", nil)
	rrChaos := httptest.NewRecorder()
	app.chaosHandler(rrChaos, reqChaos)
	if !app.ChaosMode.Load() {
		t.Errorf("expected ChaosMode to be true")
	}

	reqEval, _ := http.NewRequest("GET", "/evaluate?user_id=u1&flag_name=f1", nil)
	rrEval := httptest.NewRecorder()
	handler := telemetryMiddleware("evaluation-service", http.HandlerFunc(app.evaluationHandler))
	handler.ServeHTTP(rrEval, reqEval)

	if rrEval.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 when chaos mode is active, got %d", rrEval.Code)
	}
}

func TestGetDeterministicBucket(t *testing.T) {
	bucket1 := getDeterministicBucket("user-123-flag-a")
	bucket2 := getDeterministicBucket("user-123-flag-a")

	if bucket1 != bucket2 {
		t.Errorf("getDeterministicBucket should be deterministic: %d != %d", bucket1, bucket2)
	}

	if bucket1 < 0 || bucket1 >= 100 {
		t.Errorf("bucket out of range [0, 99]: %d", bucket1)
	}
}
