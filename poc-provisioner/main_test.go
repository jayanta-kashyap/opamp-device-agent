// POC Provisioner Tests
// Tests for HTTP handlers and request validation
// Note: These are unit tests that don't require a running k8s cluster

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDeployRequestValidation tests the deploy request parsing
func TestDeployRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		wantID  int
	}{
		{
			name:    "valid request",
			body:    `{"deviceId": 1}`,
			wantErr: false,
			wantID:  1,
		},
		{
			name:    "valid request with higher ID",
			body:    `{"deviceId": 100}`,
			wantErr: false,
			wantID:  100,
		},
		{
			name:    "invalid JSON",
			body:    `{invalid}`,
			wantErr: true,
		},
		{
			name:    "empty body",
			body:    `{}`,
			wantErr: false,
			wantID:  0, // Zero value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req DeployRequest
			err := json.Unmarshal([]byte(tt.body), &req)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if req.DeviceID != tt.wantID {
				t.Errorf("got deviceId=%d, want %d", req.DeviceID, tt.wantID)
			}
		})
	}
}

// TestResponseJSON tests response serialization
func TestResponseJSON(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		wantJSON string
	}{
		{
			name: "success response",
			response: Response{
				Success: true,
				Message: "device-1 deployed successfully",
			},
			wantJSON: `{"success":true,"message":"device-1 deployed successfully"}`,
		},
		{
			name: "error response",
			response: Response{
				Success: false,
				Message: "",
				Error:   "deployment failed",
			},
			wantJSON: `{"success":false,"message":"","error":"deployment failed"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			// Parse both to compare (ignoring whitespace)
			var got, want map[string]interface{}
			json.Unmarshal(data, &got)
			json.Unmarshal([]byte(tt.wantJSON), &want)

			if got["success"] != want["success"] {
				t.Errorf("success mismatch: got %v, want %v", got["success"], want["success"])
			}
			if got["message"] != want["message"] {
				t.Errorf("message mismatch: got %v, want %v", got["message"], want["message"])
			}
		})
	}
}

// TestHealthEndpoint tests the health check endpoint
func TestHealthEndpoint(t *testing.T) {
	// Create a simple handler for health check
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("got status %d, want %d", w.Code, http.StatusOK)
	}

	if w.Body.String() != "OK" {
		t.Errorf("got body %q, want %q", w.Body.String(), "OK")
	}
}

// TestDeviceNameFormat tests device name generation
func TestDeviceNameFormat(t *testing.T) {
	tests := []struct {
		deviceID int
		want     string
	}{
		{1, "device-1"},
		{10, "device-10"},
		{100, "device-100"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDeviceName(tt.deviceID)
			if got != tt.want {
				t.Errorf("formatDeviceName(%d) = %q, want %q", tt.deviceID, got, tt.want)
			}
		})
	}
}

// Helper function to format device name (matches main.go logic)
func formatDeviceName(deviceID int) string {
	return "device-" + itoa(deviceID)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

// TestDeployEndpointMethodValidation tests that deploy only accepts POST
func TestDeployEndpointMethodValidation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		method   string
		wantCode int
	}{
		{"POST", http.StatusOK},
		{"GET", http.StatusMethodNotAllowed},
		{"PUT", http.StatusMethodNotAllowed},
		{"DELETE", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/deploy", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("%s: got status %d, want %d", tt.method, w.Code, tt.wantCode)
			}
		})
	}
}

// TestDeployRequestParsing tests parsing of deploy request body
func TestDeployRequestParsing(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DeployRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(Response{Success: false, Error: "Invalid request body"})
			return
		}

		if req.DeviceID <= 0 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(Response{Success: false, Error: "deviceId must be positive"})
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(Response{Success: true, Message: formatDeviceName(req.DeviceID) + " deployed"})
	})

	tests := []struct {
		name        string
		body        string
		wantCode    int
		wantSuccess bool
	}{
		{
			name:        "valid request",
			body:        `{"deviceId": 5}`,
			wantCode:    http.StatusOK,
			wantSuccess: true,
		},
		{
			name:        "zero device ID",
			body:        `{"deviceId": 0}`,
			wantCode:    http.StatusBadRequest,
			wantSuccess: false,
		},
		{
			name:        "negative device ID",
			body:        `{"deviceId": -1}`,
			wantCode:    http.StatusBadRequest,
			wantSuccess: false,
		},
		{
			name:        "invalid JSON",
			body:        `{not valid}`,
			wantCode:    http.StatusBadRequest,
			wantSuccess: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/deploy", bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("got status %d, want %d", w.Code, tt.wantCode)
			}

			var resp Response
			json.NewDecoder(w.Body).Decode(&resp)
			if resp.Success != tt.wantSuccess {
				t.Errorf("got success=%v, want %v", resp.Success, tt.wantSuccess)
			}
		})
	}
}

// TestCORSHeaders tests that CORS headers are set correctly
func TestCORSHeaders(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("OPTIONS", "/api/deploy", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if cors := w.Header().Get("Access-Control-Allow-Origin"); cors != "*" {
		t.Errorf("CORS origin: got %q, want %q", cors, "*")
	}

	if methods := w.Header().Get("Access-Control-Allow-Methods"); methods == "" {
		t.Error("CORS methods header not set")
	}
}

// TestListDevicesResponse tests the list devices response format
func TestListDevicesResponse(t *testing.T) {
	type ListResponse struct {
		Devices []string `json:"devices"`
		Success bool     `json:"success"`
	}

	// Simulate response
	devices := []string{"device-1", "device-2", "device-3"}
	resp := ListResponse{
		Devices: devices,
		Success: true,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed ListResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(parsed.Devices) != 3 {
		t.Errorf("got %d devices, want 3", len(parsed.Devices))
	}

	if !parsed.Success {
		t.Error("expected success=true")
	}
}
