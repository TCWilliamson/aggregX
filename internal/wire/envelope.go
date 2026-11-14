package wire

import (
	"fmt"
	"math"
	"regexp"

	"github.com/fxamacker/cbor/v2"
)

// Envelope version defined by spec 01.
const Version = 1

// Timebases (spec 01 §3, key 8).
const (
	TBSynced = 0
	TBUptime = 1
)

// Envelope flags (spec 01 §3, key 10).
const (
	FlagBackfill = 1 << 0
	FlagMore     = 1 << 1
)

// Series ids (spec 01 §4).
const (
	SeriesMain    = 0
	SeriesEvents  = 1
	SeriesBlocks  = 2
	SeriesModBase = 16
)

// SeriesForSlot returns the series id of module slot s (1..239).
func SeriesForSlot(s uint8) uint16 { return SeriesModBase + uint16(s) }

// Limits (spec 01 §10).
const (
	MaxDirectEnvelope = 64 << 10
	MaxThreadEnvelope = 4 << 10
	MaxItemsPerSect   = 1000
	MaxBlockValues    = 4096
	MaxAckRanges      = 16
	MaxResendRanges   = 8
	MaxMailboxPerRply = 8
	MaxSlot           = 239
)

var (
	serialRE = regexp.MustCompile(`^[A-Z0-9-]{1,32}$`)
	schemaRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}@[0-9]+\.[0-9]+$`)
)

// ValidSerial reports whether s is a valid device, gateway or module serial.
func ValidSerial(s string) bool { return serialRE.MatchString(s) }

// ValidSchemaID reports whether s is a valid name@major.minor identifier.
func ValidSchemaID(s string) bool { return schemaRE.MatchString(s) }

// Envelope is one upload (spec 01 §3).
type Envelope struct {
	V      uint64                `cbor:"0,keyasint"`
	Dev    string                `cbor:"1,keyasint"`
	Inc    uint32                `cbor:"2,keyasint"`
	Boot   uint32                `cbor:"3,keyasint"`
	Schema string                `cbor:"4,keyasint"`
	Comp   uint64                `cbor:"5,keyasint"`
	FW     string                `cbor:"6,keyasint"`
	Cfg    []byte                `cbor:"7,keyasint"`
	TB     uint8                 `cbor:"8,keyasint"`
	T0     uint64                `cbor:"9,keyasint"`
	Flags  uint64                `cbor:"10,keyasint,omitempty"`
	R      *RecordSection        `cbor:"11,keyasint,omitempty"`
	Mod    map[uint8]*ModSection `cbor:"12,keyasint,omitempty"`
	Blk    *BlockSection         `cbor:"13,keyasint,omitempty"`
	Ev     *EventSection         `cbor:"14,keyasint,omitempty"`
	MRes   []MailboxResult       `cbor:"15,keyasint,omitempty"`
	RCfg   *ReportedConfig       `cbor:"16,keyasint,omitempty"`
	N      *uint32               `cbor:"17,keyasint,omitempty"`
}

// Record is one sample of periodic data: [dt, {channel: value}] (spec 01 §4.1).
// Values are int64/uint64, float32/float64, bool, string, or []float64 (geo).
type Record struct {
	_      struct{} `cbor:",toarray"`
	Dt     uint64
	Values map[uint16]any
}

// RecordSection is [first_seq, [record...]].
type RecordSection struct {
	_       struct{} `cbor:",toarray"`
	First   uint32
	Records []Record
}

// ModSection is [first_seq, [record...], sn, schema] (spec 01 §4.2).
type ModSection struct {
	_       struct{} `cbor:",toarray"`
	First   uint32
	Records []Record
	SN      string
	Schema  string
}

// Block is a run of regularly sampled values (spec 01 §4.3).
type Block struct {
	Ch     uint16   `cbor:"0,keyasint"`
	Slot   uint8    `cbor:"1,keyasint"`
	Dt     uint64   `cbor:"2,keyasint"`
	Hz     Number   `cbor:"3,keyasint"`
	Values []Number `cbor:"4,keyasint"`
}

// BlockSection is [first_seq, [block...]].
type BlockSection struct {
	_      struct{} `cbor:",toarray"`
	First  uint32
	Blocks []Block
}

// Event is an immutable event or completed session (spec 01 §4.4).
type Event struct {
	Type  uint16         `cbor:"0,keyasint"`
	Dt    uint64         `cbor:"1,keyasint"`
	Dur   *uint64        `cbor:"2,keyasint,omitempty"`
	Attrs map[uint16]any `cbor:"3,keyasint,omitempty"`
	Slot  *uint8         `cbor:"4,keyasint,omitempty"`
}

// EventSection is [first_seq, [event...]].
type EventSection struct {
	_      struct{} `cbor:",toarray"`
	First  uint32
	Events []Event
}

// ReportedConfig is the full reported config (spec 01 key 16).
type ReportedConfig struct {
	Hash   []byte         `cbor:"0,keyasint"`
	Config map[uint32]any `cbor:"1,keyasint"`
}

// Mailbox result statuses (spec 01 §7.3).
const (
	MResReceived = iota
	MResApplied
	MResFailed
	MResRejected
	MResDeferred
	MResAlreadyProcessed
)

// MailboxResult is [id, status, detail?].
type MailboxResult struct {
	ID     uint64
	Status uint8
	Detail any // nil = omitted
}

func (m MailboxResult) MarshalCBOR() ([]byte, error) {
	if m.Detail == nil {
		return encMode.Marshal([]any{m.ID, m.Status})
	}
	return encMode.Marshal([]any{m.ID, m.Status, m.Detail})
}

func (m *MailboxResult) UnmarshalCBOR(data []byte) error {
	var raw []cbor.RawMessage
	if err := decMode.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) < 2 || len(raw) > 3 {
		return errf("mres_shape", "mres must have 2 or 3 elements, got %d", len(raw))
	}
	if err := decMode.Unmarshal(raw[0], &m.ID); err != nil {
		return err
	}
	if err := decMode.Unmarshal(raw[1], &m.Status); err != nil {
		return err
	}
	m.Detail = nil
	if len(raw) == 3 {
		return decMode.Unmarshal(raw[2], &m.Detail)
	}
	return nil
}

// Number is a CBOR int or float (block rates and values).
type Number float64

func (n Number) MarshalCBOR() ([]byte, error) {
	f := float64(n)
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return encMode.Marshal(int64(f))
	}
	return encMode.Marshal(f)
}

func (n *Number) UnmarshalCBOR(data []byte) error {
	var v any
	if err := decMode.Unmarshal(data, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case int64:
		*n = Number(x)
	case uint64:
		*n = Number(x)
	case float64:
		*n = Number(x)
	case float32:
		*n = Number(x)
	default:
		return errf("not_number", "%T", v)
	}
	return nil
}

// --------------------------------------------------------------------------
// Header-only decoding, used by ingest (spec 01 §3.1, §6).

// SectionSpan is the first sequence number and item count of one section.
type SectionSpan struct {
	Series uint16
	First  uint32
	Count  uint32
}

// Last returns the last sequence number in the span.
func (s SectionSpan) Last() uint32 { return s.First + s.Count - 1 }

// Header is everything ingest needs from an envelope, without decoding values.
type Header struct {
	V       uint64
	Dev     string
	Inc     uint32
	Boot    uint32
	Schema  string
	Comp    uint64
	FW      string
	Cfg     []byte
	TB      uint8
	T0      uint64
	Flags   uint64
	N       *uint32
	Spans   []SectionSpan
	HasMRes bool
	HasRCfg bool
}

var requiredKeys = []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

type rawSection struct {
	_     struct{} `cbor:",toarray"`
	First uint32
	Items []cbor.RawMessage
}

type rawModSection struct {
	_      struct{} `cbor:",toarray"`
	First  uint32
	Items  []cbor.RawMessage
	SN     string
	Schema string
}

// DecodeHeader parses and validates the envelope header and section spans.
// It never decodes channel values. Errors are *Error with a stable Code.
func DecodeHeader(data []byte) (*Header, error) {
	var m map[uint64]cbor.RawMessage
	if err := Unmarshal(data, &m); err != nil {
		return nil, errf("not_cbor_map", "%v", err)
	}
	for k := range m {
		if k > 17 {
			return nil, errf("unknown_key", "envelope key %d", k)
		}
	}
	for _, k := range requiredKeys {
		if _, ok := m[k]; !ok {
			return nil, errf("missing_key", "envelope key %d", k)
		}
	}
	h := &Header{}
	dec := func(k uint64, v any) error {
		if err := decMode.Unmarshal(m[k], v); err != nil {
			return errf("bad_type", "envelope key %d: %v", k, err)
		}
		return nil
	}
	for _, f := range []struct {
		k uint64
		v any
	}{{0, &h.V}, {1, &h.Dev}, {2, &h.Inc}, {3, &h.Boot}, {4, &h.Schema}, {5, &h.Comp},
		{6, &h.FW}, {7, &h.Cfg}, {8, &h.TB}, {9, &h.T0}} {
		if err := dec(f.k, f.v); err != nil {
			return nil, err
		}
	}
	if _, ok := m[10]; ok {
		if err := dec(10, &h.Flags); err != nil {
			return nil, err
		}
	}
	if _, ok := m[17]; ok {
		h.N = new(uint32)
		if err := dec(17, h.N); err != nil {
			return nil, err
		}
	}
	_, h.HasMRes = m[15]
	_, h.HasRCfg = m[16]

	if raw, ok := m[11]; ok {
		s, err := spanOf(raw, SeriesMain)
		if err != nil {
			return nil, err
		}
		h.Spans = append(h.Spans, s)
	}
	if raw, ok := m[14]; ok {
		s, err := spanOf(raw, SeriesEvents)
		if err != nil {
			return nil, err
		}
		h.Spans = append(h.Spans, s)
	}
	if raw, ok := m[13]; ok {
		s, err := spanOf(raw, SeriesBlocks)
		if err != nil {
			return nil, err
		}
		h.Spans = append(h.Spans, s)
	}
	if raw, ok := m[12]; ok {
		var mods map[uint64]cbor.RawMessage
		if err := decMode.Unmarshal(raw, &mods); err != nil {
			return nil, errf("bad_type", "mod: %v", err)
		}
		if len(mods) == 0 {
			return nil, errf("empty_section", "mod")
		}
		for slot, sraw := range mods {
			if slot < 1 || slot > MaxSlot {
				return nil, errf("bad_slot", "slot %d", slot)
			}
			var ms rawModSection
			if err := decMode.Unmarshal(sraw, &ms); err != nil {
				return nil, errf("bad_type", "mod[%d]: %v", slot, err)
			}
			if !ValidSerial(ms.SN) {
				return nil, errf("bad_serial", "mod[%d] sn %q", slot, ms.SN)
			}
			if !ValidSchemaID(ms.Schema) {
				return nil, errf("bad_schema_id", "mod[%d] schema %q", slot, ms.Schema)
			}
			sp, err := checkSpan(SeriesForSlot(uint8(slot)), ms.First, len(ms.Items))
			if err != nil {
				return nil, err
			}
			h.Spans = append(h.Spans, sp)
		}
	}
	if err := h.validate(); err != nil {
		return nil, err
	}
	return h, nil
}

func spanOf(raw cbor.RawMessage, series uint16) (SectionSpan, error) {
	var s rawSection
	if err := decMode.Unmarshal(raw, &s); err != nil {
		return SectionSpan{}, errf("bad_type", "series %d section: %v", series, err)
	}
	return checkSpan(series, s.First, len(s.Items))
}

func checkSpan(series uint16, first uint32, n int) (SectionSpan, error) {
	if n == 0 {
		return SectionSpan{}, errf("empty_section", "series %d", series)
	}
	if n > MaxItemsPerSect {
		return SectionSpan{}, errf("section_too_large", "series %d has %d items", series, n)
	}
	if uint64(first)+uint64(n)-1 > math.MaxUint32 {
		return SectionSpan{}, errf("seq_overflow", "series %d", series)
	}
	return SectionSpan{Series: series, First: first, Count: uint32(n)}, nil
}

func (h *Header) validate() error {
	switch {
	case h.V != Version:
		return errf("bad_version", "v=%d", h.V)
	case !ValidSerial(h.Dev):
		return errf("bad_serial", "dev %q", h.Dev)
	case h.Inc == 0:
		return errf("bad_inc", "inc must be non-zero")
	case !ValidSchemaID(h.Schema):
		return errf("bad_schema_id", "%q", h.Schema)
	case len(h.FW) == 0 || len(h.FW) > 32:
		return errf("bad_fw", "%q", h.FW)
	case len(h.Cfg) != 8:
		return errf("bad_cfg", "cfg must be 8 bytes, got %d", len(h.Cfg))
	case h.TB != TBSynced && h.TB != TBUptime:
		return errf("bad_tb", "tb=%d", h.TB)
	case len(h.Spans) == 0 && !h.HasMRes && !h.HasRCfg:
		return errf("empty_envelope", "no samples, mres or rcfg")
	}
	return nil
}

// --------------------------------------------------------------------------
// Full decoding, used by workers.

// DecodeEnvelope validates the header and then decodes the whole envelope.
// Unknown keys inside body structures are errors (workers quarantine them).
func DecodeEnvelope(data []byte) (*Envelope, *Header, error) {
	h, err := DecodeHeader(data)
	if err != nil {
		return nil, nil, err
	}
	var e Envelope
	if err := unmarshalStrict(data, &e); err != nil {
		return nil, h, errf("bad_body", "%v", err)
	}
	if err := e.validateBody(); err != nil {
		return nil, h, err
	}
	return &e, h, nil
}

func (e *Envelope) validateBody() error {
	if e.Blk != nil {
		for i, b := range e.Blk.Blocks {
			if b.Hz <= 0 || b.Hz > 20000 {
				return errf("bad_block", "block %d hz=%v", i, float64(b.Hz))
			}
			if len(b.Values) == 0 || len(b.Values) > MaxBlockValues {
				return errf("bad_block", "block %d has %d values", i, len(b.Values))
			}
			if b.Slot > MaxSlot {
				return errf("bad_slot", "block %d slot %d", i, b.Slot)
			}
		}
	}
	check := func(recs []Record) error {
		for i, r := range recs {
			for ch, v := range r.Values {
				if v == nil {
					return errf("null_value", "record %d channel %d", i, ch)
				}
			}
		}
		return nil
	}
	if e.R != nil {
		if err := check(e.R.Records); err != nil {
			return err
		}
	}
	for _, m := range e.Mod {
		if err := check(m.Records); err != nil {
			return err
		}
	}
	return nil
}

// Encode encodes an envelope deterministically. It does not validate.
func (e *Envelope) Encode() ([]byte, error) {
	if e.V == 0 {
		e.V = Version
	}
	b, err := Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("wire: encode envelope: %w", err)
	}
	return b, nil
}
