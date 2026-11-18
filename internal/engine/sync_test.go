package engine

import (
	"bytes"
	"testing"
)

func TestSealOpen(t *testing.T) {
	msg := []byte(`{"vips":[]}`)
	sealed, err := Seal("secret-a", msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, msg) {
		t.Fatal("payload is not encrypted")
	}
	got, err := Open("secret-a", sealed)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := Open("secret-b", sealed); err == nil {
		t.Fatal("opened with the wrong secret")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := Open("secret-a", sealed); err == nil {
		t.Fatal("opened a tampered payload")
	}
}
