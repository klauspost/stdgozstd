// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "testing"

func TestBitReaderInit(t *testing.T) {
	var bw bitWriter
	bw.reset(nil)
	bw.addBits16NC(0, 4)
	bw.close()

	var br bitReader
	if err := br.init(bw.out); err != nil {
		t.Fatal(err)
	}
}

func TestBitReaderGetBits(t *testing.T) {
	var bw bitWriter
	bw.reset(nil)
	bw.addBits16NC(5, 4)
	bw.addBits16NC(13, 5)
	bw.addBits16NC(3, 3)
	bw.close()

	var br bitReader
	if err := br.init(bw.out); err != nil {
		t.Fatal(err)
	}
	if got := br.getBits(3); got != 3 {
		t.Fatalf("got %d, want 3", got)
	}
	br.fill()
	if got := br.getBits(5); got != 13 {
		t.Fatalf("got %d, want 13", got)
	}
	br.fill()
	if got := br.getBits(4); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
	if err := br.close(); err != nil {
		t.Fatal(err)
	}
}

func TestBitReaderFill(t *testing.T) {
	var bw bitWriter
	bw.reset(nil)
	// Write enough bits to exceed 64 bits total.
	bw.addBits32NC(0xDEAD, 16)
	bw.flush32()
	bw.addBits32NC(0xBEEF, 16)
	bw.flush32()
	bw.addBits32NC(0xCAFE, 16)
	bw.flush32()
	bw.addBits32NC(0xBABE, 16)
	bw.flush32()
	bw.addBits16NC(7, 4)
	bw.close()

	var br bitReader
	if err := br.init(bw.out); err != nil {
		t.Fatal(err)
	}
	if got := br.getBits(4); got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
	br.fill()
	if got := br.getBits(16); got != 0xBABE {
		t.Fatalf("got %#x, want 0xBABE", got)
	}
	br.fill()
	if got := br.getBits(16); got != 0xCAFE {
		t.Fatalf("got %#x, want 0xCAFE", got)
	}
	br.fill()
	if got := br.getBits(16); got != 0xBEEF {
		t.Fatalf("got %#x, want 0xBEEF", got)
	}
	br.fill()
	if got := br.getBits(16); got != 0xDEAD {
		t.Fatalf("got %#x, want 0xDEAD", got)
	}
	if err := br.close(); err != nil {
		t.Fatal(err)
	}
}

func TestBitReaderInitEmpty(t *testing.T) {
	var br bitReader
	if err := br.init(nil); err == nil {
		t.Fatal("expected error for empty slice")
	}
	if err := br.init([]byte{}); err == nil {
		t.Fatal("expected error for empty slice")
	}
}

func TestBitReaderInitZeroEnd(t *testing.T) {
	var br bitReader
	if err := br.init([]byte{0}); err == nil {
		t.Fatal("expected error for zero end byte")
	}
	if err := br.init([]byte{1, 2, 0}); err == nil {
		t.Fatal("expected error for zero end byte")
	}
}

func TestBitReaderRemain(t *testing.T) {
	var br bitReader
	br.cursor = 2
	br.bitsRead = 64
	if got := br.remain(); got != 16 {
		t.Fatalf("got %d, want 16", got)
	}

	br.cursor = 0
	br.bitsRead = 70
	if got := br.remain(); got != -6 {
		t.Fatalf("got %d, want -6", got)
	}
}

