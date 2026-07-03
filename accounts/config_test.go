package accounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchCLIConfig_Decodes verifies FetchCLIConfig decodes issuer/client_id/scopes
// from {accounts}/cli-config.
func TestFetchCLIConfig_Decodes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":    "https://issuer.example",
			"client_id": "cli-1",
			"scopes":    []string{"openid", "email"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg, err := FetchCLIConfig(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatalf("FetchCLIConfig: %v", err)
	}
	if cfg.Issuer != "https://issuer.example" || cfg.ClientID != "cli-1" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Scopes) != 2 || cfg.Scopes[0] != "openid" || cfg.Scopes[1] != "email" {
		t.Fatalf("scopes = %+v", cfg.Scopes)
	}
}

// TestFetchCLIConfig_MissingIssuer verifies a missing issuer is rejected.
func TestFetchCLIConfig_MissingIssuer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id": "cli-1",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := FetchCLIConfig(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("expected error for missing issuer")
	}
}

// TestFetchCLIConfig_MissingClientID verifies a missing client_id is rejected.
func TestFetchCLIConfig_MissingClientID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": "https://issuer.example",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := FetchCLIConfig(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("expected error for missing client_id")
	}
}

// TestFetchCLIConfig_NonSuccess verifies a non-2xx status is surfaced naming the status.
func TestFetchCLIConfig_NonSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/cli-config", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := FetchCLIConfig(context.Background(), nil, srv.URL)
	if err == nil {
		t.Fatal("expected error for non-2xx status")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want it to name the status", err)
	}
}

// TestFetchOIDC_Decodes verifies FetchOIDC decodes both endpoints.
func TestFetchOIDC_Decodes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": base + "/hostedui/authorize",
			"token_endpoint":         base + "/hostedui/token",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oidc, err := FetchOIDC(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatalf("FetchOIDC: %v", err)
	}
	if oidc.AuthorizationEndpoint != srv.URL+"/hostedui/authorize" {
		t.Fatalf("AuthorizationEndpoint = %q", oidc.AuthorizationEndpoint)
	}
	if oidc.TokenEndpoint != srv.URL+"/hostedui/token" {
		t.Fatalf("TokenEndpoint = %q", oidc.TokenEndpoint)
	}
}

// TestFetchOIDC_MissingAuthorizationEndpoint verifies a missing authorization_endpoint
// is rejected.
func TestFetchOIDC_MissingAuthorizationEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token_endpoint": base + "/hostedui/token",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := FetchOIDC(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("expected error for missing authorization_endpoint")
	}
}

// TestFetchOIDC_MissingTokenEndpoint verifies a missing token_endpoint is rejected.
func TestFetchOIDC_MissingTokenEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": base + "/hostedui/authorize",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := FetchOIDC(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("expected error for missing token_endpoint")
	}
}
