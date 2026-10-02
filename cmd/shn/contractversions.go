package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// contractVersionsUsage is the --contract-versions help text shared by
// register and rotate.
var contractVersionsUsage = "contract versions to declare, comma-separated, exactly as the Smart Gateway at this holder's base URL " +
	"declares them (its SHN_CONTRACT_VERSIONS, or the set its boot line prints). register default: this build's set, currently " +
	strings.Join(shnsdk.SupportedContractVersions(), ",") + ". rotate default: the set the registrar holds for this holder now."

// contractVersionsFlag is the --contract-versions value: the contract
// versions a registration or rotation declares. It is parsed by
// shnsdk.ParseDeclaredContractVersions, the parser the gateway reads its own
// SHN_CONTRACT_VERSIONS with, so the CLI accepts exactly what the gateway does.
type contractVersionsFlag struct {
	versions []string
	set      bool
}

func (f *contractVersionsFlag) String() string { return strings.Join(f.versions, ",") }

// Set parses a comma-separated token list. A list with no token is refused
// (blank, commas, or any whitespace, a carriage return included): the shared
// parser reads it as the build default, which a flag given on purpose must not
// silently become. A token listed twice is refused, as --request-frames does.
func (f *contractVersionsFlag) Set(v string) error {
	if f.set {
		return fmt.Errorf("--contract-versions given more than once")
	}
	if len(strings.FieldsFunc(v, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })) == 0 {
		return fmt.Errorf("--contract-versions needs at least one token (e.g. %s)", shnsdk.SupportedContractVersions()[0])
	}
	versions, err := shnsdk.ParseDeclaredContractVersions(v)
	if err != nil {
		if strings.Contains(err.Error(), "not natively buildable") {
			return fmt.Errorf("%w (if the gateway declares it, use a newer shn)", err)
		}
		return err
	}
	for i, tok := range versions {
		if slices.Contains(versions[:i], tok) {
			return fmt.Errorf("contract version %q listed twice", tok)
		}
	}
	f.versions, f.set = versions, true
	return nil
}

// forRegister returns the set a registration declares and where it came from:
// the flag's, or this build's default.
func (f *contractVersionsFlag) forRegister() ([]string, string) {
	if f.set {
		return append([]string(nil), f.versions...), "from --contract-versions"
	}
	return shnsdk.SupportedContractVersions(), "this build's default; pass --contract-versions to declare your gateway's set"
}

// forRotate returns the set a rotation declares and where it came from: the
// flag's, or the set the registrar holds for the holder now, read from its
// public /holders feed. A rotation re-declares the holder's contract versions,
// so without the flag it re-sends what the network holds rather than reset it
// to this build's default; when that cannot be read it refuses rather than
// guess.
func (f *contractVersionsFlag) forRotate(ctx context.Context, c *http.Client, registrar, id string) ([]string, string, error) {
	if f.set {
		return append([]string(nil), f.versions...), "from --contract-versions", nil
	}
	ctx, cancel := context.WithTimeout(ctx, feedReadTimeout)
	defer cancel()
	hs, err := shnsdk.FetchHolders(ctx, c, registrar)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read the contract versions the registrar holds for %s (GET /holders: %v); pass --contract-versions to say which to declare", id, err)
	}
	for _, h := range hs {
		if h.ID == id {
			// A holder that declares none stays silent: a non-nil empty set
			// omits the key, where nil would declare this build's default.
			if len(h.ContractVersions) == 0 {
				return []string{}, "the registrar's entry for " + id, nil
			}
			return append([]string{}, h.ContractVersions...), "the set the registrar holds for " + id, nil
		}
	}
	return nil, "", fmt.Errorf("cannot read the contract versions the registrar holds for %s (it is not on the registrar's /holders feed); pass --contract-versions to say which to declare", id)
}

// feedReadTimeout bounds rotate's read of the held set, so a registrar that
// does not answer ends the rotation (refused, nothing sent) instead of hanging it.
var feedReadTimeout = 30 * time.Second

// printContractVersions states the set a command sends, and where it came from.
func printContractVersions(w io.Writer, cmd string, versions []string, source string) {
	if len(versions) == 0 {
		fmt.Fprintf(w, "%s: declaring no contract versions (%s declares none)\n", cmd, source)
		return
	}
	fmt.Fprintf(w, "%s: declaring contract versions %s (%s)\n", cmd, strings.Join(versions, ","), source)
}
