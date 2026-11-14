package wire

// Relay result codes (spec 02 §4.2).
const (
	RelayAccepted = iota
	RelayDuplicate
	RelayBadSignature
	RelayUnknownDevice
	RelayUnclaimed
	RelayRevoked
	RelayThrottled
	RelayRetry
	RelayMalformed
	RelayTooLarge
)

// RelayRequest is the body of POST /v1/gw/relay (spec 02 §4.1).
type RelayRequest struct {
	V     uint64      `cbor:"0,keyasint"`
	GW    string      `cbor:"1,keyasint"`
	Items [][]byte    `cbor:"2,keyasint,omitempty"`
	Poll  []PollEntry `cbor:"3,keyasint,omitempty"`
	Own   []byte      `cbor:"4,keyasint,omitempty"`
	Seen  [][]any     `cbor:"5,keyasint,omitempty"`
}

// PollEntry is [dev, inc].
type PollEntry struct {
	_   struct{} `cbor:",toarray"`
	Dev string
	Inc uint32
}

// RelayReply is the response to a relay request (spec 02 §4.2).
type RelayReply struct {
	Now   uint64        `cbor:"0,keyasint"`
	Res   []RelayResult `cbor:"1,keyasint,omitempty"`
	Polls []RelayResult `cbor:"2,keyasint,omitempty"`
	Own   *Reply        `cbor:"3,keyasint,omitempty"`
	Next  uint32        `cbor:"4,keyasint"`
}

// RelayResult is the outcome for one batch or poll.
type RelayResult struct {
	Dev   string `cbor:"0,keyasint,omitempty"`
	Code  uint8  `cbor:"1,keyasint"`
	Reply []byte `cbor:"2,keyasint,omitempty"`
}

// DecodeRelayRequest decodes a relay request strictly.
func DecodeRelayRequest(data []byte) (*RelayRequest, error) {
	var r RelayRequest
	if err := unmarshalStrict(data, &r); err != nil {
		return nil, errf("bad_relay_request", "%v", err)
	}
	if r.V != 1 {
		return nil, errf("bad_version", "v=%d", r.V)
	}
	if !ValidSerial(r.GW) {
		return nil, errf("bad_serial", "gw %q", r.GW)
	}
	if len(r.Items) > 500 || len(r.Poll) > 500 {
		return nil, errf("too_many_items", "")
	}
	return &r, nil
}

// IngestRecord is the Redpanda message value (spec 06 §2.1).
type IngestRecord struct {
	V          uint64             `cbor:"0,keyasint"`
	ReceivedAt uint64             `cbor:"1,keyasint"`
	Node       string             `cbor:"2,keyasint"`
	Path       uint8              `cbor:"3,keyasint"`
	GW         string             `cbor:"4,keyasint,omitempty"`
	Dev        string             `cbor:"5,keyasint"`
	DeviceID   uint64             `cbor:"6,keyasint"`
	Batch      []byte             `cbor:"7,keyasint"`
	Known      map[uint16][]Range `cbor:"8,keyasint,omitempty"`
}
