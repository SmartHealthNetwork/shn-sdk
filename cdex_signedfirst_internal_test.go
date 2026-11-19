package shnsdk

import (
	"errors"
	"strings"
	"testing"
)

// cdexSignedFirst keeps the build's own failure where the records cannot be
// read at all, whatever their text holds: unreadable is not unsigned. The
// builders reach it only with records already read, so these rows ask it
// directly, beside a readable control that it refuses.
func TestCDexSignedFirst_UnreadableRecordsKeepTheFailure(t *testing.T) {
	task := []byte(`{"resourceType":"Task","id":"t1","status":"requested"}`)
	prov := `{"resourceType":"Provenance","target":[{"reference":"Task/t1"}],"signature":[{"data":"c2ln"}]}`
	failed := errors.New("the build's own failure")
	for _, row := range []struct{ name, records string }{
		{"not one JSON value", `{"resourceType":"Bundle","entry":[{"resource":` + prov + `}]} {}`},
		{"a member named twice", `{"resourceType":"Bundle","id":"","ID":"","entry":[{"resource":` + prov + `}]}`},
		{"past the reader's depth", `{"resourceType":"Bundle","entry":[{"resource":` + prov + `}],"x":` + strings.Repeat("[", 64) + strings.Repeat("]", 64) + `}`},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := cdexSignedFirst(task, []byte(row.records), false, failed); err != failed {
				t.Fatalf("got %v, want the build's own failure", err)
			}
		})
	}
	t.Run("control: the same records readable", func(t *testing.T) {
		recs := `{"resourceType":"Bundle","entry":[{"resource":` + prov + `}]}`
		if err := cdexSignedFirst(task, []byte(recs), false, failed); !errors.Is(err, ErrCDexSignedContent) {
			t.Fatalf("got %v, want ErrCDexSignedContent", err)
		}
	})
}
