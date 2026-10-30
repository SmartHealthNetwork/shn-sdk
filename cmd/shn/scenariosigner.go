package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// This file signs a provider gateway's /scenario requests. It is the
// documented wire format (this module's docs/PARTICIPANT_PROTOCOL.md, "/scenario
// caller assertions"), not a copy of the gateway's own package, which this module
// cannot import; it is drift-fenced against the gateway module's scenarioauth
// package by a test in the SHN platform repository, which builds this command
// and has scenarioauth's Verify check what it signs.

// The wire format of a /scenario caller assertion.
const (
	// scenarioHeader carries the assertion: base64 of its JSON.
	scenarioHeader = "X-Holder-Assertion"
	// scenarioAudiencePrefix starts the audience; the called gateway's holder
	// id follows it.
	scenarioAudiencePrefix = "scenario:"
	// scenarioTTL is how long an assertion is valid; each request mints its
	// own.
	scenarioTTL = time.Minute
	// scenarioPathPrefix is the path prefix of the routes it is for.
	scenarioPathPrefix = "/scenario/"
)

// The environment a /scenario caller reads, the same names the gateway's
// callers read: the key itself (base64), else the file that holds it, and the
// holder id of the gateway the requests are for.
const (
	scenarioKeyEnv     = "SCENARIO_CALLER_KEY"
	scenarioKeyFileEnv = "SCENARIO_CALLER_KEY_FILE"
	scenarioGatewayEnv = "SCENARIO_GATEWAY"
)

// scenarioBinding is the bytes an assertion's bh hashes: the method, a space,
// the request target (path and raw query, exactly as sent), a newline and the
// body.
func scenarioBinding(method, target string, body []byte) []byte {
	b := make([]byte, 0, len(method)+len(target)+2+len(body))
	b = append(b, method...)
	b = append(b, ' ')
	b = append(b, target...)
	b = append(b, '\n')
	return append(b, body...)
}

// scenarioAssertion is the header value that signs one request, as label, for
// the gateway whose holder id is gateway: method, target and body exactly as
// they will be sent.
func scenarioAssertion(method, target string, body []byte, label, gateway string, priv ed25519.PrivateKey, now time.Time) (string, error) {
	switch {
	case len(priv) != ed25519.PrivateKeySize:
		return "", errors.New("not an ed25519 private key")
	case label == "":
		return "", errors.New("no caller label")
	case gateway == "":
		return "", errors.New("no gateway holder id to sign for")
	case !strings.HasPrefix(target, scenarioPathPrefix):
		return "", fmt.Errorf("%q is not a %s target", target, scenarioPathPrefix)
	}
	a := shnsdk.IssueAssertionForBody(label, scenarioAudiencePrefix+gateway, priv, now, scenarioTTL, scenarioBinding(method, target, body))
	raw, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// signScenarioRequest stamps req with its assertion. It reads req's body and
// puts it back, so req can still be sent.
func signScenarioRequest(req *http.Request, label, gateway string, priv ed25519.PrivateKey, now time.Time) error {
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return err
		}
		body = b
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.ContentLength = int64(len(body))
	}
	h, err := scenarioAssertion(req.Method, req.URL.RequestURI(), body, label, gateway, priv, now)
	if err != nil {
		return err
	}
	req.Header.Set(scenarioHeader, h)
	return nil
}

// parseScenarioKey reads a caller key: a base64 ed25519 private key, the
// format scenario-key writes.
func parseScenarioKey(s string) (ed25519.PrivateKey, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(k) != ed25519.PrivateKeySize {
		return nil, errors.New("not a base64 ed25519 private key")
	}
	return ed25519.PrivateKey(k), nil
}

// loadScenarioKey reads the caller key in the file at path.
func loadScenarioKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	k, err := parseScenarioKey(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// scenarioKeyFrom is the caller key a command signs with: the file keyFile
// names, else SCENARIO_CALLER_KEY, else the file SCENARIO_CALLER_KEY_FILE
// names; nil and no error when none is given.
func scenarioKeyFrom(keyFile string, getenv func(string) string) (ed25519.PrivateKey, error) {
	if keyFile != "" {
		return loadScenarioKey(keyFile)
	}
	if v := getenv(scenarioKeyEnv); v != "" {
		k, err := parseScenarioKey(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", scenarioKeyEnv, err)
		}
		return k, nil
	}
	if p := getenv(scenarioKeyFileEnv); p != "" {
		return loadScenarioKey(p)
	}
	return nil, nil
}

// scenarioGatewayFrom is the holder id of the gateway a command signs for:
// gatewayID, else SCENARIO_GATEWAY, else the id in the bundle's manifest.json
// (a participant's own gateway runs as its own holder); "" when none is given.
func scenarioGatewayFrom(gatewayID, bundle string, getenv func(string) string) (string, error) {
	if id := strings.TrimSpace(gatewayID); id != "" {
		return id, nil
	}
	if id := strings.TrimSpace(getenv(scenarioGatewayEnv)); id != "" {
		return id, nil
	}
	if bundle == "" {
		return "", nil
	}
	man, err := readManifest(bundle)
	if err != nil {
		return "", fmt.Errorf("read the bundle's %s: %w", filepath.Join(bundle, manifestFile), err)
	}
	if strings.TrimSpace(man.ID) == "" {
		return "", fmt.Errorf("the bundle's %s names no holder id", filepath.Join(bundle, manifestFile))
	}
	return strings.TrimSpace(man.ID), nil
}
