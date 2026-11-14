package wire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

var update = flag.Bool("update", false, "rewrite golden vectors in spec/vectors")

const vectorDir = "../../spec/vectors"

type example struct {
	name string
	v    any
}

func examples() []example {
	return []example{
		{"envelope_11_1_minimal", ExampleMinimal()},
		{"envelope_11_2_thread", ExampleThread()},
		{"reply_11_3", ExampleReply()},
		{"envelope_11_4_uptime", ExampleUptime()},
		{"envelope_11_5_move_boundary_result", ExampleMoveBoundaryResult()},
	}
}

func TestExamplesRoundTrip(t *testing.T) {
	for _, ex := range examples() {
		t.Run(ex.name, func(t *testing.T) {
			b, err := Marshal(ex.v)
			if err != nil {
				t.Fatal(err)
			}
			diag, _ := cbor.Diagnose(b)
			t.Logf("%d bytes\n%s\n%s", len(b), hex.EncodeToString(b), diag)

			if strings.HasPrefix(ex.name, "envelope") {
				envelopeRoundTrip(t, b)
			} else {
				replyRoundTrip(t, b)
			}
			golden(t, ex.name, b)
		})
	}
}

// envelopeRoundTrip checks spec 01 §2: decode then re-encode gives identical bytes.
func envelopeRoundTrip(t *testing.T, b []byte) {
	t.Helper()
	e, h, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(h.Spans) == 0 && !h.HasMRes {
		t.Fatal("no spans")
	}
	b2, err := e.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, b2) {
		t.Fatalf("not deterministic:\n%x\n%x", b, b2)
	}
}

func replyRoundTrip(t *testing.T, b []byte) {
	t.Helper()
	r, err := DecodeReply(b)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, b2) {
		t.Fatalf("reply not deterministic")
	}
}

// writeVector writes a golden vector under -update. Vectors are committed
// source files, so they get ordinary source permissions.
func writeVector(t *testing.T, name string, data []byte) {
	t.Helper()
	//nolint:gosec // G306: committed golden vectors are world-readable source files.
	if err := os.WriteFile(filepath.Join(vectorDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func golden(t *testing.T, name string, b []byte) {
	t.Helper()
	path := filepath.Join(vectorDir, name+".cbor")
	diag, err := cbor.Diagnose(b)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		writeVector(t, name+".cbor", b)
		writeVector(t, name+".diag", []byte(diag+"\n"))
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // G304: path is built from the fixed vector directory and a test-table name.
	if err != nil {
		t.Fatalf("missing vector (run go test ./internal/wire -update): %v", err)
	}
	if !bytes.Equal(want, b) {
		t.Fatalf("vector %s changed:\nwant %x\ngot  %x", name, want, b)
	}
}

func TestHeaderSpans(t *testing.T) {
	b, _ := ExampleThread().Encode()
	h, err := DecodeHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint16]SectionSpan{}
	for _, s := range h.Spans {
		got[s.Series] = s
	}
	want := map[uint16]uint32{SeriesMain: 5120, SeriesEvents: 300, SeriesBlocks: 44, 17: 901}
	for series, first := range want {
		if got[series].First != first || got[series].Count != 1 {
			t.Errorf("series %d: got %+v", series, got[series])
		}
	}
}

// mutate decodes b as a generic map, applies f, and re-encodes.
func mutate(t *testing.T, b []byte, f func(m map[uint64]any)) []byte {
	t.Helper()
	var m map[uint64]any
	if err := Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	out, err := Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInvalidEnvelopes(t *testing.T) {
	base, _ := ExampleMinimal().Encode()
	cases := []struct {
		name string
		data []byte
		code string
	}{
		{"missing_inc", mutate(t, base, func(m map[uint64]any) { delete(m, 2) }), "missing_key"},
		{"missing_cfg", mutate(t, base, func(m map[uint64]any) { delete(m, 7) }), "missing_key"},
		{"unknown_key_18", mutate(t, base, func(m map[uint64]any) { m[18] = 1 }), "unknown_key"},
		{"v2", mutate(t, base, func(m map[uint64]any) { m[0] = 2 }), "bad_version"},
		{"inc_zero", mutate(t, base, func(m map[uint64]any) { m[2] = 0 }), "bad_inc"},
		{"dev_lowercase", mutate(t, base, func(m map[uint64]any) { m[1] = "sn-00042" }), "bad_serial"},
		{"dev_33", mutate(t, base, func(m map[uint64]any) { m[1] = strings.Repeat("A", 33) }), "bad_serial"},
		{"cfg_7_bytes", mutate(t, base, func(m map[uint64]any) { m[7] = []byte{1, 2, 3, 4, 5, 6, 7} }), "bad_cfg"},
		{"tb_2", mutate(t, base, func(m map[uint64]any) { m[8] = 2 }), "bad_tb"},
		{"empty", mutate(t, base, func(m map[uint64]any) { delete(m, 11) }), "empty_envelope"},
		{"slot_0", mutate(t, base, func(m map[uint64]any) {
			m[12] = map[uint64]any{0: []any{1, []any{[]any{0, map[uint64]any{1: 1}}}, "M-1", "co2@2.0"}}
		}), "bad_slot"},
		{"slot_240", mutate(t, base, func(m map[uint64]any) {
			m[12] = map[uint64]any{240: []any{1, []any{[]any{0, map[uint64]any{1: 1}}}, "M-1", "co2@2.0"}}
		}), "bad_slot"},
		{"trailing_bytes", append(append([]byte{}, base...), 0x00), "not_cbor_map"},
		{"not_a_map", []byte{0x83, 0x01, 0x02, 0x03}, "not_cbor_map"},
		// {0:1, 0:1}: duplicate key.
		{"duplicate_key", []byte{0xA2, 0x00, 0x01, 0x00, 0x01}, "not_cbor_map"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeHeader(c.data)
			var we *Error
			if !errors.As(err, &we) {
				t.Fatalf("want *Error %s, got %v", c.code, err)
			}
			if we.Code != c.code {
				t.Fatalf("want %s, got %s (%v)", c.code, we.Code, err)
			}
			if *update {
				writeVector(t, "invalid_envelope_"+c.name+".cbor", c.data)
				writeVector(t, "invalid_envelope_"+c.name+".error", []byte(c.code+"\n"))
			}
		})
	}
}

func TestBodyErrorsQuarantine(t *testing.T) {
	e := ExampleThread()
	e.Blk.Blocks[0].Hz = 0
	b, _ := e.Encode()
	if _, err := DecodeHeader(b); err != nil {
		t.Fatalf("header must still decode at ingest: %v", err)
	}
	if _, _, err := DecodeEnvelope(b); err == nil {
		t.Fatal("worker decode must reject hz=0")
	}
	// Unknown key inside an event map: ingest accepts, workers reject.
	b2 := mutate(t, mustEncode(t, ExampleThread()), func(m map[uint64]any) {
		m[14] = []any{300, []any{map[uint64]any{0: 1, 1: 0, 9: 1}}}
	})
	if _, err := DecodeHeader(b2); err != nil {
		t.Fatalf("ingest must accept: %v", err)
	}
	if _, _, err := DecodeEnvelope(b2); err == nil {
		t.Fatal("worker must reject unknown event key")
	}
}

func mustEncode(t *testing.T, e *Envelope) []byte {
	b, err := e.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCOSE(t *testing.T) {
	key := TestKey(1)
	payload := mustEncode(t, ExampleThread())
	signed, err := Sign1(key, payload, AADBatch)
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(signed) - len(payload)
	t.Logf("envelope %d bytes, signed %d bytes, overhead %d", len(payload), len(signed), overhead)
	if overhead < 81 || overhead > 82 {
		t.Errorf("overhead %d, spec 02 §3 says 81-82", overhead)
	}
	m, err := ParseSign1(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Verify(&key.PublicKey, AADBatch); err != nil {
		t.Fatal(err)
	}
	if err = m.Verify(&key.PublicKey, AADReply); err == nil {
		t.Fatal("wrong AAD must fail")
	}
	if err = m.Verify(&TestKey(2).PublicKey, AADBatch); err == nil {
		t.Fatal("wrong key must fail")
	}
	// Flip one payload bit.
	bad := append([]byte{}, signed...)
	idx := bytes.Index(bad, []byte("SN-00107"))
	bad[idx] ^= 0x01
	m2, err := ParseSign1(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := m2.Verify(&key.PublicKey, AADBatch); err == nil {
		t.Fatal("tampered payload must fail")
	}
	if *update {
		writeVector(t, "cose_batch_valid_testkey1.cbor", signed)
		pub, err := UncompressedPoint(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		writeVector(t, "cose_batch_valid_testkey1.pub", []byte(hex.EncodeToString(pub)+"\n"))
	}
}

func TestCOSEStoredVectorVerifies(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(vectorDir, "cose_batch_valid_testkey1.cbor"))
	if err != nil {
		t.Fatalf("missing vector (spec 07 §2): %v", err)
	}
	m, err := ParseSign1(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(&TestKey(1).PublicKey, AADBatch); err != nil {
		t.Fatal(err)
	}
}

func TestCOSERejects(t *testing.T) {
	key := TestKey(1)
	kid, _ := KID(&key.PublicKey)
	payload := mustEncode(t, ExampleMinimal())
	sig := make([]byte, 64)
	build := func(tag uint64, prot []byte, unprot map[any]any, pl any, s []byte) []byte {
		b, err := Marshal(cbor.Tag{Number: tag, Content: []any{prot, unprot, pl, s}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	enc := func(v any) []byte { b, _ := Marshal(v); return b }
	cases := map[string][]byte{
		"cose_wrong_tag":   build(98, protectedHeader(kid), map[any]any{}, payload, sig),
		"cose_alg_eddsa":   build(18, enc(map[int]any{1: -8, 4: kid}), map[any]any{}, payload, sig),
		"cose_alg_none":    build(18, enc(map[int]any{1: 0, 4: kid}), map[any]any{}, payload, sig),
		"cose_alg_hmac":    build(18, enc(map[int]any{1: 5, 4: kid}), map[any]any{}, payload, sig),
		"cose_extra_label": build(18, enc(map[int]any{1: -7, 4: kid, 3: 60}), map[any]any{}, payload, sig),
		"cose_unprotected": build(18, protectedHeader(kid), map[any]any{4: kid}, payload, sig),
		"cose_detached":    build(18, protectedHeader(kid), map[any]any{}, nil, sig),
		"cose_sig_63":      build(18, protectedHeader(kid), map[any]any{}, payload, sig[:63]),
		"cose_untagged":    enc([]any{protectedHeader(kid), map[any]any{}, payload, sig}),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSign1(b); err == nil {
				t.Fatal("must reject")
			}
			if *update {
				writeVector(t, "invalid_"+name+".cbor", b)
			}
		})
	}
}

func TestAckCovers(t *testing.T) {
	a := Ack{Upto: 10, Ranges: []Range{{From: 20, To: 25}}}
	for seq, want := range map[uint32]bool{0: true, 10: true, 11: false, 20: true, 25: true, 26: false} {
		if a.Covers(seq) != want {
			t.Errorf("Covers(%d) != %v", seq, want)
		}
	}
	if (Ack{Upto: -1}).Covers(0) {
		t.Error("upto -1 covers nothing")
	}
}

// Example prints the worked examples of spec 01 §11 as hex and diagnostic notation.
func Example() {
	for _, ex := range examples() {
		b, _ := Marshal(ex.v)
		diag, _ := cbor.Diagnose(b)
		_ = diag
		fmt.Printf("%s: %d bytes\n", ex.name, len(b))
	}
	// Output:
	// envelope_11_1_minimal: 96 bytes
	// envelope_11_2_thread: 163 bytes
	// reply_11_3: 83 bytes
	// envelope_11_4_uptime: 117 bytes
	// envelope_11_5_move_boundary_result: 91 bytes
}
