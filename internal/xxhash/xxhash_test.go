// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xxhash

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func sum64(b []byte) uint64 {
	d := New()
	_, _ = d.Write(b)
	return d.Sum64()
}

// Known test vectors from the xxhash specification.
var testVectors = []struct {
	input string
	want  uint64
}{
	{"", 0xef46db3751d8e999},
	{"\x00", 0xe934a84adb052768},
	{"\x00\x00\x00\x00", 0x3aefa6fd5cf2deb4},
	{"a", 0xd24ec4f1a98c6e5b},
	{"ab", 0x65f708ca92d04a61},
	{"abc", 0x44bc2cf5ad770999},
	{"abcd", 0xde0327b0d25d92cc},
	{"abcdefgh", 0x3ad351775b4634b7},
	{"abcdefghijklmnop", 0x71ce8137ca2dd53d},
	{"abcdefghijklmnopqrstuvwxyz012345", 0xbf2cd639b4143b80},
	{"abcdefghijklmnopqrstuvwxyz0123456789", 0x64f23ecf1609b766},
}

func TestVectors(t *testing.T) {
	for _, tc := range testVectors {
		got := sum64([]byte(tc.input))
		if got != tc.want {
			t.Errorf("sum64(%q) = %#016x, want %#016x", tc.input, got, tc.want)
		}
	}
}

func TestStreamingVsOneShot(t *testing.T) {
	for _, tc := range testVectors {
		d := New()
		_, _ = d.Write([]byte(tc.input))
		got := d.Sum64()
		if got != tc.want {
			t.Errorf("streaming(%q) = %#016x, want %#016x", tc.input, got, tc.want)
		}
	}
}

func TestStreamingByteAtATime(t *testing.T) {
	input := []byte("abcdefghijklmnopqrstuvwxyz0123456789")
	want := sum64(input)
	d := New()
	for _, b := range input {
		_, _ = d.Write([]byte{b})
	}
	if got := d.Sum64(); got != want {
		t.Errorf("byte-at-a-time = %#016x, want %#016x", got, want)
	}
}

func TestZeroLengthWrite(t *testing.T) {
	d := New()
	n, err := d.Write(nil)
	if n != 0 || err != nil {
		t.Errorf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	n, err = d.Write([]byte{})
	if n != 0 || err != nil {
		t.Errorf("Write([]) = (%d, %v), want (0, nil)", n, err)
	}
	if got, want := d.Sum64(), sum64(nil); got != want {
		t.Errorf("empty = %#016x, want %#016x", got, want)
	}
}

func TestMultipleResets(t *testing.T) {
	d := New()
	for i := 0; i < 5; i++ {
		_, _ = d.Write([]byte("hello"))
		d.Reset()
	}
	if got, want := d.Sum64(), sum64(nil); got != want {
		t.Errorf("after resets = %#016x, want %#016x", got, want)
	}
	_, _ = d.Write([]byte("a"))
	if got, want := d.Sum64(), sum64([]byte("a")); got != want {
		t.Errorf("after reset+write = %#016x, want %#016x", got, want)
	}
}

func TestSum(t *testing.T) {
	d := New()
	_, _ = d.Write([]byte("abc"))
	b := d.Sum(nil)
	if len(b) != 8 {
		t.Fatalf("Sum length = %d, want 8", len(b))
	}
	got := binary.BigEndian.Uint64(b)
	want := d.Sum64()
	if got != want {
		t.Errorf("Sum = %#016x, want %#016x", got, want)
	}
}

func TestSizeBlockSize(t *testing.T) {
	d := New()
	if d.Size() != 8 {
		t.Errorf("Size = %d, want 8", d.Size())
	}
	if d.BlockSize() != 32 {
		t.Errorf("BlockSize = %d, want 32", d.BlockSize())
	}
}

func TestSumAppend(t *testing.T) {
	d := New()
	_, _ = d.Write([]byte("test"))
	prefix := []byte("prefix")
	result := d.Sum(prefix)
	if !bytes.HasPrefix(result, prefix) {
		t.Error("Sum did not preserve prefix")
	}
	if len(result) != len(prefix)+8 {
		t.Errorf("Sum result length = %d, want %d", len(result), len(prefix)+8)
	}
}
