package shnsdk

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Manifest is the public (no private keys) metadata `shn register`/`shn keygen` writes
// to manifest.json. One canonical shape — producer (shn CLI) and consumers (the gateway)
// share it so the on-disk format cannot skew.
type Manifest struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	EncPub  string `json:"encPub"`
	SignPub string `json:"signPub"`
	BaseURL string `json:"baseURL"`
}

// Bundle is a parsed registration bundle: the public Manifest plus the recovered
// private Identity (from sign.key/enc.key).
type Bundle struct {
	Manifest Manifest
	Identity Identity
}

const (
	bundleSignKeyFile  = "sign.key"
	bundleEncKeyFile   = "enc.key"
	bundleManifestFile = "manifest.json"
)

// WriteBundle writes the private keys (0600) + the public manifest.json to dir.
func WriteBundle(dir string, id Identity, role, baseURL string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, bundleSignKeyFile),
		[]byte(base64.StdEncoding.EncodeToString(id.SignPriv)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", bundleSignKeyFile, err)
	}
	if err := os.WriteFile(filepath.Join(dir, bundleEncKeyFile),
		[]byte(base64.StdEncoding.EncodeToString(id.EncPriv[:])), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", bundleEncKeyFile, err)
	}
	man := Manifest{
		ID:      id.HolderID,
		Role:    role,
		EncPub:  base64.StdEncoding.EncodeToString(id.EncPub[:]),
		SignPub: base64.StdEncoding.EncodeToString(id.SignPub),
		BaseURL: baseURL,
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, bundleManifestFile), append(mb, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", bundleManifestFile, err)
	}
	return nil
}

// LoadBundle parses manifest.json + sign.key + enc.key from dir back into a Bundle.
func LoadBundle(dir string) (Bundle, error) {
	var b Bundle
	manRaw, err := os.ReadFile(filepath.Join(dir, bundleManifestFile))
	if err != nil {
		return b, fmt.Errorf("read %s: %w", bundleManifestFile, err)
	}
	if err := json.Unmarshal(manRaw, &b.Manifest); err != nil {
		return b, fmt.Errorf("parse %s: %w", bundleManifestFile, err)
	}
	if b.Manifest.ID == "" {
		return b, fmt.Errorf("%s: missing \"id\" field", bundleManifestFile)
	}
	signB64, err := os.ReadFile(filepath.Join(dir, bundleSignKeyFile))
	if err != nil {
		return b, fmt.Errorf("read %s: %w", bundleSignKeyFile, err)
	}
	signPriv, err := base64.StdEncoding.DecodeString(string(signB64))
	if err != nil || len(signPriv) != ed25519.PrivateKeySize {
		return b, fmt.Errorf("%s: bad ed25519 private key (len=%d, err=%v)", bundleSignKeyFile, len(signPriv), err)
	}
	encB64, err := os.ReadFile(filepath.Join(dir, bundleEncKeyFile))
	if err != nil {
		return b, fmt.Errorf("read %s: %w", bundleEncKeyFile, err)
	}
	encRaw, err := base64.StdEncoding.DecodeString(string(encB64))
	if err != nil || len(encRaw) != 32 {
		return b, fmt.Errorf("%s: bad X25519 private key (len=%d, err=%v)", bundleEncKeyFile, len(encRaw), err)
	}
	var encPriv [32]byte
	copy(encPriv[:], encRaw)
	sp := ed25519.PrivateKey(signPriv)
	var encPub [32]byte
	if epRaw, e := base64.StdEncoding.DecodeString(b.Manifest.EncPub); e == nil && len(epRaw) == 32 {
		copy(encPub[:], epRaw)
	}
	b.Identity = Identity{
		HolderID: b.Manifest.ID,
		SignPriv: sp,
		SignPub:  sp.Public().(ed25519.PublicKey),
		EncPriv:  &encPriv,
		EncPub:   &encPub,
	}
	return b, nil
}
