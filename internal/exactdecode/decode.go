// Package exactdecode reads a JSON body as a Smart Gateway reads a
// participant's message for a decision: one well-formed document within the
// strict reader's bounds, repeating no member name, every member name that
// reaches a destination field matching it exactly. decodenames.go is the
// gateway's own field-name check, held identical to it.
package exactdecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/SmartHealthNetwork/shn-sdk/internal/splice"
)

var (
	// ErrDuplicateKey: a body repeats a member name in some object, exactly
	// or under case folding. Returned as a *DuplicateKeyError.
	ErrDuplicateKey = errors.New("relay: duplicate member name")
	// ErrInvalidJSON: a body is not exactly one well-formed JSON document.
	ErrInvalidJSON = errors.New("relay: invalid JSON")
	// ErrTooLarge: a body exceeds the size, depth or token bound.
	ErrTooLarge = errors.New("relay: body exceeds a size, depth or token limit")
)

// DuplicateKeyError reports the first repeated member name. Key is the
// decoded name and Offset the byte offset of its second occurrence.
type DuplicateKeyError struct {
	Key    string
	Offset int
	err    error
}

func (e *DuplicateKeyError) Error() string {
	return fmt.Sprintf("relay: duplicate member name %q at offset %d", e.Key, e.Offset)
}

// Is reports whether target is ErrDuplicateKey.
func (e *DuplicateKeyError) Is(target error) bool { return target == ErrDuplicateKey }

// Unwrap returns the scanner's error.
func (e *DuplicateKeyError) Unwrap() error { return e.err }

// scanError is a body the strict reader refuses for anything but a repeat:
// not one well-formed document (ErrInvalidJSON), or past a bound
// (ErrTooLarge).
type scanError struct {
	sentinel error
	err      error
}

func (e *scanError) Error() string { return e.sentinel.Error() + ": " + e.err.Error() }

// Is reports whether target is the error's sentinel.
func (e *scanError) Is(target error) bool { return target == e.sentinel }

// Unwrap returns the scanner's error.
func (e *scanError) Unwrap() error { return e.err }

func scan(b []byte) (*splice.Doc, error) {
	d, err := splice.Scan(b, splice.DefaultLimits())
	if err == nil {
		return d, nil
	}
	var dk *splice.DuplicateKeyError
	if errors.As(err, &dk) {
		return nil, &DuplicateKeyError{Key: dk.Key, Offset: dk.Offset, err: err}
	}
	if errors.Is(err, splice.ErrSizeLimit) || errors.Is(err, splice.ErrDepthLimit) || errors.Is(err, splice.ErrTokenLimit) {
		return nil, &scanError{sentinel: ErrTooLarge, err: err}
	}
	return nil, &scanError{sentinel: ErrInvalidJSON, err: err}
}

// Decode reads b into v as the gateway's relay.Decode reads a body: the
// body is first scanned strictly (a repeated member name anywhere is a
// *DuplicateKeyError; a body over a bound, or not one well-formed document,
// is refused), then member names must match v's field names exactly (a
// member that would reach a struct field only by case folding is a
// *FieldNameError). Numbers decode as json.Number. A value that does not
// fit v is reported with the decoder's own error.
func Decode(b []byte, v any) error {
	d, err := scan(b)
	if err != nil {
		return err
	}
	if err := checkFieldNames(d, d.Root(), v); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("relay: decode: %w", err)
	}
	return nil
}

// ReadsTwoWays reports whether err is Decode finding a body that can be read
// two ways: a repeated member name, or a member named only in another case.
func ReadsTwoWays(err error) bool {
	return errors.Is(err, ErrDuplicateKey) || errors.Is(err, ErrFieldNameCase)
}
