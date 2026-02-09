// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "testing"

func TestBitWriterFlush32(t *testing.T) {
	var bw bitWriter
	bw.reset(nil)
	bw.addBits32NC(0xDEADBEEF, 32)
	bw.flush32()
	if len(bw.out) != 4 {
		t.Fatalf("expected 4 bytes after flush32, got %d", len(bw.out))
	}
	bw.addBits16NC(0xFF, 8)
	bw.flush32()
	// 8 bits < 32, so no additional flush.
	if len(bw.out) != 4 {
		t.Fatalf("expected 4 bytes (no second flush), got %d", len(bw.out))
	}
}

func TestBitWriterClose(t *testing.T) {
	var bw bitWriter
	bw.reset(nil)
	bw.addBits16NC(5, 3)
	bw.close()

	if len(bw.out) == 0 {
		t.Fatal("expected output after close")
	}
	last := bw.out[len(bw.out)-1]
	if last == 0 {
		t.Fatal("last byte is zero; end marker missing")
	}
	// Verify round-trip: bitReader should consume it cleanly.
	var br bitReader
	if err := br.init(bw.out); err != nil {
		t.Fatal(err)
	}
	if got := br.getBits(3); got != 5 {
		t.Fatalf("round-trip: got %d, want 5", got)
	}
	if err := br.close(); err != nil {
		t.Fatal(err)
	}
}

func TestBitWriterReset(t *testing.T) {
	var bw bitWriter
	bw.reset(nil)
	bw.addBits16NC(0xAB, 8)
	bw.close()
	first := make([]byte, len(bw.out))
	copy(first, bw.out)

	bw.reset(nil)
	bw.addBits16NC(0xCD, 8)
	bw.close()

	if len(bw.out) == 0 {
		t.Fatal("expected output after second write")
	}
	// Verify the two outputs are independent.
	var br bitReader
	if err := br.init(first); err != nil {
		t.Fatal(err)
	}
	if got := br.getBits(8); got != 0xAB {
		t.Fatalf("first: got %#x, want 0xAB", got)
	}
	_ = br.close()

	if err := br.init(bw.out); err != nil {
		t.Fatal(err)
	}
	if got := br.getBits(8); got != 0xCD {
		t.Fatalf("second: got %#x, want 0xCD", got)
	}
	_ = br.close()
}
