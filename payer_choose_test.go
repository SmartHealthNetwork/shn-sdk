package shnsdk

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const (
	cmsPayerSystem = "urn:oid:2.16.840.1.113883.6.300"
	npiSystem      = "http://hl7.org/fhir/sid/us-npi"
	einSystem      = "http://terminology.hl7.org/NamingSystem/USEIN"
	epicPayerID    = "http://open.epic.com/FHIR/StructureDefinition/payer-id"
)

// orgCoverage is a Coverage whose payor is a contained Organization carrying ids in order.
func orgCoverage(ids ...PayerIdentifier) []byte {
	s := `{"resourceType":"Coverage","payor":[{"reference":"#p"}],"contained":[{"resourceType":"Organization","id":"p","identifier":[`
	for i, id := range ids {
		if i > 0 {
			s += ","
		}
		s += `{"system":"` + id.System + `","value":"` + id.Value + `"}`
	}
	return []byte(s + `]}]}`)
}

// registry resolves the identifiers it holds to their holder, as a payer directory does.
func registry(entries map[PayerIdentifier]string) func(PayerIdentifier) (string, bool) {
	return func(p PayerIdentifier) (string, bool) {
		h, ok := entries[p]
		return h, ok
	}
}

var (
	npi      = PayerIdentifier{npiSystem, "1234567893"}
	ein      = PayerIdentifier{einSystem, "7239994834"}
	payer301 = PayerIdentifier{cmsPayerSystem, "00301"}
	payer300 = PayerIdentifier{cmsPayerSystem, "00300"}
	epic301  = PayerIdentifier{epicPayerID, "PAYER-301"}
	dir      = registry(map[PayerIdentifier]string{payer301: "payer-22", epic301: "payer-22", payer300: "payer-20"})
)

func TestCoveragePayerCandidates(t *testing.T) {
	t.Run("every identifier of the payor Organization, in document order, duplicates once", func(t *testing.T) {
		got, err := CoveragePayerCandidates(orgCoverage(npi, ein, payer301, npi), nil)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{npi, ein, payer301}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("an inline payor identifier is the one candidate", func(t *testing.T) {
		got, err := CoveragePayerCandidates([]byte(`{"resourceType":"Coverage","payor":[{"identifier":{"system":"`+cmsPayerSystem+`","value":"00301"}}]}`), nil)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{payer301}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("an external Organization is read through resolveRef", func(t *testing.T) {
		cov := []byte(`{"resourceType":"Coverage","payor":[{"reference":"Organization/ext"}]}`)
		got, err := CoveragePayerCandidates(cov, func(ref string) ([]byte, bool) {
			return []byte(`{"resourceType":"Organization","id":"ext","identifier":[{"system":"` + npiSystem + `","value":"1234567893"},{"system":"` + cmsPayerSystem + `","value":"00301"}]}`), ref == "Organization/ext"
		})
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{npi, payer301}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("one list per Coverage of a Bundle", func(t *testing.T) {
		b := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(orgCoverage(npi, payer301)) + `},{"resource":` + string(orgCoverage(payer300)) + `}]}`)
		got, err := CoveragePayerCandidates(b, nil)
		if err != nil || len(got) != 2 || !slices.Equal(got[0], []PayerIdentifier{npi, payer301}) || !slices.Equal(got[1], []PayerIdentifier{payer300}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	for name, v := range map[string][]byte{
		"a payor that resolves to nothing":    []byte(`{"resourceType":"Coverage","payor":[{"reference":"Organization/none"}]}`),
		"a Bundle that holds no Coverage":     []byte(`{"resourceType":"Bundle","entry":[]}`),
		"an Organization with no identifiers": orgCoverage(),
	} {
		t.Run(name+" names no payer", func(t *testing.T) {
			if _, err := CoveragePayerCandidates(v, nil); !errors.Is(err, ErrNoCoveragePayer) {
				t.Fatalf("err %v, want ErrNoCoveragePayer", err)
			}
		})
	}
}

func TestChooseRegisteredPayer(t *testing.T) {
	choose := func(t *testing.T, cov []byte) (PayerIdentifier, string, error) {
		t.Helper()
		cands, err := CoveragePayerCandidates(cov, nil)
		if err != nil {
			t.Fatalf("candidates: %v", err)
		}
		return ChooseRegisteredPayer(cands, dir)
	}
	t.Run("the registered identifier routes wherever it sits", func(t *testing.T) {
		for _, order := range [][]PayerIdentifier{{payer301, npi, ein}, {npi, payer301, ein}, {npi, ein, payer301}} {
			pid, holder, err := choose(t, orgCoverage(order...))
			if err != nil || pid != payer301 || holder != "payer-22" {
				t.Fatalf("order %v: got %v %q %v, want %v payer-22", order, pid, holder, err, payer301)
			}
		}
	})
	t.Run("two identifiers the registry gives one holder route there, naming the first in document order", func(t *testing.T) {
		pid, holder, err := choose(t, orgCoverage(npi, epic301, payer301))
		if err != nil || pid != epic301 || holder != "payer-22" {
			t.Fatalf("got %v %q %v", pid, holder, err)
		}
	})
	t.Run("identifiers naming two holders are ambiguous", func(t *testing.T) {
		if _, _, err := choose(t, orgCoverage(payer301, payer300)); !errors.Is(err, ErrAmbiguousCoveragePayer) {
			t.Fatalf("err %v, want ErrAmbiguousCoveragePayer", err)
		}
	})
	t.Run("no registered identifier names every candidate", func(t *testing.T) {
		_, _, err := choose(t, orgCoverage(npi, ein))
		var nr *NoRegisteredPayerError
		if !errors.Is(err, ErrNoRegisteredPayer) || !errors.As(err, &nr) || !slices.Equal(nr.Candidates, []PayerIdentifier{npi, ein}) {
			t.Fatalf("err %v, want NoRegisteredPayerError naming %v", err, []PayerIdentifier{npi, ein})
		}
	})
	t.Run("a Bundle's Coverages must name one holder", func(t *testing.T) {
		same := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(orgCoverage(npi, payer301)) + `},{"resource":` + string(orgCoverage(epic301)) + `}]}`)
		if pid, holder, err := choose(t, same); err != nil || pid != payer301 || holder != "payer-22" {
			t.Fatalf("one holder: got %v %q %v", pid, holder, err)
		}
		two := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(orgCoverage(payer301)) + `},{"resource":` + string(orgCoverage(payer300)) + `}]}`)
		if _, _, err := choose(t, two); !errors.Is(err, ErrAmbiguousCoveragePayer) {
			t.Fatalf("two holders: err %v, want ErrAmbiguousCoveragePayer", err)
		}
		for name, order := range map[string][2][]PayerIdentifier{
			"registered first":   {{payer301}, {npi}},
			"unregistered first": {{npi}, {payer301}},
		} {
			mixed := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(orgCoverage(order[0]...)) + `},{"resource":` + string(orgCoverage(order[1]...)) + `}]}`)
			if _, _, err := choose(t, mixed); !errors.Is(err, ErrAmbiguousCoveragePayer) {
				t.Fatalf("a registered and an unregistered Coverage (%s): err %v, want ErrAmbiguousCoveragePayer", name, err)
			}
		}
		otherUnregistered := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(orgCoverage(npi)) + `},{"resource":` + string(orgCoverage(ein)) + `}]}`)
		if _, _, err := choose(t, otherUnregistered); !errors.Is(err, ErrAmbiguousCoveragePayer) {
			t.Fatalf("two Coverages naming different unregistered identifiers: err %v, want ErrAmbiguousCoveragePayer", err)
		}
		sameUnregistered := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(orgCoverage(npi, ein)) + `},{"resource":` + string(orgCoverage(ein, npi)) + `}]}`)
		var nr *NoRegisteredPayerError
		if _, _, err := choose(t, sameUnregistered); !errors.As(err, &nr) || !slices.Equal(nr.Candidates, []PayerIdentifier{npi, ein}) {
			t.Fatalf("Coverages naming the same unregistered identifiers: err %v, want NoRegisteredPayerError naming the first Coverage's", err)
		}
	})
	t.Run("a resolve answering ok with no holder is no registration", func(t *testing.T) {
		cands, _ := CoveragePayerCandidates(orgCoverage(npi), nil)
		if _, _, err := ChooseRegisteredPayer(cands, func(PayerIdentifier) (string, bool) { return "", true }); !errors.Is(err, ErrNoRegisteredPayer) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("no candidates names no payer", func(t *testing.T) {
		if _, _, err := ChooseRegisteredPayer(nil, dir); !errors.Is(err, ErrNoCoveragePayer) {
			t.Fatalf("err %v", err)
		}
		if _, _, err := ChooseRegisteredPayer([][]PayerIdentifier{{}}, dir); !errors.Is(err, ErrNoCoveragePayer) {
			t.Fatalf("empty list: err %v", err)
		}
	})
}

// ParseCoveragePayer keeps its documented, position-based answer: the payor
// Organization's first identifier. It is not a routing decision.
func TestParseCoveragePayerIsPositionBased(t *testing.T) {
	if got, err := ParseCoveragePayer(orgCoverage(npi, payer301), nil); err != nil || got != npi {
		t.Fatalf("got %v %v, want the first identifier %v", got, err, npi)
	}
}

func TestOrganizationIdentifiers(t *testing.T) {
	org := []byte(`{"resourceType":"Organization","identifier":[{"system":"` + npiSystem + `","value":"1234567893"},{"value":"no-system"},{"system":"` + cmsPayerSystem + `","value":"00301"},{"system":"` + npiSystem + `","value":"1234567893"}]}`)
	if got := OrganizationIdentifiers(org); !slices.Equal(got, []PayerIdentifier{npi, payer301}) {
		t.Fatalf("got %v", got)
	}
	if got := OrganizationIdentifiers([]byte(`{"resourceType":"Patient","identifier":[{"system":"a","value":"b"}]}`)); got != nil {
		t.Fatalf("a Patient: got %v, want nil", got)
	}
}

// ParseCoveragePayer still decides Coverage by Coverage and stops at the first
// problem: an ambiguity met before a Coverage whose payor does not resolve is
// reported as ambiguity, and the later payor is never looked up.
func TestParseCoveragePayerStopsAtTheFirstProblem(t *testing.T) {
	b := []byte(`{"resourceType":"Bundle","entry":[{"resource":` + string(orgCoverage(payer301)) + `},{"resource":` + string(orgCoverage(payer300)) + `},` +
		`{"resource":{"resourceType":"Coverage","id":"np","payor":[{"reference":"Organization/elsewhere"}]}}]}`)
	calls := 0
	_, err := ParseCoveragePayer(b, func(string) ([]byte, bool) { calls++; return nil, false })
	if !errors.Is(err, ErrAmbiguousCoveragePayer) || calls != 0 {
		t.Fatalf("err %v after %d lookups, want ErrAmbiguousCoveragePayer and none", err, calls)
	}
}

func TestCoveragePayerCandidatesPayorForms(t *testing.T) {
	t.Run("an inline identifier and a referenced Organization are both candidates, the inline one first", func(t *testing.T) {
		cov := []byte(`{"resourceType":"Coverage","payor":[{"reference":"#p","identifier":{"system":"` + npiSystem + `","value":"1234567893"}}],` +
			`"contained":[{"resourceType":"Organization","id":"p","identifier":[{"system":"` + npiSystem + `","value":"1234567893"},{"system":"` + cmsPayerSystem + `","value":"00301"}]}]}`)
		got, err := CoveragePayerCandidates(cov, nil)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{npi, payer301}) {
			t.Fatalf("got %v, %v", got, err)
		}
		if pid, holder, err := ChooseRegisteredPayer(got, dir); err != nil || pid != payer301 || holder != "payer-22" {
			t.Fatalf("choose: %v %q %v", pid, holder, err)
		}
		// The position-based answer is unchanged: the inline identifier.
		if pid, err := ParseCoveragePayer(cov, nil); err != nil || pid != npi {
			t.Fatalf("ParseCoveragePayer: %v %v, want the inline %v", pid, err, npi)
		}
	})
	t.Run("an external reference in a Bundle resolves among its entries, then through resolveRef", func(t *testing.T) {
		b := []byte(`{"resourceType":"Bundle","entry":[` +
			`{"resource":{"resourceType":"Coverage","payor":[{"reference":"Organization/in"}]}},` +
			`{"resource":{"resourceType":"Organization","id":"in","identifier":[{"system":"` + npiSystem + `","value":"1234567893"},{"system":"` + cmsPayerSystem + `","value":"00301"}]}},` +
			`{"resource":{"resourceType":"Coverage","payor":[{"reference":"Organization/out"}]}}]}`)
		got, err := CoveragePayerCandidates(b, func(ref string) ([]byte, bool) {
			return []byte(`{"resourceType":"Organization","id":"out","identifier":[{"system":"` + epicPayerID + `","value":"PAYER-301"}]}`), ref == "Organization/out"
		})
		if err != nil || len(got) != 2 || !slices.Equal(got[0], []PayerIdentifier{npi, payer301}) || !slices.Equal(got[1], []PayerIdentifier{epic301}) {
			t.Fatalf("got %v, %v", got, err)
		}
		if pid, holder, err := ChooseRegisteredPayer(got, dir); err != nil || pid != payer301 || holder != "payer-22" {
			t.Fatalf("choose: %v %q %v", pid, holder, err)
		}
	})
	t.Run("a Coverage with no candidates among others names no payer", func(t *testing.T) {
		if _, _, err := ChooseRegisteredPayer([][]PayerIdentifier{{payer301}, {}}, dir); !errors.Is(err, ErrNoCoveragePayer) {
			t.Fatalf("err %v", err)
		}
	})
}

// payorsCoverage is a Coverage whose payors are contained Organizations, one per
// identifier list, in order.
func payorsCoverage(orgs ...[]PayerIdentifier) []byte {
	payors, contained := "", ""
	for i, ids := range orgs {
		if i > 0 {
			payors += ","
			contained += ","
		}
		id := "p" + string(rune('0'+i))
		payors += `{"reference":"#` + id + `"}`
		contained += `{"resourceType":"Organization","id":"` + id + `","identifier":[`
		for j, x := range ids {
			if j > 0 {
				contained += ","
			}
			contained += `{"system":"` + x.System + `","value":"` + x.Value + `"}`
		}
		contained += `]}`
	}
	return []byte(`{"resourceType":"Coverage","payor":[` + payors + `],"contained":[` + contained + `]}`)
}

// Every payor counts, not only payor[0]: Coverage.payor carries no ordering meaning.
func TestChooseRegisteredPayerEveryPayor(t *testing.T) {
	choose := func(t *testing.T, cov []byte) (PayerIdentifier, string, error) {
		t.Helper()
		cands, err := CoveragePayerCandidates(cov, nil)
		if err != nil {
			t.Fatalf("candidates: %v", err)
		}
		return ChooseRegisteredPayer(cands, dir)
	}
	t.Run("a second payor naming the same holder routes", func(t *testing.T) {
		if pid, holder, err := choose(t, payorsCoverage([]PayerIdentifier{payer301}, []PayerIdentifier{epic301})); err != nil || pid != payer301 || holder != "payer-22" {
			t.Fatalf("got %v %q %v", pid, holder, err)
		}
	})
	t.Run("a second payor naming another registered holder is ambiguous", func(t *testing.T) {
		if _, _, err := choose(t, payorsCoverage([]PayerIdentifier{payer301}, []PayerIdentifier{payer300})); !errors.Is(err, ErrAmbiguousCoveragePayer) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("a registered payor after an unregistered payor[0] routes", func(t *testing.T) {
		cov := payorsCoverage([]PayerIdentifier{npi, ein}, []PayerIdentifier{payer301})
		if pid, holder, err := choose(t, cov); err != nil || pid != payer301 || holder != "payer-22" {
			t.Fatalf("got %v %q %v", pid, holder, err)
		}
		// The position-based answer still reads payor[0] alone.
		if pid, err := ParseCoveragePayer(cov, nil); err != nil || pid != npi {
			t.Fatalf("ParseCoveragePayer: %v %v, want payor[0]'s first identifier %v", pid, err, npi)
		}
	})
	t.Run("a payor whose reference resolves to nothing makes the payer unknown", func(t *testing.T) {
		cov := []byte(`{"resourceType":"Coverage","id":"c1","payor":[{"reference":"#p"},{"reference":"#gone"}],` +
			`"contained":[{"resourceType":"Organization","id":"p","identifier":[{"system":"` + cmsPayerSystem + `","value":"00301"}]}]}`)
		_, err := CoveragePayerCandidates(cov, nil)
		var u *UnresolvedPayorError
		if !errors.Is(err, ErrUnresolvedPayor) || !errors.Is(err, ErrNoCoveragePayer) || !errors.As(err, &u) || u.Index != 1 || u.Reference != "#gone" || u.Coverage != "c1" {
			t.Fatalf("err %v, want UnresolvedPayorError naming payor[1] #gone of Coverage c1", err)
		}
	})
}

func TestChooseRegisteredPayerEdges(t *testing.T) {
	t.Run("a registered identifier before one answered ok with no holder routes", func(t *testing.T) {
		resolve := func(p PayerIdentifier) (string, bool) {
			if p == payer301 {
				return "payer-22", true
			}
			return "", true
		}
		if pid, holder, err := ChooseRegisteredPayer([][]PayerIdentifier{{payer301, npi}}, resolve); err != nil || pid != payer301 || holder != "payer-22" {
			t.Fatalf("got %v %q %v", pid, holder, err)
		}
	})
	t.Run("an unregistered set and its subset are ambiguous", func(t *testing.T) {
		if _, _, err := ChooseRegisteredPayer([][]PayerIdentifier{{npi, ein}, {npi}}, dir); !errors.Is(err, ErrAmbiguousCoveragePayer) {
			t.Fatalf("err %v", err)
		}
		if _, _, err := ChooseRegisteredPayer([][]PayerIdentifier{{npi, ein}, {npi, npi}}, dir); !errors.Is(err, ErrAmbiguousCoveragePayer) {
			t.Fatalf("with a repeat: err %v", err)
		}
	})
}

func TestCoveragePayerCandidatesUnknownAndSelfPay(t *testing.T) {
	cms := `{"system":"` + cmsPayerSystem + `","value":"00301"}`
	org := `{"resourceType":"Organization","id":"p","identifier":[` + cms + `]}`
	cov := func(payors, contained string) []byte {
		return []byte(`{"resourceType":"Coverage","payor":[` + payors + `],"contained":[` + contained + `]}`)
	}
	unresolved := func(t *testing.T, v []byte, resolve func(string) ([]byte, bool), index int) {
		t.Helper()
		_, err := CoveragePayerCandidates(v, resolve)
		var u *UnresolvedPayorError
		if !errors.As(err, &u) || u.Index != index {
			t.Fatalf("err %v, want UnresolvedPayorError at payor[%d]", err, index)
		}
	}
	routes := func(t *testing.T, v []byte, resolve func(string) ([]byte, bool)) {
		t.Helper()
		cands, err := CoveragePayerCandidates(v, resolve)
		if err != nil {
			t.Fatalf("candidates: %v", err)
		}
		if pid, holder, err := ChooseRegisteredPayer(cands, dir); err != nil || pid != payer301 || holder != "payer-22" {
			t.Fatalf("got %v %q %v, want payer-22 by 00301", pid, holder, err)
		}
	}
	t.Run("a registered payor beside a Patient self-pay payor routes", func(t *testing.T) {
		routes(t, cov(`{"reference":"#p"},{"reference":"Patient/m"}`, org), nil)
	})
	t.Run("a contained RelatedPerson payor is self-pay", func(t *testing.T) {
		routes(t, cov(`{"reference":"#p"},{"reference":"#rp"}`, org+`,{"resourceType":"RelatedPerson","id":"rp"}`), nil)
	})
	t.Run("a payor typed RelatedPerson whose reference resolves to nothing is self-pay", func(t *testing.T) {
		routes(t, cov(`{"reference":"#p"},{"type":"RelatedPerson","reference":"urn:uuid:9a6e"}`, org), func(string) ([]byte, bool) { return nil, false })
	})
	t.Run("a payor known by its inline identifier keeps it when its reference resolves to nothing", func(t *testing.T) {
		routes(t, cov(`{"identifier":`+cms+`,"reference":"Organization/gone"}`, ""), func(string) ([]byte, bool) { return nil, false })
	})
	t.Run("a registered payor beside a dangling one is refused", func(t *testing.T) {
		unresolved(t, cov(`{"reference":"#p"},{"reference":"#gone"}`, org), nil, 1)
	})
	t.Run("an external payor the resolver does not answer is unknown", func(t *testing.T) {
		unresolved(t, cov(`{"reference":"#p"},{"reference":"Organization/other"}`, org), func(string) ([]byte, bool) { return nil, false }, 1)
	})
	t.Run("an external payor with no resolver is unknown", func(t *testing.T) {
		unresolved(t, cov(`{"reference":"Organization/other"},{"reference":"#p"}`, org), nil, 0)
	})
	t.Run("a payor Organization with no identifier is unknown", func(t *testing.T) {
		unresolved(t, cov(`{"reference":"#p"},{"reference":"#e"}`, org+`,{"resourceType":"Organization","id":"e"}`), nil, 1)
	})
	t.Run("a payor naming nothing is unknown", func(t *testing.T) {
		unresolved(t, cov(`{"reference":"#p"},{"display":"a plan"}`, org), nil, 1)
	})
	t.Run("a self-pay Coverage names no payer", func(t *testing.T) {
		if _, err := CoveragePayerCandidates(cov(`{"reference":"Patient/m"}`, ""), nil); !errors.Is(err, ErrNoCoveragePayer) || errors.Is(err, ErrUnresolvedPayor) {
			t.Fatalf("err %v, want ErrNoCoveragePayer alone", err)
		}
	})
	t.Run("a Bundle's unresolved payor names its Coverage", func(t *testing.T) {
		b := []byte(`{"resourceType":"Bundle","entry":[{"resource":{"resourceType":"Coverage","id":"c2","payor":[{"reference":"Organization/none"}]}}]}`)
		_, err := CoveragePayerCandidates(b, nil)
		var u *UnresolvedPayorError
		if !errors.As(err, &u) || u.Coverage != "c2" || u.Index != 0 {
			t.Fatalf("err %v", err)
		}
	})
}

func TestChooseRegisteredPayerSubsetEitherWay(t *testing.T) {
	if _, _, err := ChooseRegisteredPayer([][]PayerIdentifier{{npi}, {npi, ein}}, dir); !errors.Is(err, ErrAmbiguousCoveragePayer) {
		t.Fatalf("err %v", err)
	}
}

// A payor's type decides by structure: its Reference.type when it states one,
// ahead of the type its reference resolves to or names.
func TestCoveragePayerCandidatesTypePrecedence(t *testing.T) {
	cms := `{"system":"` + cmsPayerSystem + `","value":"00301"}`
	org := `{"resourceType":"Organization","id":"p","identifier":[` + cms + `]}`
	cov := func(payors, contained string) []byte {
		return []byte(`{"resourceType":"Coverage","payor":[` + payors + `],"contained":[` + contained + `]}`)
	}
	patientAt := func(ref string) func(string) ([]byte, bool) {
		return func(r string) ([]byte, bool) { return []byte(`{"resourceType":"Patient","id":"9"}`), r == ref }
	}
	orgAt := func(ref string) func(string) ([]byte, bool) {
		return func(r string) ([]byte, bool) { return []byte(org), r == ref }
	}
	unknownAt := func(t *testing.T, v []byte, resolve func(string) ([]byte, bool), index int) {
		t.Helper()
		_, err := CoveragePayerCandidates(v, resolve)
		var u *UnresolvedPayorError
		if !errors.As(err, &u) || u.Index != index {
			t.Fatalf("err %v, want UnresolvedPayorError at payor[%d]", err, index)
		}
	}
	candidates := func(t *testing.T, v []byte, resolve func(string) ([]byte, bool), want []PayerIdentifier) {
		t.Helper()
		got, err := CoveragePayerCandidates(v, resolve)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], want) {
			t.Fatalf("got %v, %v, want %v", got, err, want)
		}
	}
	t.Run("type Organization on a dangling Patient reference is unknown, not self-pay", func(t *testing.T) {
		unknownAt(t, cov(`{"reference":"#p"},{"type":"Organization","reference":"Patient/9"}`, org), func(string) ([]byte, bool) { return nil, false }, 1)
	})
	t.Run("type Organization on a reference resolving to a Patient is unknown", func(t *testing.T) {
		unknownAt(t, cov(`{"reference":"#p"},{"type":"Organization","reference":"Organization/9"}`, org), patientAt("Organization/9"), 1)
	})
	t.Run("type Patient on a reference resolving to an Organization is self-pay and adds nothing", func(t *testing.T) {
		candidates(t, cov(`{"reference":"#p"},{"type":"Patient","reference":"Organization/9"}`, org), func(r string) ([]byte, bool) {
			return []byte(`{"resourceType":"Organization","identifier":[{"system":"` + epicPayerID + `","value":"PAYER-301"}]}`), r == "Organization/9"
		}, []PayerIdentifier{payer301})
	})
	t.Run("a history reference to a RelatedPerson is self-pay", func(t *testing.T) {
		candidates(t, cov(`{"reference":"#p"},{"reference":"RelatedPerson/1/_history/2"}`, org), nil, []PayerIdentifier{payer301})
	})
	t.Run("an absolute reference to a Patient is self-pay", func(t *testing.T) {
		candidates(t, cov(`{"reference":"#p"},{"reference":"https://ehr.example/fhir/Patient/1"}`, org), nil, []PayerIdentifier{payer301})
	})
	t.Run("a payor resolving to a Practitioner is unknown", func(t *testing.T) {
		unknownAt(t, cov(`{"reference":"#p"},{"reference":"#pr"}`, org+`,{"resourceType":"Practitioner","id":"pr"}`), nil, 1)
	})
	t.Run("an Organization reference resolving through resolveRef adds its identifiers", func(t *testing.T) {
		candidates(t, cov(`{"reference":"Organization/9"}`, ""), orgAt("Organization/9"), []PayerIdentifier{payer301})
	})
	t.Run("a bare # names the first contained Organization with an identifier, as before", func(t *testing.T) {
		candidates(t, cov(`{"reference":"#"}`, org), nil, []PayerIdentifier{payer301})
	})
	t.Run("of contained resources sharing an id, the Organization with an identifier is read, as before", func(t *testing.T) {
		candidates(t, cov(`{"reference":"#p"}`, `{"resourceType":"Organization","id":"p"},`+org), nil, []PayerIdentifier{payer301})
	})
	t.Run("a non-string type or Coverage id is read as absent, as before", func(t *testing.T) {
		v := []byte(`{"resourceType":"Coverage","id":7,"payor":[{"type":{"x":1},"reference":"#p"}],"contained":[` + org + `]}`)
		candidates(t, v, nil, []PayerIdentifier{payer301})
	})
}

// A declared type decides, an inline identifier included.
func TestCoveragePayerCandidatesDeclaredTypeAndInline(t *testing.T) {
	cms := `{"system":"` + cmsPayerSystem + `","value":"00301"}`
	mrn := `{"system":"urn:example:mrn","value":"M1"}`
	orgAt := func(ref string) func(string) ([]byte, bool) {
		return func(r string) ([]byte, bool) {
			return []byte(`{"resourceType":"Organization","identifier":[{"system":"` + epicPayerID + `","value":"PAYER-301"}]}`), r == ref
		}
	}
	cov := func(payors string) []byte { return []byte(`{"resourceType":"Coverage","payor":[` + payors + `]}`) }
	got := func(t *testing.T, v []byte, resolve func(string) ([]byte, bool)) ([][]PayerIdentifier, error) {
		t.Helper()
		return CoveragePayerCandidates(v, resolve)
	}
	t.Run("a self-pay payor's inline identifier is the patient's and is not offered", func(t *testing.T) {
		if c, err := got(t, cov(`{"identifier":`+cms+`},{"type":"Patient","reference":"Patient/m","identifier":`+mrn+`}`), nil); err != nil || len(c) != 1 || !slices.Equal(c[0], []PayerIdentifier{payer301}) {
			t.Fatalf("got %v, %v, want only the payer's identifier", c, err)
		}
		if _, err := got(t, cov(`{"reference":"Patient/m","identifier":`+mrn+`}`), nil); !errors.Is(err, ErrNoCoveragePayer) || errors.Is(err, ErrUnresolvedPayor) {
			t.Fatalf("a self-pay Coverage: err %v, want ErrNoCoveragePayer alone", err)
		}
		// Beside an unregistered payor, the refusal names that payor's identifier alone:
		// the patient's never appears in its text.
		c, err := got(t, cov(`{"identifier":{"system":"`+npiSystem+`","value":"1234567893"}},{"reference":"Patient/m","identifier":`+mrn+`}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = ChooseRegisteredPayer(c, dir)
		var nr *NoRegisteredPayerError
		if !errors.As(err, &nr) || !slices.Equal(nr.Candidates, []PayerIdentifier{npi}) || strings.Contains(err.Error(), "M1") {
			t.Fatalf("err %v, want NoRegisteredPayerError naming the NPI alone", err)
		}
	})
	t.Run("a payor declared another type on an Organization reference is unknown", func(t *testing.T) {
		_, err := got(t, cov(`{"identifier":`+cms+`},{"type":"Practitioner","reference":"Organization/9"}`), orgAt("Organization/9"))
		var u *UnresolvedPayorError
		if !errors.As(err, &u) || u.Index != 1 {
			t.Fatalf("err %v, want UnresolvedPayorError at payor[1]", err)
		}
	})
	t.Run("an inline identifier on a payor declared another type is kept; its reference adds nothing", func(t *testing.T) {
		if c, err := got(t, cov(`{"type":"Practitioner","reference":"Organization/9","identifier":`+cms+`}`), orgAt("Organization/9")); err != nil || len(c) != 1 || !slices.Equal(c[0], []PayerIdentifier{payer301}) {
			t.Fatalf("got %v, %v", c, err)
		}
	})
	t.Run("an inline identifier on an Organization payor is joined by the Organization's", func(t *testing.T) {
		if c, err := got(t, cov(`{"type":"Organization","reference":"Organization/9","identifier":`+cms+`}`), orgAt("Organization/9")); err != nil || len(c) != 1 || !slices.Equal(c[0], []PayerIdentifier{payer301, epic301}) {
			t.Fatalf("got %v, %v", c, err)
		}
	})
	t.Run("a bare # with no contained Organization and an id-less contained Patient is unknown", func(t *testing.T) {
		_, err := got(t, []byte(`{"resourceType":"Coverage","payor":[{"reference":"#"}],"contained":[{"resourceType":"Patient"}]}`), nil)
		if !errors.Is(err, ErrUnresolvedPayor) {
			t.Fatalf("err %v", err)
		}
	})
}

// TestCoveragePayerCandidatesSeveralEntries: in a Bundle, a payor reference that
// several entries answer picks none of them. Several Organizations name every
// identifier they name, in document order, each once, for the registry to
// decide among, as a Smart Gateway reads them; several that are not all
// Organizations name no payer and are never passed to resolveRef.
// ParseCoveragePayer still reads the first entry.
func TestCoveragePayerCandidatesSeveralEntries(t *testing.T) {
	org := func(ids ...PayerIdentifier) string {
		var list []string
		for _, id := range ids {
			list = append(list, `{"system":"`+id.System+`","value":"`+id.Value+`"}`)
		}
		return `{"resource":{"resourceType":"Organization","id":"p","identifier":[` + strings.Join(list, ",") + `]}}`
	}
	bundle := func(payor string, entries ...string) []byte {
		return []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Coverage","id":"c1","payor":[` +
			payor + `]}},` + strings.Join(entries, ",") + `]}`)
	}
	const byRef = `{"reference":"Organization/p"}`
	other := PayerIdentifier{cmsPayerSystem, "99999"}

	t.Run("two Organizations naming two registered payers: ambiguous", func(t *testing.T) {
		b := bundle(byRef, org(npi, payer301), org(npi, ein, payer300))
		got, err := CoveragePayerCandidates(b, nil)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{npi, payer301, ein, payer300}) {
			t.Fatalf("got %v, %v, want the union", got, err)
		}
		if _, _, err := ChooseRegisteredPayer(got, dir); !errors.Is(err, ErrAmbiguousCoveragePayer) {
			t.Fatalf("choose: %v, want ambiguous", err)
		}
		if pid, err := ParseCoveragePayer(b, nil); err != nil || pid != npi {
			t.Fatalf("ParseCoveragePayer: %v, %v, want the first entry's first identifier", pid, err)
		}
	})
	t.Run("two Organizations naming one payer beside unregistered identifiers: that payer", func(t *testing.T) {
		got, err := CoveragePayerCandidates(bundle(byRef, org(npi, payer301), org(payer301, other)), nil)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{npi, payer301, other}) {
			t.Fatalf("got %v, %v", got, err)
		}
		if pid, holder, err := ChooseRegisteredPayer(got, dir); err != nil || holder != "payer-22" || pid != payer301 {
			t.Fatalf("choose: %v %q %v, want payer-22 by 00301", pid, holder, err)
		}
	})
	t.Run("an Organization naming no identifier beside one that names some: the latter", func(t *testing.T) {
		got, err := CoveragePayerCandidates(bundle(byRef, org(), org(payer301)), nil)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{payer301}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("the same entry twice: as one", func(t *testing.T) {
		got, err := CoveragePayerCandidates(bundle(byRef, org(npi, payer301), org(npi, payer301)), nil)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{npi, payer301}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	const urn = "urn:uuid:5e7a0c1d-0000-4000-8000-0000000000a1"
	mixed := []string{
		`{"fullUrl":"` + urn + `","resource":{"resourceType":"Organization","id":"o","identifier":[{"system":"` + cmsPayerSystem + `","value":"00301"}]}}`,
		`{"fullUrl":"` + urn + `","resource":{"resourceType":"Patient","id":"m"}}`,
	}
	read := false
	never := func(string) ([]byte, bool) {
		read = true
		return []byte(`{"resourceType":"Organization","identifier":[{"system":"` + cmsPayerSystem + `","value":"00301"}]}`), true
	}
	t.Run("an Organization and a resource that is not one: the payor is unknown, and resolveRef is never asked", func(t *testing.T) {
		_, err := CoveragePayerCandidates(bundle(`{"reference":"`+urn+`"}`, mixed...), never)
		var unresolved *UnresolvedPayorError
		if !errors.As(err, &unresolved) || unresolved.Index != 0 || unresolved.Reference != urn {
			t.Fatalf("got %v, want the payor unresolved", err)
		}
		if read {
			t.Fatal("resolveRef was asked for a reference the Bundle answers")
		}
	})
	t.Run("the same, beside an inline identifier: known by it", func(t *testing.T) {
		got, err := CoveragePayerCandidates(bundle(`{"reference":"`+urn+`","identifier":{"system":"`+cmsPayerSystem+`","value":"00300"}}`, mixed...), never)
		if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{payer300}) {
			t.Fatalf("got %v, %v, want the inline identifier alone", got, err)
		}
		if read {
			t.Fatal("resolveRef was asked for a reference the Bundle answers")
		}
	})
}

// An entry carrying no resource member answers no reference, beside an
// Organization that does, in either order, as a Smart Gateway reads it; one whose
// resource is null answers it as a resource that is no Organization, so the payor
// is unknown, as the gateway reads that too.
func TestCoveragePayerCandidatesSeveralEntriesResourceless(t *testing.T) {
	const urn = "urn:uuid:5e7a0c1d-0000-4000-8000-0000000000a2"
	org := `{"fullUrl":"` + urn + `","resource":{"resourceType":"Organization","id":"o","identifier":[{"system":"` + cmsPayerSystem + `","value":"00301"}]}}`
	empty := `{"fullUrl":"` + urn + `"}`
	for name, entries := range map[string][]string{"the Organization first": {org, empty}, "the empty entry first": {empty, org}} {
		t.Run(name, func(t *testing.T) {
			b := []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Coverage","id":"c1","payor":[{"reference":"` + urn + `"}]}},` +
				entries[0] + `,` + entries[1] + `]}`)
			got, err := CoveragePayerCandidates(b, nil)
			if err != nil || len(got) != 1 || !slices.Equal(got[0], []PayerIdentifier{payer301}) {
				t.Fatalf("got %v, %v, want the Organization's identifier", got, err)
			}
		})
	}
	null := `{"fullUrl":"` + urn + `","resource":null}`
	for name, entries := range map[string][]string{"a null resource after the Organization": {org, null}, "a null resource first": {null, org}} {
		t.Run(name, func(t *testing.T) {
			b := []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Coverage","id":"c1","payor":[{"reference":"` + urn + `"}]}},` +
				entries[0] + `,` + entries[1] + `]}`)
			_, err := CoveragePayerCandidates(b, nil)
			var unresolved *UnresolvedPayorError
			if !errors.As(err, &unresolved) {
				t.Fatalf("got %v, want the payor unknown", err)
			}
		})
	}
}
