package wire

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"math/big"

	"github.com/fxamacker/cbor/v2"
)

// The worked examples of spec 01 §11. Tests check that they encode, decode and
// round-trip, and the vectors in spec/vectors are generated from them.

func u32(v uint32) *uint32 { return &v }

// ExampleMinimal is spec 01 §11.1: a direct device sending two main records.
func ExampleMinimal() *Envelope { // §11.1
	return &Envelope{
		V: 1, Dev: "SN-00042", Inc: 2876543210, Boot: 17, Schema: "envsense@3.2", Comp: 0,
		FW: "2.4.1", Cfg: []byte{0xA9, 0x1F, 0x03, 0xC2, 0x4D, 0x5E, 0x6F, 0x70},
		TB: TBSynced, T0: 1794614900123,
		R: &RecordSection{First: 18233, Records: []Record{
			{Dt: 0, Values: map[uint16]any{1: float32(21.4), 2: uint64(45)}},
			{Dt: 5000, Values: map[uint16]any{1: float32(21.5), 2: uint64(45)}},
		}},
	}
}

// ExampleThread is spec 01 §11.2: a Thread device with every section kind.
func ExampleThread() *Envelope { // §11.2
	return &Envelope{
		V: 1, Dev: "SN-00107", Inc: 912345678, Boot: 4, Schema: "envsense@3.2", Comp: 7,
		FW: "2.4.1", Cfg: []byte{0x00, 0x11, 0x22, 0x33, 0xAA, 0xBB, 0xCC, 0xDD},
		TB: TBSynced, T0: 1794614903000, N: u32(3141592653),
		R: &RecordSection{First: 5120, Records: []Record{
			{Dt: 0, Values: map[uint16]any{1: float32(19.75), 32768: uint64(3012)}},
		}},
		Mod: map[uint8]*ModSection{1: {First: 901, SN: "M-7781", Schema: "co2@2.0",
			Records: []Record{{Dt: 0, Values: map[uint16]any{1: uint64(612)}}}}},
		Blk: &BlockSection{First: 44, Blocks: []Block{
			{Ch: 9, Slot: 0, Dt: 120, Hz: 100, Values: []Number{0.25, -0.125, 0.5}},
		}},
		Ev: &EventSection{First: 300, Events: []Event{
			{Type: 1, Dt: 0, Attrs: map[uint16]any{1: uint64(4)}},
		}},
	}
}

// ExampleReply is spec 01 §11.3: a reply with acks, a resend request and a cfg mailbox item.
func ExampleReply() *Reply { // §11.3
	body, _ := Marshal(CfgBody{Ver: 12, Hash: []byte{0x5E, 0x0C, 0x1D, 0x2E, 0x3F, 0x40, 0x51, 0x62},
		Set: map[uint32]any{3: uint64(5)}})
	return &Reply{
		Now: 1794614960500, Inc: 2876543210, St: StOK,
		Ack:    map[uint16]Ack{0: {Upto: 18233, Ranges: []Range{{From: 18300, To: 18320}}}},
		Resend: map[uint16][]Range{0: {{From: 18234, To: 18299}}},
		Next:   60,
		Mbox:   []MailboxItem{{ID: 19, Type: MboxCfg, Exp: 1794618560500, Body: cbor.RawMessage(body)}},
	}
}

// ExampleUptime is spec 01 §11.4: an envelope on the uptime timebase after a reboot.
func ExampleUptime() *Envelope { // §11.4
	return &Envelope{
		V: 1, Dev: "SN-00107", Inc: 912345678, Boot: 5, Schema: "envsense@3.2", Comp: 7,
		FW: "2.4.2", Cfg: []byte{0x00, 0x11, 0x22, 0x33, 0xAA, 0xBB, 0xCC, 0xDD},
		TB: TBUptime, T0: 2150, N: u32(77),
		R: &RecordSection{First: 6000, Records: []Record{
			{Dt: 0, Values: map[uint16]any{1: float32(19.5)}},
			{Dt: 5000, Values: map[uint16]any{1: float32(19.5)}},
		}},
		Ev: &EventSection{First: 301, Events: []Event{
			{Type: 1, Dt: 0, Attrs: map[uint16]any{1: uint64(4)}},
			{Type: 2, Dt: 0, Attrs: map[uint16]any{1: uint64(0), 2: uint64(5121), 3: uint64(5999)}},
		}},
	}
}

// ExampleMoveBoundaryResult is spec 01 §11.5: a control upload carrying only a move_boundary result.
func ExampleMoveBoundaryResult() *Envelope { // §11.5
	return &Envelope{
		V: 1, Dev: "SN-00107", Inc: 912345678, Boot: 5, Schema: "envsense@3.2", Comp: 7,
		FW: "2.4.2", Cfg: []byte{0x00, 0x11, 0x22, 0x33, 0xAA, 0xBB, 0xCC, 0xDD},
		TB: TBSynced, T0: 1794700000000, N: u32(78),
		MRes: []MailboxResult{{ID: 31, Status: MResApplied,
			Detail: map[uint64]uint64{0: 6230, 1: 302, 2: 45, 17: 1022}}},
	}
}

// TestKey returns a fixed P-256 key for vectors and examples. It must never be
// accepted outside tests (spec 07 §2).
func TestKey(n int64) *ecdsa.PrivateKey {
	d := new(big.Int).SetInt64(0x1D0700000000 + n)
	k := &ecdsa.PrivateKey{D: d}
	k.Curve = elliptic.P256()
	k.X, k.Y = elliptic.P256().ScalarBaseMult(d.Bytes())
	return k
}
