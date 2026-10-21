package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// scenarioLabel is a caller label scenario-key accepts: it names the key's
// file, so it is a short plain name.
var scenarioLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// defaultScenarioLabel is the label scenario-key, send-test and scenario-sign
// use when none is given.
const defaultScenarioLabel = "send-test"

// scenarioCallersFileName is the callers file scenario-key keeps in a bundle
// directory, the file a gateway's SCENARIO_CALLERS_FILE names.
const scenarioCallersFileName = "scenario-callers.json"

// scenarioCallerEntry is one entry of a gateway's callers file: a caller's
// label and the base64 of its ed25519 public key.
type scenarioCallerEntry struct {
	Label     string `json:"label"`
	PublicKey string `json:"public_key"`
}

// cmdScenarioKey implements `shn scenario-key`: generate a /scenario caller
// key for --label, write it (0600) to --out, and add the caller to the
// gateway's callers file, so the gateway admits the caller's signed requests
// once it restarts. It never prints the private key.
func cmdScenarioKey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scenario-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	label := fs.String("label", defaultScenarioLabel, "the caller's label, which its requests are signed under")
	bundle := fs.String("bundle", "", "the gateway's bundle directory (its SHN_SECRETS): the caller is added to "+scenarioCallersFileName+" there")
	callersFile := fs.String("callers-file", "", "the gateway's callers file (its SCENARIO_CALLERS_FILE), when it is not "+scenarioCallersFileName+" in --bundle")
	out := fs.String("out", ".", "the directory the key file is written to; never the gateway's bundle")
	force := fs.Bool("force", false, "replace an existing key file for --label, and its entry in the callers file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !scenarioLabel.MatchString(*label) {
		fmt.Fprintf(stderr, "shn scenario-key: --label %q: use lower-case letters, digits, '.', '_' or '-'\n", *label)
		return 2
	}
	path := *callersFile
	if path == "" && *bundle != "" {
		path = filepath.Join(*bundle, scenarioCallersFileName)
	}
	keyPath := filepath.Join(*out, "scenario-caller-"+*label+".sign")
	if *bundle != "" {
		if st, err := os.Stat(*bundle); err != nil || !st.IsDir() {
			fmt.Fprintf(stderr, "shn scenario-key: --bundle %s is not a directory\n", *bundle)
			return 2
		}
	}
	if st, err := os.Stat(*out); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "shn scenario-key: --out %s is not a directory\n", *out)
		return 2
	}
	// The key is the caller's: never in the gateway's bundle, nor, with no
	// bundle named, in the directory its callers file is in, under any name.
	guard, what := *bundle, "the gateway's bundle"
	if guard == "" && path != "" {
		guard, what = filepath.Dir(path), "the callers file's directory"
	}
	if guard != "" {
		inside, err := within(*out, guard)
		if err != nil {
			fmt.Fprintf(stderr, "shn scenario-key: cannot check --out %s against %s (%s): %v\n", *out, what, guard, err)
			return 2
		}
		if inside {
			fmt.Fprintf(stderr, "shn scenario-key: --out %s is in %s (%s): the caller's key is the caller's, so write it elsewhere (the gateway reads only the callers file)\n", *out, what, guard)
			return 2
		}
	}

	// Read the callers file before anything is written, so a refusal changes
	// nothing.
	var entries []json.RawMessage
	if path != "" {
		var err error
		entries, err = readCallers(path)
		if err != nil {
			fmt.Fprintf(stderr, "shn scenario-key: %v\n", err)
			return 1
		}
		kept := entries[:0:0]
		for _, raw := range entries {
			var e scenarioCallerEntry
			_ = json.Unmarshal(raw, &e) // readCallers checked every entry
			if e.Label == *label {
				if !*force {
					fmt.Fprintf(stderr, "shn scenario-key: %s already names the caller %q; pass --force to replace its key and its entry\n", path, *label)
					return 1
				}
				continue
			}
			kept = append(kept, raw)
		}
		entries = kept
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(stderr, "shn scenario-key: generate the key: %v\n", err)
		return 1
	}
	if err := writeKeyFile(keyPath, priv, *force); err != nil {
		fmt.Fprintf(stderr, "shn scenario-key: %v\n", err)
		return 1
	}
	entry := scenarioCallerEntry{Label: *label, PublicKey: base64.StdEncoding.EncodeToString(pub)}
	fmt.Fprintf(stdout, "wrote the /scenario caller key for %s to %s (0600). It is a credential: keep it out of the gateway's bundle and out of version control.\n", *label, keyPath)
	if path == "" {
		line, _ := json.Marshal(entry)
		fmt.Fprintf(stdout, "add this entry to the gateway's callers file (SCENARIO_CALLERS_FILE), then restart the gateway:\n%s\n", line)
		return 0
	}
	raw, _ := json.Marshal(entry)
	entries = append(entries, raw)
	if err := writeCallers(path, entries); err != nil {
		fmt.Fprintf(stderr, "shn scenario-key: %v (the key file is written; add the caller to the callers file yourself)\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "added the caller %s to %s; restart the gateway to admit it.\n", *label, path)
	return 0
}

// within reports whether directory dir is base or inside it, with links
// resolved, so neither another spelling nor a link reaches base unseen. A
// path it cannot resolve is an error, which the caller refuses on.
func within(dir, base string) (bool, error) {
	d, err := filepath.EvalSymlinks(dir)
	if err == nil {
		d, err = filepath.Abs(d)
	}
	if err != nil {
		return false, err
	}
	b, err := filepath.EvalSymlinks(base)
	if err == nil {
		b, err = filepath.Abs(b)
	}
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(b, d)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

// readCallers reads a callers file's entries, each a {label, public_key} with
// a label, listed once, and a 32-byte base64 key, the file a gateway boots
// with; a missing file has none.
func readCallers(path string) ([]json.RawMessage, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s is not a JSON array of callers: %w", path, err)
	}
	seen := map[string]bool{}
	for i, r := range entries {
		var e scenarioCallerEntry
		if err := json.Unmarshal(r, &e); err != nil || e.Label == "" {
			return nil, fmt.Errorf("%s: entry %d is not a {label, public_key} caller", path, i)
		}
		if seen[e.Label] {
			return nil, fmt.Errorf("%s: the caller %q is listed twice, which the gateway refuses at boot", path, e.Label)
		}
		seen[e.Label] = true
		if k, err := base64.StdEncoding.DecodeString(e.PublicKey); err != nil || len(k) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s: entry %d (%s) has no ed25519 public key", path, i, e.Label)
		}
	}
	return entries, nil
}

// writeKeyFile writes priv's base64 to path, readable by its owner only. It
// refuses to replace an existing file, or a link, unless force is set; with
// force it writes a new file beside path and renames it over path, so a link
// there is replaced, never written through.
func writeKeyFile(path string, priv ed25519.PrivateKey, force bool) error {
	line := base64.StdEncoding.EncodeToString(priv) + "\n"
	if !force {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s exists; pass --force to replace it", path)
		}
		if err != nil {
			return err
		}
		return writeOwnerOnly(f, line)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := writeOwnerOnly(f, line); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// writeOwnerOnly writes line to f, readable by its owner only, and closes f.
func writeOwnerOnly(f *os.File, line string) error {
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// writeCallers replaces the callers file at path with entries, through a
// temporary file beside it, so a reader never sees half a file. A new file is
// readable by all (it holds public keys only); an existing one keeps its mode.
func writeCallers(path string, entries []json.RawMessage) error {
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
