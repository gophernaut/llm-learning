package iotest

import (
	"testing"
)

func TestTruncateWriter(t *testing.T) {
	w := io.NewWriter(strings.NewReader(""))
	tr := TruncateWriter(w, 10)

	// Test writing 15 bytes
	_, err := tr.Write([]byte("1234567890abcdefgh"))
	if err == nil {
		t.Errorf("Expected error when writing more than 10 bytes")
	}

	// Test writing 5 bytes
	_, err = tr.Write([]byte("ijklmnop"))
	if err != nil {
		t.Errorf("Expected no error when writing 5 bytes")
	}

	// Test writing 0 bytes
	_, err = tr.Write([]byte{})
	if err != nil {
		t.Errorf("Expected no error when writing 0 bytes")
	}

	// Test writing 10 bytes
	_, err = tr.Write([]byte("1234567890"))
	if err != nil {
		t.Errorf("Expected no error when writing 10 bytes")
	}

	// Test writing 11 bytes
	_, err = tr.Write([]byte("abcdefgh"))
	if err == nil {
		t.Errorf("Expected error when writing 11 bytes")
	}
}