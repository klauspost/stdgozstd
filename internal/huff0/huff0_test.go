// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package huff0

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestCompress1XDecompress1XRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(0))
	for _, size := range []int{64, 256, 1024, 4096, 65536} {
		input := make([]byte, size)
		for i := range input {
			// Skewed distribution for compressibility.
			input[i] = byte(rng.Intn(32))
		}
		s := &Scratch{}
		compressed, reused, err := Compress1X(input, s)
		if err != nil {
			t.Fatalf("size=%d: Compress1X: %v", size, err)
		}
		_ = reused

		s2, remain, err := ReadTable(compressed, nil)
		if err != nil {
			t.Fatalf("size=%d: ReadTable: %v", size, err)
		}

		dec := s2.Decoder()
		decompressed, err := dec.Decompress1X(make([]byte, 0, len(input)), remain)
		if err != nil {
			t.Fatalf("size=%d: Decompress1X: %v", size, err)
		}
		if !bytes.Equal(decompressed, input) {
			t.Fatalf("size=%d: round-trip mismatch: got %d bytes, want %d", size, len(decompressed), len(input))
		}
	}
}

func TestCompress4XDecompress4XRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, size := range []int{256, 1024, 4096, 65536} {
		input := make([]byte, size)
		for i := range input {
			input[i] = byte(rng.Intn(32))
		}
		s := &Scratch{}
		compressed, reused, err := Compress4X(input, s)
		if err != nil {
			t.Fatalf("size=%d: Compress4X: %v", size, err)
		}
		_ = reused

		s2, remain, err := ReadTable(compressed, nil)
		if err != nil {
			t.Fatalf("size=%d: ReadTable: %v", size, err)
		}

		dec := s2.Decoder()
		decompressed, err := dec.Decompress4X(make([]byte, 0, len(input)), remain)
		if err != nil {
			t.Fatalf("size=%d: Decompress4X: %v", size, err)
		}
		if !bytes.Equal(decompressed, input) {
			t.Fatalf("size=%d: round-trip mismatch: got %d bytes, want %d", size, len(decompressed), len(input))
		}
	}
}

func TestCompressRLE(t *testing.T) {
	input := bytes.Repeat([]byte{42}, 1000)
	s := &Scratch{}
	_, _, err := Compress1X(input, s)
	if err != ErrUseRLE {
		t.Errorf("expected ErrUseRLE, got %v", err)
	}
}

func TestCompressIncompressible(t *testing.T) {
	s := &Scratch{}
	_, _, err := Compress1X([]byte{1}, s)
	if err != ErrIncompressible {
		t.Errorf("expected ErrIncompressible, got %v", err)
	}
}

func TestTransferCTable(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	input := make([]byte, 4096)
	for i := range input {
		input[i] = byte(rng.Intn(32))
	}

	s1 := &Scratch{}
	_, _, err := Compress1X(input, s1)
	if err != nil {
		t.Fatal(err)
	}

	s2 := &Scratch{}
	s2.TransferCTable(s1)
	if s2.prevTableLog != s1.prevTableLog {
		t.Error("prevTableLog not transferred")
	}
}

func TestScratchReuse(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	// Force no reuse so each output includes the table.
	s := &Scratch{Reuse: ReusePolicyNone}
	for i := 0; i < 5; i++ {
		input := make([]byte, 1024+i*512)
		for j := range input {
			input[j] = byte(rng.Intn(32))
		}
		compressed, reused, err := Compress1X(input, s)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if reused {
			t.Fatalf("iteration %d: should not reuse", i)
		}

		s2, remain, err := ReadTable(compressed, nil)
		if err != nil {
			t.Fatalf("iteration %d ReadTable: %v", i, err)
		}
		dec := s2.Decoder()
		got, err := dec.Decompress1X(make([]byte, 0, len(input)), remain)
		if err != nil {
			t.Fatalf("iteration %d Decompress1X: %v", i, err)
		}
		if !bytes.Equal(got, input) {
			t.Fatalf("iteration %d: mismatch", i)
		}
	}
}

func TestTableReusePolicy(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	input := make([]byte, 4096)
	for i := range input {
		input[i] = byte(rng.Intn(32))
	}

	s := &Scratch{Reuse: ReusePolicyNone}
	_, reused, err := Compress1X(input, s)
	if err != nil {
		t.Fatal(err)
	}
	if reused {
		t.Error("should not reuse with ReusePolicyNone")
	}

	// Second compression with Allow should potentially reuse.
	s.Reuse = ReusePolicyAllow
	_, _, err = Compress1X(input, s)
	if err != nil {
		t.Fatal(err)
	}
}
