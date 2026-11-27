// Package signedsend is the one way this module sends a request that carries
// an SHN signed header: a holder's or an admin's assertion. It follows no
// redirect, whatever client it is given, so the signature goes only to the URL
// the request was built for. A redirect would re-send the header to the host
// it names, and that host could spend the assertion once, before it expires,
// on any route that accepts it.
//
// Every other file in the module sets a signed header only through Do; a
// fence test (sdk/signedsend_fence_test.go) fails the build otherwise.
package signedsend

import "net/http"

// HolderAssertion is the header that carries an SHN assertion: a holder's own,
// or a Trust admin's.
const HolderAssertion = "X-Holder-Assertion"

// signed is the closed set of headers that carry an SHN signature, by their
// canonical names.
var signed = map[string]bool{HolderAssertion: true}

// IsSigned reports whether name, in any letter case, is a header that
// carries an SHN signature.
func IsSigned(name string) bool { return signed[http.CanonicalHeaderKey(name)] }

// Do sets each of headers on req and sends req through a copy of c
// (http.DefaultClient when nil) that follows no redirect: a 3xx comes back as
// the answer, for the caller to read as a failure or as unknown. c itself is
// not changed.
func Do(c *http.Client, req *http.Request, headers map[string]string) (*http.Response, error) {
	if c == nil {
		c = http.DefaultClient
	}
	noRedirect := *c
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return noRedirect.Do(req)
}
