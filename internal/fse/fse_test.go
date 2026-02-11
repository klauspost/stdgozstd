// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fse

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

func TestCompressBasic(t *testing.T) {
	// Repetitive input should compress well.
	input := bytes.Repeat([]byte("abcabcabcabc"), 100)
	s := &Scratch{}
	out, err := Compress(input, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= len(input) {
		t.Errorf("compressed %d >= original %d", len(out), len(input))
	}
}

func TestCompressRLE(t *testing.T) {
	input := bytes.Repeat([]byte{0x42}, 100)
	s := &Scratch{}
	_, err := Compress(input, s)
	if !errors.Is(err, ErrUseRLE) {
		t.Errorf("expected ErrUseRLE, got %v", err)
	}
}

func TestCompressIncompressible(t *testing.T) {
	// Single byte
	_, err := Compress([]byte{1}, nil)
	if !errors.Is(err, ErrIncompressible) {
		t.Errorf("expected ErrIncompressible for single byte, got %v", err)
	}

	// Empty
	_, err = Compress(nil, nil)
	if !errors.Is(err, ErrIncompressible) {
		t.Errorf("expected ErrIncompressible for nil, got %v", err)
	}
}

func TestCompressRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	input := make([]byte, 1000)
	rng.Read(input)
	s := &Scratch{}
	_, err := Compress(input, s)
	// Random data is usually incompressible — that's fine.
	if err != nil && !errors.Is(err, ErrIncompressible) {
		t.Fatal(err)
	}
}

func TestCompressScratchReuse(t *testing.T) {
	s := &Scratch{}
	for i := range 5 {
		input := bytes.Repeat([]byte("hello world test data for fse"), 50+i*10)
		out, err := Compress(input, s)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if len(out) >= len(input) {
			t.Fatalf("iteration %d: no compression", i)
		}
	}
}

func TestCompressSkewed(t *testing.T) {
	// Heavily skewed distribution: mostly 'a' with a few others.
	input := make([]byte, 1000)
	for i := range input {
		if i%100 == 0 {
			input[i] = byte(i % 10)
		} else {
			input[i] = 'a'
		}
	}
	s := &Scratch{}
	out, err := Compress(input, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= len(input) {
		t.Error("skewed distribution should compress well")
	}
}

func TestHistogram(t *testing.T) {
	s := &Scratch{}
	h := s.Histogram()
	if len(h) != 256 {
		t.Fatalf("histogram length = %d, want 256", len(h))
	}

	s.HistogramFinished(10, 50)
	if s.symbolLen != 11 {
		t.Errorf("symbolLen = %d, want 11", s.symbolLen)
	}
	if s.maxCount != 50 {
		t.Errorf("maxCount = %d, want 50", s.maxCount)
	}
}

func TestTableStep(t *testing.T) {
	// Known property: step should be odd for power-of-2 table sizes.
	for log := uint(5); log <= 12; log++ {
		size := uint32(1) << log
		step := tableStep(size)
		if step%2 == 0 {
			t.Errorf("tableStep(%d) = %d, should be odd", size, step)
		}
	}
}

func TestCompressDecompressRoundTrip(t *testing.T) {
	input := bytes.Repeat([]byte("abcdefghijklmnop"), 200)
	cs := &Scratch{}
	compressed, err := Compress(input, cs)
	if err != nil {
		t.Fatal(err)
	}

	ds := &Scratch{}
	decompressed, err := Decompress(compressed, ds)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decompressed, input) {
		t.Errorf("round-trip mismatch: got %d bytes, want %d bytes", len(decompressed), len(input))
	}
}

func TestHighBits(t *testing.T) {
	tests := []struct {
		val  uint32
		want uint32
	}{
		{1, 0},
		{2, 1},
		{3, 1},
		{4, 2},
		{255, 7},
		{256, 8},
	}
	for _, tc := range tests {
		got := highBits(tc.val)
		if got != tc.want {
			t.Errorf("highBits(%d) = %d, want %d", tc.val, got, tc.want)
		}
	}
}
