package wire

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var we *Error
	if !errors.As(err, &we) || we.Code != code {
		t.Fatalf("want error code %q, got %v", code, err)
	}
	if !strings.Contains(we.Error(), code) {
		t.Fatalf("Error() must include the code: %q", we.Error())
	}
}

// spec 02 §4.1: relay request validation.
func TestDecodeRelayRequest(t *testing.T) {
	ok, _ := Marshal(RelayRequest{V: 1, GW: "GW-00001", Items: [][]byte{{1}}, Poll: []PollEntry{{Dev: "SN-1", Inc: 7}}})
	r, err := DecodeRelayRequest(ok)
	if err != nil || r.GW != "GW-00001" || r.Poll[0].Inc != 7 {
		t.Fatalf("valid request: %v %+v", err, r)
	}
	v2, _ := Marshal(RelayRequest{V: 2, GW: "GW-00001"})
	_, err = DecodeRelayRequest(v2)
	wantCode(t, err, "bad_version")
	badGW, _ := Marshal(RelayRequest{V: 1, GW: "gw-1"})
	_, err = DecodeRelayRequest(badGW)
	wantCode(t, err, "bad_serial")
	many, _ := Marshal(RelayRequest{V: 1, GW: "GW-1", Items: make([][]byte, 501)})
	_, err = DecodeRelayRequest(many)
	wantCode(t, err, "too_many_items")
	unknown, _ := Marshal(map[int]any{0: 1, 1: "GW-1", 9: 1})
	_, err = DecodeRelayRequest(unknown)
	wantCode(t, err, "bad_relay_request")
}

// spec 01 §7: strict reply decoding.
func TestDecodeReplyRejectsUnknownKey(t *testing.T) {
	b, _ := Marshal(map[int]any{0: 1, 2: 1, 4: 0, 7: 60, 42: 1})
	_, err := DecodeReply(b)
	wantCode(t, err, "bad_reply")
}

// spec 01 §7.3: mres is [id, status] or [id, status, detail].
func TestMailboxResultShapes(t *testing.T) {
	for _, m := range []MailboxResult{{ID: 1, Status: MResApplied}, {ID: 2, Status: MResFailed, Detail: "boom"}} {
		b, err := Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var got MailboxResult
		if err := Unmarshal(b, &got); err != nil || got.ID != m.ID || got.Status != m.Status {
			t.Fatalf("round trip %+v: %v %+v", m, err, got)
		}
	}
	for _, bad := range []any{[]any{1}, []any{1, 2, 3, 4}, []any{"x", 1}, []any{1, "x"}} {
		b, _ := Marshal(bad)
		var got MailboxResult
		if err := Unmarshal(b, &got); err == nil {
			t.Errorf("must reject %v", bad)
		}
	}
}

// spec 01 §4.3: block numbers are int or float.
func TestNumber(t *testing.T) {
	for _, v := range []any{uint64(5), int64(-5), float32(0.5), 2.25} {
		b, _ := Marshal(v)
		var n Number
		if err := Unmarshal(b, &n); err != nil {
			t.Fatalf("%v: %v", v, err)
		}
	}
	b, _ := Marshal("x")
	var n Number
	if err := Unmarshal(b, &n); err == nil {
		t.Fatal("string is not a number")
	}
	enc, _ := Marshal(Number(3))
	if !bytes.Equal(enc, []byte{0x03}) {
		t.Fatalf("integral Number must encode as int, got %x", enc)
	}
}

// spec 01 §4.3, §4.1: body checks done by workers.
func TestValidateBody(t *testing.T) {
	cases := map[string]func(e *Envelope){
		"block_no_values":   func(e *Envelope) { e.Blk.Blocks[0].Values = nil },
		"block_too_many":    func(e *Envelope) { e.Blk.Blocks[0].Values = make([]Number, MaxBlockValues+1) },
		"block_hz_high":     func(e *Envelope) { e.Blk.Blocks[0].Hz = 20001 },
		"block_slot_240":    func(e *Envelope) { e.Blk.Blocks[0].Slot = 240 },
		"null_main_value":   func(e *Envelope) { e.R.Records[0].Values[1] = nil },
		"null_module_value": func(e *Envelope) { e.Mod[1].Records[0].Values[1] = nil },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := ExampleThread()
			mut(e)
			if err := e.validateBody(); err == nil {
				t.Fatal("must reject")
			}
		})
	}
	if err := ExampleThread().validateBody(); err != nil {
		t.Fatal(err)
	}
}

// spec 01 §10: section limits and sequence overflow.
func TestCheckSpan(t *testing.T) {
	if _, err := checkSpan(0, 1, 0); err == nil {
		t.Error("empty section")
	}
	if _, err := checkSpan(0, 1, MaxItemsPerSect+1); err == nil {
		t.Error("too many items")
	}
	if _, err := checkSpan(0, 4294967295, 2); err == nil {
		t.Error("seq overflow")
	}
	s, err := checkSpan(17, 10, 5)
	if err != nil || s.Last() != 14 || s.Series != SeriesForSlot(1) {
		t.Errorf("got %+v %v", s, err)
	}
}

func TestDecodeHeaderSectionErrors(t *testing.T) {
	base, _ := ExampleThread().Encode()
	cases := map[string]func(m map[uint64]any){
		"bad_r":         func(m map[uint64]any) { m[11] = "x" },
		"bad_ev":        func(m map[uint64]any) { m[14] = 5 },
		"bad_blk":       func(m map[uint64]any) { m[13] = []any{} },
		"mod_not_map":   func(m map[uint64]any) { m[12] = 1 },
		"mod_empty":     func(m map[uint64]any) { m[12] = map[uint64]any{} },
		"mod_bad_shape": func(m map[uint64]any) { m[12] = map[uint64]any{1: 5} },
		"mod_bad_sn": func(m map[uint64]any) {
			m[12] = map[uint64]any{1: []any{1, []any{[]any{0, map[uint64]any{}}}, "m-1", "co2@2.0"}}
		},
		"mod_bad_schema": func(m map[uint64]any) {
			m[12] = map[uint64]any{1: []any{1, []any{[]any{0, map[uint64]any{}}}, "M-1", "CO2"}}
		},
		"mod_empty_recs": func(m map[uint64]any) { m[12] = map[uint64]any{1: []any{1, []any{}, "M-1", "co2@2.0"}} },
		"bad_flags":      func(m map[uint64]any) { m[10] = "x" },
		"bad_nonce":      func(m map[uint64]any) { m[17] = -1 },
		"bad_dev_type":   func(m map[uint64]any) { m[1] = 5 },
		"bad_schema_id":  func(m map[uint64]any) { m[4] = "Envsense" },
		"bad_fw":         func(m map[uint64]any) { m[6] = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeHeader(mutate(t, base, mut)); err == nil {
				t.Fatal("must reject")
			}
		})
	}
	// A control upload with only mres is valid (spec 01 §3).
	ctl, _ := ExampleMoveBoundaryResult().Encode()
	if h, err := DecodeHeader(ctl); err != nil || !h.HasMRes || len(h.Spans) != 0 {
		t.Fatalf("control upload: %v %+v", err, h)
	}
}

func TestDecodeEnvelopeBodyError(t *testing.T) {
	b := mutate(t, mustEncode(t, ExampleThread()), func(m map[uint64]any) {
		m[13] = []any{44, []any{map[uint64]any{0: 9, 1: 0, 2: 0, 3: "fast", 4: []any{1}}}}
	})
	_, h, err := DecodeEnvelope(b)
	wantCode(t, err, "bad_body")
	if h == nil {
		t.Fatal("header must be returned with a body error, for quarantine")
	}
}

func TestEncodeDefaultsVersion(t *testing.T) {
	e := ExampleMinimal()
	e.V = 0
	b, err := e.Encode()
	if err != nil || e.V != Version {
		t.Fatal(err)
	}
	if _, err := DecodeHeader(b); err != nil {
		t.Fatal(err)
	}
}

// spec 02 §3: public key encoding and kid.
func TestPublicKeyRoundTrip(t *testing.T) {
	k := TestKey(3)
	raw, err := UncompressedPoint(&k.PublicKey)
	if err != nil || len(raw) != 65 {
		t.Fatal(err)
	}
	pub, err := ParseUncompressedPoint(raw)
	if err != nil || pub.X.Cmp(k.X) != 0 {
		t.Fatal(err)
	}
	_, err = ParseUncompressedPoint(raw[:64])
	wantCode(t, err, "bad_public_key")
	off := append([]byte{}, raw...)
	off[64] ^= 1
	_, err = ParseUncompressedPoint(off)
	wantCode(t, err, "bad_public_key")
}

func TestUnmarshalTrailing(t *testing.T) {
	var v any
	if err := Unmarshal([]byte{0x01, 0x02}, &v); err == nil {
		t.Fatal("trailing bytes")
	}
	if err := unmarshalStrict([]byte{0x01, 0x02}, &v); err == nil {
		t.Fatal("trailing bytes (strict)")
	}
}
