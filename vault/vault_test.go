package vault

import (
	"bytes"
	"testing"
)

func TestRoundTripAndAdditionalData(t *testing.T) {
	v, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := v.Seal([]byte("secret"), []byte("record-a"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Open(sealed, []byte("record-a"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "secret" {
		t.Fatalf("Open() = %q, want secret", got)
	}
	if _, err := v.Open(sealed, []byte("record-b")); err == nil {
		t.Fatal("Open() with different additional data succeeded")
	}
}
