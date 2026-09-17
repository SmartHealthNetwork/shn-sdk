package main

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	shnsdk "github.com/SmartHealthNetwork/shn-sdk"
)

// requestFramesUsage is the --request-frames help text shared by register
// and rotate.
var requestFramesUsage = "request-frame capabilities to declare, comma-separated (default: every capability this build supports, " +
	"currently " + strings.Join(shnsdk.SupportedRequestFrames(), ",") + "). Declare v1op only when the Smart Gateway serving " +
	"--base-url is v0.44.0 or newer; otherwise pass --request-frames v1, and rotate after you upgrade the gateway " +
	"(rotate issues new keys: restart the gateway with the new key directory)."

// requestFrameTokenRe is the registrar's frame-token rule.
var requestFrameTokenRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// requestFramesFlag is the --request-frames value: the requestFrames a
// registration declares. Unset means this build's full supported set.
type requestFramesFlag struct {
	frames []string
	set    bool
}

func (f *requestFramesFlag) String() string { return strings.Join(f.frames, ",") }

// Set parses a comma-separated token list. Every token must satisfy the
// registrar's token rule and be a capability this build supports; the list
// must be non-empty and free of duplicates.
func (f *requestFramesFlag) Set(v string) error {
	if f.set {
		return fmt.Errorf("--request-frames given more than once")
	}
	var out []string
	for _, tok := range strings.Split(v, ",") {
		tok = strings.TrimSpace(tok)
		switch {
		case !requestFrameTokenRe.MatchString(tok):
			return fmt.Errorf("request frame %q is not a valid token (lowercase letters and digits, 1-16 characters)", tok)
		case !slices.Contains(shnsdk.SupportedRequestFrames(), tok):
			return fmt.Errorf("request frame %q is not supported by this build (supported: %s)", tok, strings.Join(shnsdk.SupportedRequestFrames(), ","))
		case slices.Contains(out, tok):
			return fmt.Errorf("request frame %q listed twice", tok)
		}
		out = append(out, tok)
	}
	f.frames, f.set = out, true
	return nil
}

// warn tells the user, without refusing, when the list declares framed DTR
// operations but not request frames for the other transaction types:
// requesters then frame only DTR operations to this holder.
func (f *requestFramesFlag) warn(w io.Writer, cmd string) {
	if f.set && slices.Contains(f.frames, shnsdk.RequestFrameV1Op) && !slices.Contains(f.frames, shnsdk.RequestFrameV1) {
		fmt.Fprintf(w, "%s: warning: --request-frames lists %s without %s; requesters will frame only DTR operations to you and send your other requests bare (pass --request-frames %s,%s to accept both)\n",
			cmd, shnsdk.RequestFrameV1Op, shnsdk.RequestFrameV1, shnsdk.RequestFrameV1, shnsdk.RequestFrameV1Op)
	}
}

// resolve returns the frames to declare: the flag's list, or this build's
// full supported set when the flag was not given.
func (f *requestFramesFlag) resolve() []string {
	if !f.set {
		return shnsdk.SupportedRequestFrames()
	}
	return append([]string(nil), f.frames...)
}
