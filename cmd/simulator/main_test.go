package main

import (
	"testing"

	"iotp/internal/wire"
)

// spec 02 §4.1: the smoke request is a valid relay request whose items verify.
func TestRelayBody(t *testing.T) {
	b, err := relayBody("GW-00001", 3)
	if err != nil {
		t.Fatal(err)
	}
	req, err := wire.DecodeRelayRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Items) != 3 {
		t.Fatalf("want 3 items, got %d", len(req.Items))
	}
	for i, item := range req.Items {
		m, err := wire.ParseSign1(item)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Verify(&wire.TestKey(int64(i)).PublicKey, wire.AADBatch); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}
	for _, n := range []int{0, wire.MaxRelayItems + 1} {
		if _, err := relayBody("GW-00001", n); err == nil {
			t.Fatalf("n=%d must be rejected", n)
		}
	}
}
