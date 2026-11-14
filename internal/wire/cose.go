package wire

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"

	"github.com/fxamacker/cbor/v2"
)

// External AAD values (spec 02 §3, §3.5).
var (
	AADBatch = []byte("iotp-batch-v1")
	AADReply = []byte("iotp-reply-v1")
)

const (
	coseTagSign1 = 18
	algES256     = -7
)

// KID returns the 4-byte key id: the first 4 bytes of SHA-256 over the
// 65-byte uncompressed public key (spec 02 §3).
func KID(pub *ecdsa.PublicKey) ([]byte, error) {
	raw, err := UncompressedPoint(pub)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:4], nil
}

// UncompressedPoint returns the 65-byte SEC1 uncompressed encoding of pub.
func UncompressedPoint(pub *ecdsa.PublicKey) ([]byte, error) {
	k, err := pub.ECDH()
	if err != nil {
		return nil, errf("bad_public_key", "%v", err)
	}
	return k.Bytes(), nil
}

// ParseUncompressedPoint parses a 65-byte P-256 public key.
func ParseUncompressedPoint(b []byte) (*ecdsa.PublicKey, error) {
	if len(b) != 65 || b[0] != 4 {
		return nil, errf("bad_public_key", "want 65-byte uncompressed point")
	}
	x := new(big.Int).SetBytes(b[1:33])
	y := new(big.Int).SetBytes(b[33:])
	if !elliptic.P256().IsOnCurve(x, y) {
		return nil, errf("bad_public_key", "point not on curve")
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
}

// protectedHeader returns the exact protected header bytes {1: -7, 4: kid}.
func protectedHeader(kid []byte) []byte {
	// A2 01 26 04 44 <kid>
	return append([]byte{0xA2, 0x01, 0x26, 0x04, 0x44}, kid...)
}

type sign1 struct {
	_           struct{} `cbor:",toarray"`
	Protected   []byte
	Unprotected map[any]any
	Payload     []byte
	Signature   []byte
}

func sigStructure(protected, aad, payload []byte) ([]byte, error) {
	return Marshal([]any{"Signature1", protected, aad, payload})
}

// Sign1 produces a COSE_Sign1 (tag 18) over payload with the profile of spec 02 §3.
func Sign1(key *ecdsa.PrivateKey, payload, aad []byte) ([]byte, error) {
	kid, err := KID(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	prot := protectedHeader(kid)
	tbs, err := sigStructure(prot, aad, payload)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(tbs)
	r, s, err := ecdsa.Sign(rand.Reader, key, h[:])
	if err != nil {
		return nil, fmt.Errorf("wire: sign: %w", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	msg := sign1{Protected: prot, Unprotected: map[any]any{}, Payload: payload, Signature: sig}
	return Marshal(cbor.Tag{Number: coseTagSign1, Content: msg})
}

// Sign1Message is a structurally valid, not yet verified, COSE_Sign1.
type Sign1Message struct {
	KID       []byte
	Protected []byte
	Payload   []byte
	Signature []byte
}

// ParseSign1 strictly decodes a COSE_Sign1 per spec 02 §3.1 step 1. It does
// not verify the signature.
func ParseSign1(data []byte) (*Sign1Message, error) {
	var tag cbor.RawTag
	if err := Unmarshal(data, &tag); err != nil {
		return nil, errf("cose_not_tagged", "%v", err)
	}
	if tag.Number != coseTagSign1 {
		return nil, errf("cose_wrong_tag", "tag %d", tag.Number)
	}
	var arr []cbor.RawMessage
	if err := decMode.Unmarshal(tag.Content, &arr); err != nil || len(arr) != 4 {
		return nil, errf("cose_shape", "want 4-element array")
	}
	var m Sign1Message
	if err := decMode.Unmarshal(arr[0], &m.Protected); err != nil {
		return nil, errf("cose_protected", "%v", err)
	}
	var unprot map[any]any
	if err := decMode.Unmarshal(arr[1], &unprot); err != nil || len(unprot) != 0 {
		return nil, errf("cose_unprotected", "unprotected header must be an empty map")
	}
	// Payload must be attached: a bstr, never nil.
	if len(arr[2]) == 1 && arr[2][0] == 0xF6 {
		return nil, errf("cose_detached", "payload must be attached")
	}
	if err := decMode.Unmarshal(arr[2], &m.Payload); err != nil {
		return nil, errf("cose_payload", "%v", err)
	}
	if err := decMode.Unmarshal(arr[3], &m.Signature); err != nil || len(m.Signature) != 64 {
		return nil, errf("cose_signature", "signature must be a 64-byte bstr")
	}
	var hdr map[int64]cbor.RawMessage
	if err := Unmarshal(m.Protected, &hdr); err != nil {
		return nil, errf("cose_protected", "%v", err)
	}
	if len(hdr) != 2 {
		return nil, errf("cose_protected", "protected header must be exactly {1, 4}")
	}
	var alg int64
	if err := decMode.Unmarshal(hdr[1], &alg); err != nil || alg != algES256 {
		return nil, errf("cose_alg", "only ES256 (-7) is accepted")
	}
	if err := decMode.Unmarshal(hdr[4], &m.KID); err != nil || len(m.KID) != 4 {
		return nil, errf("cose_kid", "kid must be a 4-byte bstr")
	}
	// Byte-exact header: rejects non-deterministic encodings of the same map.
	if !bytes.Equal(m.Protected, protectedHeader(m.KID)) {
		return nil, errf("cose_protected", "protected header not in canonical form")
	}
	return &m, nil
}

// Verify checks the signature with pub and aad. The caller must already have
// chosen pub by device identity and m.KID (spec 02 §3.1 steps 2-3).
func (m *Sign1Message) Verify(pub *ecdsa.PublicKey, aad []byte) error {
	kid, err := KID(pub)
	if err != nil {
		return err
	}
	if !bytes.Equal(kid, m.KID) {
		return errf("cose_kid_mismatch", "")
	}
	tbs, err := sigStructure(m.Protected, aad, m.Payload)
	if err != nil {
		return err
	}
	h := sha256.Sum256(tbs)
	r := new(big.Int).SetBytes(m.Signature[:32])
	s := new(big.Int).SetBytes(m.Signature[32:])
	if !ecdsa.Verify(pub, h[:], r, s) {
		return errf("cose_bad_signature", "")
	}
	return nil
}
