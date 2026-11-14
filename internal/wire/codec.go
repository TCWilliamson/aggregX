// Package wire implements the device envelope, reply, COSE profile and gateway
// relay formats of specs 01 and 02. The normative grammar is
// spec/wire/envelope.cddl; this package is checked against it and against the
// golden vectors in spec/vectors.
package wire

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

var (
	encMode cbor.EncMode
	decMode cbor.DecMode
	// strictDecMode additionally rejects map keys that do not match a struct field.
	strictDecMode cbor.DecMode
)

func init() {
	var err error
	// Deterministic encoding, RFC 8949 §4.2.1 (spec README "Conventions").
	encMode, err = cbor.EncOptions{
		Sort:          cbor.SortCoreDeterministic,
		ShortestFloat: cbor.ShortestFloat16,
		NaNConvert:    cbor.NaNConvert7e00,
		InfConvert:    cbor.InfConvertFloat16,
		IndefLength:   cbor.IndefLengthForbidden,
	}.EncMode()
	if err != nil {
		panic(err)
	}
	base := cbor.DecOptions{
		DupMapKey:        cbor.DupMapKeyEnforcedAPF,
		MaxNestedLevels:  16,
		MaxArrayElements: 1 << 16,
		MaxMapPairs:      1 << 16,
		IntDec:           cbor.IntDecConvertSigned,
		DefaultMapType:   nil,
	}
	decMode, err = base.DecMode()
	if err != nil {
		panic(err)
	}
	strict := base
	strict.ExtraReturnErrors = cbor.ExtraDecErrorUnknownField
	strictDecMode, err = strict.DecMode()
	if err != nil {
		panic(err)
	}
}

// Marshal encodes v with the package's deterministic encoding.
func Marshal(v any) ([]byte, error) { return encMode.Marshal(v) }

// Unmarshal decodes data into v. Trailing bytes are an error.
func Unmarshal(data []byte, v any) error {
	rest, err := decMode.UnmarshalFirst(data, v)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("wire: %d trailing bytes", len(rest))
	}
	return nil
}

func unmarshalStrict(data []byte, v any) error {
	rest, err := strictDecMode.UnmarshalFirst(data, v)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("wire: %d trailing bytes", len(rest))
	}
	return nil
}

// Error is a wire-level validation failure. Code is a stable identifier used
// by golden vectors and quarantine reasons.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return "wire: " + e.Code
	}
	return "wire: " + e.Code + ": " + e.Detail
}

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, a...)}
}
