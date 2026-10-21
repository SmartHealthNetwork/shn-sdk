package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// cmdScenarioSign implements `shn scenario-sign`: print the X-Holder-Assertion
// value that signs one /scenario request (METHOD TARGET [BODY]) as --label,
// for the gateway whose holder id --gateway-id names (else SCENARIO_GATEWAY,
// else --bundle's manifest), so any HTTP client can send it:
//
//	curl -X POST "$gw/scenario/uc01" -H "X-Holder-Assertion: $(shn scenario-sign ... POST /scenario/uc01 "$body")" --data-raw "$body"
//
// It prints the header value only, never the key. The body must be the bytes
// the client sends, and the assertion is valid for one minute and one use.
func cmdScenarioSign(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scenario-sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyFile := fs.String("key", "", "the caller's key file (scenario-key writes it); else "+scenarioKeyEnv+" or "+scenarioKeyFileEnv)
	label := fs.String("label", defaultScenarioLabel, "the caller's label, as the gateway's callers file names it")
	gatewayID := fs.String("gateway-id", "", "the holder id of the gateway the request is for; else "+scenarioGatewayEnv+", else --bundle's manifest")
	bundle := fs.String("bundle", "", "the gateway's bundle directory, whose manifest names its holder id")
	bodyFile := fs.String("body-file", "", "read the body from this file instead of the BODY argument")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 2 || len(rest) > 3 || (len(rest) == 3 && *bodyFile != "") {
		fmt.Fprintln(stderr, "usage: shn scenario-sign [flags] METHOD TARGET [BODY]   (TARGET is the path and query, e.g. /scenario/uc01)")
		return 2
	}
	method, target := rest[0], rest[1]
	if method != strings.ToUpper(method) {
		// The method is signed as the client sends it, and the routes take it
		// upper-case: a lower-case one would sign what no route answers.
		fmt.Fprintf(stderr, "shn scenario-sign: METHOD %q: give it upper-case, as the client sends it (e.g. POST)\n", method)
		return 2
	}
	var body []byte
	switch {
	case *bodyFile != "":
		b, err := os.ReadFile(*bodyFile)
		if err != nil {
			fmt.Fprintf(stderr, "shn scenario-sign: %v\n", err)
			return 1
		}
		body = b
	case len(rest) == 3:
		body = []byte(rest[2])
	}
	priv, err := scenarioKeyFrom(*keyFile, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "shn scenario-sign: %v\n", err)
		return 1
	}
	if priv == nil {
		fmt.Fprintf(stderr, "shn scenario-sign: no key: pass --key <file> (shn scenario-key writes one), or set %s or %s\n", scenarioKeyEnv, scenarioKeyFileEnv)
		return 2
	}
	gateway, err := scenarioGatewayFrom(*gatewayID, *bundle, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "shn scenario-sign: %v\n", err)
		return 1
	}
	if gateway == "" {
		fmt.Fprintf(stderr, "shn scenario-sign: no gateway: pass --gateway-id <holder id> or --bundle <dir>, or set %s\n", scenarioGatewayEnv)
		return 2
	}
	h, err := scenarioAssertion(method, target, body, *label, gateway, priv, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "shn scenario-sign: %v\n", err)
		return 2
	}
	fmt.Fprintln(stdout, h)
	return 0
}
