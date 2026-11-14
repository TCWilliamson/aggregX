package wire

import "github.com/fxamacker/cbor/v2"

// Reply statuses (spec 01 §7, key 4).
const (
	StOK = iota
	StThrottled
	StUnclaimed
	StRevoked
	StRetryLater
	StRejected
)

// Mailbox item types (spec 01 §7.2).
const (
	MboxCfg          = 1
	MboxCmd          = 2
	MboxOTA          = 3
	MboxReportConfig = 4
	MboxMoveBoundary = 5
	MboxLive         = 6 // Later
	MboxKeyRollover  = 7
	MboxUnclaim      = 8
)

// Reply is the server's answer to an upload or poll (spec 01 §7).
type Reply struct {
	Now    uint64             `cbor:"0,keyasint"`
	Dev    string             `cbor:"1,keyasint,omitempty"`
	Inc    uint32             `cbor:"2,keyasint"`
	N      *uint32            `cbor:"3,keyasint,omitempty"`
	St     uint8              `cbor:"4,keyasint"`
	Ack    map[uint16]Ack     `cbor:"5,keyasint,omitempty"`
	Resend map[uint16][]Range `cbor:"6,keyasint,omitempty"`
	Next   uint32             `cbor:"7,keyasint"`
	Mbox   []MailboxItem      `cbor:"8,keyasint,omitempty"`
	More   bool               `cbor:"9,keyasint,omitempty"`
}

// Ack is [upto, [[from, to]...]]. Upto is -1 when nothing is contiguous yet.
type Ack struct {
	_      struct{} `cbor:",toarray"`
	Upto   int64
	Ranges []Range
}

// Range is an inclusive [from, to].
type Range struct {
	_    struct{} `cbor:",toarray"`
	From uint32
	To   uint32
}

// Covers reports whether a covers seq.
func (a Ack) Covers(seq uint32) bool {
	if int64(seq) <= a.Upto {
		return true
	}
	for _, r := range a.Ranges {
		if seq >= r.From && seq <= r.To {
			return true
		}
	}
	return false
}

// MailboxItem is one server-to-device instruction (spec 01 §7.2).
type MailboxItem struct {
	ID   uint64          `cbor:"0,keyasint"`
	Type uint8           `cbor:"1,keyasint"`
	Exp  uint64          `cbor:"2,keyasint"`
	Body cbor.RawMessage `cbor:"3,keyasint"`
	Prio uint8           `cbor:"4,keyasint,omitempty"`
}

// CfgBody is the body of a cfg item.
type CfgBody struct {
	Ver   uint64         `cbor:"0,keyasint"`
	Hash  []byte         `cbor:"1,keyasint"`
	Set   map[uint32]any `cbor:"2,keyasint,omitempty"`
	Unset []uint32       `cbor:"3,keyasint,omitempty"`
}

// MoveBoundaryBody is the body of a move_boundary item.
type MoveBoundaryBody struct {
	MoveID uint64 `cbor:"0,keyasint"`
}

// Encode encodes the reply deterministically.
func (r *Reply) Encode() ([]byte, error) { return Marshal(r) }

// DecodeReply decodes a reply strictly.
func DecodeReply(data []byte) (*Reply, error) {
	var r Reply
	if err := unmarshalStrict(data, &r); err != nil {
		return nil, errf("bad_reply", "%v", err)
	}
	return &r, nil
}
