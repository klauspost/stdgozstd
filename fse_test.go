// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "testing"

func TestInitPredefined(t *testing.T) {
	initPredefined()
	for i, name := range []string{"LiteralLengths", "Offsets", "MatchLengths"} {
		dec := &fsePredef[i]
		if dec.symbolLen == 0 {
			t.Fatalf("fsePredef[%s].symbolLen == 0", name)
		}
		if dec.actualTableLog == 0 {
			t.Fatalf("fsePredef[%s].actualTableLog == 0", name)
		}

		enc := &fsePredefEnc[i]
		if enc.symbolLen == 0 {
			t.Fatalf("fsePredefEnc[%s].symbolLen == 0", name)
		}
		if enc.actualTableLog == 0 {
			t.Fatalf("fsePredefEnc[%s].actualTableLog == 0", name)
		}
	}
}

func TestSymbolTableX(t *testing.T) {
	initPredefined()
	for i, name := range []string{"LiteralLengths", "Offsets", "MatchLengths"} {
		if len(symbolTableX[i]) == 0 {
			t.Fatalf("symbolTableX[%s] is empty", name)
		}
	}
}

func TestFSEEncoderDecoderRoundTrip(t *testing.T) {
	var enc fseEncoder
	// Symbol frequencies: 4 symbols, total = 200.
	enc.count[0] = 80
	enc.count[1] = 60
	enc.count[2] = 40
	enc.count[3] = 20
	enc.symbolLen = 4
	enc.maxCount = 80

	if err := enc.normalizeCount(200); err != nil {
		t.Fatal(err)
	}

	header, err := enc.writeCount(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(header) == 0 {
		t.Fatal("writeCount produced empty header")
	}

	// Pad to ensure byteReader has at least 4 bytes for Uint32NC.
	padded := make([]byte, len(header)+4)
	copy(padded, header)
	br := byteReader{b: padded}

	var dec fseDecoder
	if err := dec.readNCount(&br, maxSymbolValue); err != nil {
		t.Fatal(err)
	}

	if dec.symbolLen != enc.symbolLen {
		t.Fatalf("symbolLen: got %d, want %d", dec.symbolLen, enc.symbolLen)
	}
	if dec.actualTableLog != enc.actualTableLog {
		t.Fatalf("actualTableLog: got %d, want %d", dec.actualTableLog, enc.actualTableLog)
	}
	for i := range enc.symbolLen {
		if dec.norm[i] != enc.norm[i] {
			t.Fatalf("norm[%d]: got %d, want %d", i, dec.norm[i], enc.norm[i])
		}
	}
}

// RFC 8878 Appendix A: predefined distribution tables.
func TestPredefinedTablesMatchRFC(t *testing.T) {
	initPredefined()

	// Literal Length table: tableLog=6, 36 symbols.
	llNorm := []int16{
		4, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1,
		2, 2, 2, 2, 2, 2, 2, 2, 2, 3, 2, 1, 1, 1, 1, 1,
		-1, -1, -1, -1,
	}
	if fsePredef[tableLiteralLengths].actualTableLog != 6 {
		t.Fatalf("LL tableLog: got %d, want 6", fsePredef[tableLiteralLengths].actualTableLog)
	}
	for i, want := range llNorm {
		if got := fsePredef[tableLiteralLengths].norm[i]; got != want {
			t.Fatalf("LL norm[%d]: got %d, want %d", i, got, want)
		}
	}

	// Offset table: tableLog=5, 29 symbols.
	ofNorm := []int16{
		1, 1, 1, 1, 1, 1, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, -1, -1, -1, -1, -1,
	}
	if fsePredef[tableOffsets].actualTableLog != 5 {
		t.Fatalf("OF tableLog: got %d, want 5", fsePredef[tableOffsets].actualTableLog)
	}
	for i, want := range ofNorm {
		if got := fsePredef[tableOffsets].norm[i]; got != want {
			t.Fatalf("OF norm[%d]: got %d, want %d", i, got, want)
		}
	}

	// Match Length table: tableLog=6, 53 symbols.
	mlNorm := []int16{
		1, 4, 3, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1,
		1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, -1, -1,
		-1, -1, -1, -1, -1,
	}
	if fsePredef[tableMatchLengths].actualTableLog != 6 {
		t.Fatalf("ML tableLog: got %d, want 6", fsePredef[tableMatchLengths].actualTableLog)
	}
	for i, want := range mlNorm {
		if got := fsePredef[tableMatchLengths].norm[i]; got != want {
			t.Fatalf("ML norm[%d]: got %d, want %d", i, got, want)
		}
	}
}

func TestFSENormalizeCountRLE(t *testing.T) {
	var enc fseEncoder
	// Single symbol repeated: should trigger RLE mode.
	enc.count[0] = 100
	enc.symbolLen = 1
	enc.maxCount = 100
	if err := enc.normalizeCount(100); err != nil {
		t.Fatal(err)
	}
	if !enc.useRLE {
		t.Fatal("expected RLE mode for single symbol")
	}
}
