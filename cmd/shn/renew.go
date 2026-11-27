package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// exitRenewalUnknown is shn renew's exit code when the registrar did not
// confirm the renewal: renew again later. A refusal exits 1, and renewing
// again does not change it.
const exitRenewalUnknown = 3

// renewTimeout bounds one renewal's request.
const renewTimeout = 30 * time.Second

// cmdRenew is `shn renew <id> --registrar URL -out <keys>`: a holder's own
// renewal of its registration against the registrar's PUT
// /register/{id}/lease, signed with the holder's current key. It reads the
// keys in -out and writes nothing. It exits 0 when the registrar renewed the
// registration; 1 when the registrar refused it, or when the keys in -out
// cannot be read and nothing was sent; and exitRenewalUnknown when the
// registrar did not confirm it (no answer, a redirect, a 404 or 405 from a
// registrar that predates the renewal, a 5xx, or an answer it cannot read).
// Each case says which on stderr.
func cmdRenew(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("renew", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registrar := fs.String("registrar", "", "registrar base URL (required)")
	out := fs.String("out", ".", "key directory holding the holder's current keys (read only)")
	id, rest := splitPositional(args)
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if id == "" {
		fmt.Fprintln(stderr, "shn renew: usage: shn renew <id> --registrar <url> -out <keys>")
		return 2
	}
	if *registrar == "" {
		fmt.Fprintln(stderr, "shn renew: --registrar is required")
		return 2
	}
	cur, err := loadIdentity(*out, id)
	if err != nil {
		fmt.Fprintf(stderr, "shn renew: load current identity: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), renewTimeout)
	defer cancel()
	got, err := cur.RenewLease(ctx, &http.Client{Timeout: renewTimeout}, *registrar)
	switch got.Outcome {
	case shnsdk.RenewalRenewed:
		if got.LeaseExpiresAt == nil {
			fmt.Fprintf(stdout, "renewed %s. No lease runs: nothing lapses.\n", id)
		} else {
			fmt.Fprintf(stdout, "renewed %s. Its lease lapses at %s unless it renews before then.\n", id, got.LeaseExpiresAt.UTC().Format(time.RFC3339))
		}
		return 0
	case shnsdk.RenewalRefused:
		fmt.Fprintf(stderr, "shn renew: %v\n", err)
		return 1
	default:
		fmt.Fprintf(stderr, "shn renew: %v\nshn renew: renew again later.\n", err)
		return exitRenewalUnknown
	}
}
