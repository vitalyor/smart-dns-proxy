package model

import "testing"

func TestBundleRoundTrip(t *testing.T) {
	b := Bundle{
		NodeID: "n1", Name: "ingress-01", Role: "ingress",
		NodeCertPEM: "cert", NodeKeyPEM: "key", CACertPEM: "ca", PanelClientFP: "deadbeef",
	}
	got, err := DecodeBundle(b.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "n1" || got.Role != "ingress" || got.PanelClientFP != "deadbeef" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestDecodeBundleRejectsIncomplete(t *testing.T) {
	// Missing the pin is the dangerous case: without it a node would trust any
	// CA-signed client, defeating the whole point.
	b := Bundle{NodeID: "n1", NodeCertPEM: "c", NodeKeyPEM: "k", CACertPEM: "ca"}
	if _, err := DecodeBundle(b.Encode()); err == nil {
		t.Fatal("a bundle without a panel fingerprint must be rejected")
	}
	if _, err := DecodeBundle("not base64!!!"); err == nil {
		t.Fatal("garbage must be rejected")
	}
}
