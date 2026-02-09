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
