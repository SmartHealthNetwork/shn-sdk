package shnsdk

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// CDexTaskPath is a path to a payer's CDex Task below a FHIR base, as the
// CDex Task's frame headers carry it (FrameHeaderReadPath,
// FrameHeaderTaskLocation): "Task/<id>" or "Task/<id>/_history/<vid>", and,
// for a read only, "?" and the query as sent.
type CDexTaskPath struct {
	// ID is the Task's logical id, a FHIR id.
	ID string
	// VersionID is the version a vread names, a FHIR id; empty for a read.
	VersionID string
	// HasQuery says the path carries a query, which may be empty ("Task/1?").
	HasQuery bool
	// RawQuery is the query after the "?", exactly as sent.
	RawQuery string
}

// String is the path as a frame header carries it: the value it was read
// from, byte for byte.
func (p CDexTaskPath) String() string {
	s := "Task/" + p.ID
	if p.VersionID != "" {
		s += "/_history/" + p.VersionID
	}
	if p.HasQuery {
		s += "?" + p.RawQuery
	}
	return s
}

// ErrCDexTaskPath is a value of FrameHeaderReadPath or FrameHeaderTaskLocation
// that is not a path to a Task by its id: the gateway that reads it refuses
// it, and sends nothing on.
var ErrCDexTaskPath = errors.New("shnsdk: not a CDex Task path")

// ParseCDexTaskReadPath reads a FrameHeaderReadPath value: "Task/<id>" or
// "Task/<id>/_history/<vid>", then optionally "?" and a query. <id> and <vid>
// are FHIR ids ([A-Za-z0-9\-\.]{1,64}) other than "." and "..", the dot
// segments, which a server reading the path below its own base would take as
// a step out of it. The query is carried exactly as sent, and the provider's
// system judges it as it would directly; only what cannot be carried as a
// query is refused: a control character (CR and LF among them), a space, a
// "#", which would begin a fragment, and bytes that are not UTF-8. Anything
// else that is not such a path is ErrCDexTaskPath.
func ParseCDexTaskReadPath(v string) (CDexTaskPath, error) {
	return parseCDexTaskPath(v, true)
}

// ParseCDexTaskLocation reads a FrameHeaderTaskLocation value: "Task/<id>" or
// "Task/<id>/_history/<vid>", with <id> and <vid> as ParseCDexTaskReadPath
// reads them, and no query. Anything else is ErrCDexTaskPath.
func ParseCDexTaskLocation(v string) (CDexTaskPath, error) {
	return parseCDexTaskPath(v, false)
}

func parseCDexTaskPath(v string, query bool) (CDexTaskPath, error) {
	var p CDexTaskPath
	path, q, hasQuery := strings.Cut(v, "?")
	if hasQuery {
		if !query {
			return CDexTaskPath{}, fmt.Errorf("%w: a Task location carries no query", ErrCDexTaskPath)
		}
		if !carriesAsQuery(q) {
			return CDexTaskPath{}, fmt.Errorf("%w: the query holds a control character, a space, a # or bytes that are not UTF-8", ErrCDexTaskPath)
		}
		p.HasQuery, p.RawQuery = true, q
	}
	rest, ok := strings.CutPrefix(path, "Task/")
	if !ok {
		return CDexTaskPath{}, fmt.Errorf("%w: it does not begin Task/", ErrCDexTaskPath)
	}
	id, history, versioned := strings.Cut(rest, "/")
	if !isFHIRPathID(id) {
		return CDexTaskPath{}, fmt.Errorf("%w: the Task id is not a FHIR id, or is a dot segment", ErrCDexTaskPath)
	}
	p.ID = id
	if versioned {
		vid, ok := strings.CutPrefix(history, "_history/")
		if !ok || !isFHIRPathID(vid) {
			return CDexTaskPath{}, fmt.Errorf("%w: after the id, only /_history/ and a version id", ErrCDexTaskPath)
		}
		p.VersionID = vid
	}
	return p, nil
}

// isFHIRPathID reports whether s is a FHIR id (fhirIDPattern) that is not the
// dot segment "." or "..". Other ids of dots only ("...") are FHIR ids and no
// dot segment, so they are read as ids.
func isFHIRPathID(s string) bool {
	return s != "." && s != ".." && fhirIDPattern.MatchString(s)
}

// carriesAsQuery reports whether s can be carried as a request's query exactly
// as sent: valid UTF-8, with no control character (below 0x20, or 0x7f), no
// space and no "#". Every other byte is the provider system's to judge.
func carriesAsQuery(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c == ' ' || c == '#' {
			return false
		}
	}
	return true
}
