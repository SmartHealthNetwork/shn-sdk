package shnsdk

import "testing"

func TestSelectPayerPASLineFromAdvertisedCapability(t *testing.T) {
	for _, row := range []struct {
		name     string
		versions []string
		want     string
		refuse   bool
	}{
		{"legacy", nil, "2.0", false},
		{"current", []string{ContractPAPAS20, ContractPAPAS22}, "2.2", false},
		{"mid", []string{ContractPAPAS21}, "2.1", false},
		{"unrelated", []string{ContractPACRD22}, "", true},
		{"unsupported", []string{"pa.pas@9.9"}, "", true},
	} {
		t.Run(row.name, func(t *testing.T) {
			got, err := selectPayerPASLine(Payer{ContractVersions: row.versions})
			if (err != nil) != row.refuse || got != row.want {
				t.Fatalf("line=%q err=%v, want %q refuse=%v", got, err, row.want, row.refuse)
			}
		})
	}
}
