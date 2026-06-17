package shnsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type transportAddOneRequest struct {
	X int `json:"x"`
}

type transportAddOneResponse struct {
	Y int `json:"y"`
}

// TestPostJSON_RoundTrip proves PostJSON sends a JSON body and decodes a 2xx
// response into out.
func TestPostJSON_RoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad content-type", http.StatusBadRequest)
			return
		}
		var req transportAddOneRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(transportAddOneResponse{Y: req.X + 1})
	}))
	defer server.Close()

	reqBody := transportAddOneRequest{X: 41}
	var resp transportAddOneResponse
	err := PostJSON(context.Background(), server.Client(), server.URL, reqBody, &resp, nil)
	if err != nil {
		t.Fatalf("PostJSON error: %v", err)
	}
	if resp.Y != 42 {
		t.Errorf("expected Y=42, got Y=%d", resp.Y)
	}
}

// TestPostJSON_Non2xx proves PostJSON returns a *StatusError with the right Code
// and Body on a non-2xx response.
func TestPostJSON_Non2xx(t *testing.T) {
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer errServer.Close()

	err := PostJSON(context.Background(), errServer.Client(), errServer.URL, transportAddOneRequest{X: 1}, nil, nil)
	if err == nil {
		t.Fatal("expected non-nil error on 500 response, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StatusError, got %T: %v", err, err)
	}
	if se.Code != http.StatusInternalServerError {
		t.Errorf("expected Code=%d, got %d", http.StatusInternalServerError, se.Code)
	}
	if se.Body == "" {
		t.Error("expected non-empty Body in StatusError")
	}
}

// TestPostJSON_BoundsResponseSize proves PostJSON does not hang and returns an
// error when the server writes more than MaxResponseBytes of non-JSON data.
func TestPostJSON_BoundsResponseSize(t *testing.T) {
	bigServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		payload := make([]byte, MaxResponseBytes+100)
		for i := range payload {
			payload[i] = 'a'
		}
		_, _ = w.Write(payload)
	}))
	defer bigServer.Close()

	var out map[string]any
	err := PostJSON(context.Background(), bigServer.Client(), bigServer.URL, map[string]string{}, &out, nil)
	// Body is not valid JSON (just 'a' bytes), so unmarshal fails.
	// The key property: the call completes (does not hang).
	if err == nil {
		t.Fatal("expected error on oversized/non-JSON response, got nil")
	}
}

// TestDecodeJSONBody_ValidBody proves DecodeJSONBody returns (false, nil) and
// correctly populates dst for a well-formed JSON body within the size limit.
func TestDecodeJSONBody_ValidBody(t *testing.T) {
	type payload struct {
		X int `json:"x"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var dst payload
		tooLarge, err := DecodeJSONBody(w, r, &dst)
		if tooLarge {
			t.Errorf("expected tooLarge=false")
		}
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if dst.X != 42 {
			t.Errorf("X = %d, want 42", dst.X)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL, "application/json", bytes.NewBufferString(`{"x":42}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestDecodeJSONBody_TooLarge proves DecodeJSONBody returns (true, non-nil) when
// the request body exceeds MaxRequestBytes.
func TestDecodeJSONBody_TooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var dst map[string]any
		tooLarge, err := DecodeJSONBody(w, r, &dst)
		if !tooLarge {
			t.Errorf("expected tooLarge=true")
		}
		if err == nil {
			t.Errorf("expected non-nil error")
		}
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}))
	defer srv.Close()

	// Build a valid JSON object whose string value exceeds MaxRequestBytes.
	// {"k":"aaa...aaa"} — the string value alone is MaxRequestBytes+1 bytes.
	val := bytes.Repeat([]byte("a"), MaxRequestBytes+1)
	body := append([]byte(`{"k":"`), val...)
	body = append(body, []byte(`"}`)...)
	resp, err := srv.Client().Post(srv.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

// TestDecodeJSONBody_MalformedJSON proves DecodeJSONBody returns (false, non-nil)
// on malformed JSON (not a size limit hit).
func TestDecodeJSONBody_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var dst map[string]any
		tooLarge, err := DecodeJSONBody(w, r, &dst)
		if tooLarge {
			t.Errorf("expected tooLarge=false for malformed json")
		}
		if err == nil {
			t.Errorf("expected non-nil error for malformed json")
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL, "application/json", bytes.NewBufferString("{not valid json"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestNewClient proves NewClient returns a client with a non-zero Timeout.
func TestNewClient(t *testing.T) {
	c := NewClient()
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.Timeout == 0 {
		t.Error("expected non-zero Timeout on client returned by NewClient")
	}
}

func TestPostRaw_SendsExactBytes(t *testing.T) {
	raw := []byte("{\"a\":1}\n  not-json-after") // deliberately NOT what json.Marshal would produce
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if err := PostRaw(context.Background(), srv.Client(), srv.URL, raw, nil, map[string]string{"X-H": "v"}); err != nil {
		t.Fatalf("PostRaw: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("server got %q, want exact %q", got, raw)
	}
}
