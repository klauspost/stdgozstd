// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package le

import (
	"bytes"
	"testing"
)

func TestLoad8(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03}
	if got := Load8(b, 0); got != 0x01 {
		t.Errorf("Load8(0) = %#x, want 0x01", got)
	}
	if got := Load8(b, 2); got != 0x03 {
		t.Errorf("Load8(2) = %#x, want 0x03", got)
	}
	if got := Load8(b, uint32(1)); got != 0x02 {
		t.Errorf("Load8(uint32(1)) = %#x, want 0x02", got)
	}
}

func TestLoad16(t *testing.T) {
	b := []byte{0x34, 0x12, 0x78, 0x56}
	if got := Load16(b, 0); got != 0x1234 {
		t.Errorf("Load16(0) = %#x, want 0x1234", got)
	}
	if got := Load16(b, 2); got != 0x5678 {
		t.Errorf("Load16(2) = %#x, want 0x5678", got)
	}
}

func TestLoad32(t *testing.T) {
	b := []byte{0x78, 0x56, 0x34, 0x12}
	if got := Load32(b, 0); got != 0x12345678 {
		t.Errorf("Load32(0) = %#x, want 0x12345678", got)
	}
}

func TestLoad64(t *testing.T) {
	b := []byte{0xEF, 0xCD, 0xAB, 0x90, 0x78, 0x56, 0x34, 0x12}
	if got := Load64(b, 0); got != 0x1234567890ABCDEF {
		t.Errorf("Load64(0) = %#x, want 0x1234567890ABCDEF", got)
	}
}

func TestStore16(t *testing.T) {
	b := make([]byte, 2)
	Store16(b, 0x1234)
	want := []byte{0x34, 0x12}
	if !bytes.Equal(b, want) {
		t.Errorf("Store16(0x1234) = %x, want %x", b, want)
	}
}

func TestStore32(t *testing.T) {
	b := make([]byte, 4)
	Store32(b, 0x12345678)
	want := []byte{0x78, 0x56, 0x34, 0x12}
	if !bytes.Equal(b, want) {
		t.Errorf("Store32(0x12345678) = %x, want %x", b, want)
	}
}

func TestStore64(t *testing.T) {
	b := make([]byte, 8)
	Store64(b, 0, uint64(0x1234567890ABCDEF))
	want := []byte{0xEF, 0xCD, 0xAB, 0x90, 0x78, 0x56, 0x34, 0x12}
	if !bytes.Equal(b, want) {
		t.Errorf("Store64 = %x, want %x", b, want)
	}
}

func TestRoundTrip(t *testing.T) {
	b := make([]byte, 8)

	Store16(b, 0xBEEF)
	if got := Load16(b, 0); got != 0xBEEF {
		t.Errorf("round-trip 16: got %#x, want 0xBEEF", got)
	}

	Store32(b, 0xDEADBEEF)
	if got := Load32(b, 0); got != 0xDEADBEEF {
		t.Errorf("round-trip 32: got %#x, want 0xDEADBEEF", got)
	}

	Store64(b, 0, 0xCAFEBABEDEADBEEF)
	if got := Load64(b, 0); got != 0xCAFEBABEDEADBEEF {
		t.Errorf("round-trip 64: got %#x, want 0xCAFEBABEDEADBEEF", got)
	}
}

func TestIndexTypes(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A}
	// Verify different index types all work
	_ = Load8(b, int(1))
	_ = Load8(b, int32(1))
	_ = Load8(b, int64(1))
	_ = Load8(b, uint(1))
	_ = Load8(b, uint32(1))
	_ = Load8(b, uint64(1))
	_ = Load16(b, int(1))
	_ = Load32(b, int(1))
	_ = Load64(b, int(1))
	Store64(b, int32(0), 0)
}
