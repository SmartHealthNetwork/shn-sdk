package shnsdk

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// noRoundTrip fails the test if anything is sent: a refusal made before a request.
type noRoundTrip struct{ t *testing.T }

func (n noRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("request sent to %s; want none for a blank member", r.URL)
	return nil, errors.New("no request expected")
}

// TestOriginatorsRefuseABlankMember: the SDK's originators derive no patient
// identifier from a blank member id and send nothing for it, where a blank
// member bound every leg's token to the identifier every blank member shares.
func TestOriginatorsRefuseABlankMember(t *testing.T) {
	c := &http.Client{Transport: noRoundTrip{t}}
	ep := Endpoints{HubURL: "http://hub.invalid", AuthzURL: "http://authz.invalid"}
	id := Identity{HolderID: "provider"}
	for _, m := range []string{"", " ", "\t"} {
		if _, _, err := id.RunEligibility(context.Background(), c, ep, Payer{ID: "payer"}, "9999999999", m, "1975-04-02", "Johansson"); !errors.Is(err, ErrBlankMember) {
			t.Errorf("RunEligibility(member %q): err %v; want ErrBlankMember", m, err)
		}
		req := demoPARequest()
		req.Member = m
		if _, err := id.RunPriorAuth(context.Background(), c, ep, Payer{ID: "payer"}, req); !errors.Is(err, ErrBlankMember) {
			t.Errorf("RunPriorAuth(member %q): err %v; want ErrBlankMember", m, err)
		}
	}
}
