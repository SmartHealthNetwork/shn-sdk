package accounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// TestClient_CreateReturnsAssignedID verifies Create POSTs the expected body to
// /clients (with the Bearer token) and decodes the server-assigned id.
func TestClient_CreateReturnsAssignedID(t *testing.T) {
	var got map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "client-42"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	id, err := NewClient(srv.URL, "tok-1").Create(context.Background(), "Kit", "provider", "ENC", "SIGN", "https://x.example")
	if err != nil || id != "client-42" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	if got["name"] != "Kit" || got["role"] != "provider" || got["encPub"] != "ENC" || got["signPub"] != "SIGN" || got["baseURL"] != "https://x.example" {
		t.Fatalf("create body = %+v", got)
	}
}

// TestClient_CreateMissingID verifies an empty id in the response is an error.
func TestClient_CreateMissingID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := NewClient(srv.URL, "tok-1").Create(context.Background(), "Kit", "provider", "ENC", "SIGN", "https://x.example"); err == nil {
		t.Fatal("expected error for missing id")
	}
}

// TestClient_SubmitPoP verifies SubmitPoP posts the RegistrationRequest fields to
// /clients/{id}/pop.
func TestClient_SubmitPoP(t *testing.T) {
	var got map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients/client-42/pop", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	reg := shnsdk.RegistrationRequest{
		ID:      "client-42",
		Role:    "provider",
		EncPub:  "ENC",
		SignPub: "SIGN",
		BaseURL: "https://x.example",
		Pop:     "POP",
	}
	if err := NewClient(srv.URL, "tok-1").SubmitPoP(context.Background(), "client-42", reg); err != nil {
		t.Fatalf("SubmitPoP: %v", err)
	}
	want := map[string]string{
		"pop":     "POP",
		"encPub":  "ENC",
		"signPub": "SIGN",
		"baseURL": "https://x.example",
		"role":    "provider",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("pop body[%q] = %q, want %q (full: %+v)", k, got[k], v, got)
		}
	}
}

// TestClient_SubmitPoP_Error verifies a 4xx/5xx response is surfaced as an error
// containing the status and the server body text.
func TestClient_SubmitPoP_Error(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients/client-42/pop", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad pop", http.StatusBadRequest)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	err := NewClient(srv.URL, "tok-1").SubmitPoP(context.Background(), "client-42", shnsdk.RegistrationRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "bad pop") {
		t.Fatalf("error = %v, want it to contain status and body", err)
	}
}

// TestClient_ListDecodesRows verifies List decodes the GET /clients rows.
func TestClient_ListDecodesRows(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /clients", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode([]ClientRow{
			{ID: "c1", Name: "Kit", Role: "provider", Status: "active", CreatedAt: "2026-01-01", SignPubFp: "sfp", EncPubFp: "efp"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	rows, err := NewClient(srv.URL, "tok-1").List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "c1" || rows[0].Name != "Kit" {
		t.Fatalf("rows = %+v", rows)
	}
}

// TestClient_ListError verifies a non-2xx response is surfaced as an error
// containing the status and body.
func TestClient_ListError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /clients", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server exploded", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, err := NewClient(srv.URL, "tok-1").List(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "server exploded") {
		t.Fatalf("error = %v, want it to contain status and body", err)
	}
}

// TestClient_Revoke verifies Revoke POSTs /clients/{id}/revoke.
func TestClient_Revoke(t *testing.T) {
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients/client-42/revoke", func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if err := NewClient(srv.URL, "tok-1").Revoke(context.Background(), "client-42"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !called {
		t.Fatal("revoke endpoint was not called")
	}
}

// TestClient_RevokeError verifies a non-2xx response is surfaced as an error
// containing the status and body.
func TestClient_RevokeError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clients/client-42/revoke", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	err := NewClient(srv.URL, "tok-1").Revoke(context.Background(), "client-42")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want it to contain status and body", err)
	}
}

// TestClient_WithHTTP verifies WithHTTP overrides the transport used by calls.
func TestClient_WithHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /clients", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ClientRow{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := NewClient(srv.URL, "tok-1").WithHTTP(srv.Client())
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
}
