package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteThinQErrorExplainsQuotaExhaustion(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeThinQError(recorder, http.StatusTooManyRequests, "setup verification", &thinQAPIError{
		Status:  http.StatusUnauthorized,
		Code:    "1314",
		Message: "Exceeded User API calls",
	})

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", recorder.Code)
	}
	var response map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["error"] != "LG ThinQ API quota exceeded." {
		t.Fatalf("unexpected error: %#v", response["error"])
	}
	if response["provider_code"] != "1314" {
		t.Fatalf("unexpected provider code: %#v", response["provider_code"])
	}
	if response["remediation"] == "" {
		t.Fatal("expected quota remediation")
	}
}

func TestWriteThinQErrorExplainsCountryAndPATFailures(t *testing.T) {
	tests := []struct {
		name    string
		apiErr  *thinQAPIError
		wantMsg string
	}{
		{
			name:    "country",
			apiErr:  &thinQAPIError{Status: http.StatusUnauthorized, Code: "1307", Message: "Not supported country"},
			wantMsg: "LG ThinQ does not support the selected country.",
		},
		{
			name:    "pat",
			apiErr:  &thinQAPIError{Status: http.StatusUnauthorized, Message: "Invalid token"},
			wantMsg: "LG ThinQ rejected the PAT or application settings.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeThinQError(recorder, http.StatusUnauthorized, "PAT verification", test.apiErr)

			var response map[string]any
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response["error"] != test.wantMsg {
				t.Fatalf("unexpected error: %#v", response["error"])
			}
			if response["remediation"] == "" {
				t.Fatal("expected remediation")
			}
		})
	}
}
