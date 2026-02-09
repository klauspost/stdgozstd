// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestParseDictErrors(t *testing.T) {
	// Empty input.
	if _, err := ParseDict(nil); err == nil {
		t.Fatal("expected error for nil input")
	}
	// Too short.
	if _, err := ParseDict(make([]byte, 10)); err == nil {
		t.Fatal("expected error for short input")
	}
	// Wrong magic.
	bad := make([]byte, 100)
	bad[0], bad[1], bad[2], bad[3] = 0x00, 0x00, 0x00, 0x00
	if _, err := ParseDict(bad); !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestDictNilSafety(t *testing.T) {
	var d *Dict
	if d.ID() != 0 {
		t.Fatal("nil Dict.ID should be 0")
	}
	if d.Bytes() != nil {
		t.Fatal("nil Dict.Bytes should be nil")
	}
	var di *dict
	if di.ID() != 0 {
		t.Fatal("nil dict.ID should be 0")
	}
}

func TestRawDictRoundTripAllLevels(t *testing.T) {
	dictContent := bytes.Repeat([]byte("dictionary prefix content here! "), 100)
	src := append([]byte{}, dictContent[500:1500]...)
	src = append(src, []byte("and some unique new content")...)
	src = bytes.Repeat(src, 10)

	for level := BestSpeed; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)
			if err := w.SetLevel(level); err != nil {
				t.Fatal(err)
			}
			w.SetRawDict(dictContent)
			w.Reset(&buf)
			if _, err := w.Write(src); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			r, err := NewReader(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			r.SetRawDict(dictContent)
			got, err := io.ReadAll(r)
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if err != nil {
				t.Fatalf("level %d: readall: %v", level, err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("level %d: mismatch: got %d, want %d bytes", level, len(got), len(src))
			}
		})
	}
}

func TestRawDictAppendTo(t *testing.T) {
	dictContent := bytes.Repeat([]byte("encode-all dict prefix "), 80)
	src := append([]byte{}, dictContent[200:800]...)
	src = append(src, bytes.Repeat([]byte("extra payload "), 50)...)

	for level := BestSpeed; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			w := NewWriter(nil)
			if err := w.SetLevel(level); err != nil {
				t.Fatal(err)
			}
			w.SetRawDict(dictContent)
			compressed := w.AppendCompress(nil, src)

			r, err := NewReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatal(err)
			}
			r.SetRawDict(dictContent)
			got, err := io.ReadAll(r)
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("level %d: mismatch", level)
			}
		})
	}
}

func TestRawDictImprovesCompression(t *testing.T) {
	dictContent := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog "), 100)
	// Data that heavily references dict content.
	src := append([]byte{}, dictContent[:2000]...)
	src = append(src, []byte("tiny unique suffix")...)

	w := NewWriter(nil)

	// Without dict.
	noDictCompressed := w.AppendCompress(nil, src)

	// With dict.
	w.SetRawDict(dictContent)
	withDictCompressed := w.AppendCompress(nil, src)

	if len(withDictCompressed) > len(noDictCompressed) {
		t.Fatalf("dict should help: with=%d, without=%d", len(withDictCompressed), len(noDictCompressed))
	}
}

func TestNoDictDecodeWithDict(t *testing.T) {
	// Compress without dict, decompress with dict set — should still work.
	src := bytes.Repeat([]byte("no dict needed "), 200)
	dictContent := []byte("some dict content that is not used")

	w := NewWriter(nil)
	compressed := w.AppendCompress(nil, src)

	r, err := NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	r.SetRawDict(dictContent)
	got, err := io.ReadAll(r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestDictNilClear(t *testing.T) {
	dictContent := bytes.Repeat([]byte("dict content "), 50)
	src := []byte("hello world, compressed without dict")

	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.SetRawDict(dictContent)
	w.AddDict(nil) // clear dict
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func loadTestDict(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/d0.dict")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --- Parsed Dict Format Validation ---

func TestParseDictFormat(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID() == 0 {
		t.Fatal("expected non-zero dict ID")
	}
	if !bytes.Equal(d.Bytes(), raw) {
		t.Fatal("Bytes() != input")
	}
	if d.d.litEnc == nil {
		t.Fatal("litEnc is nil")
	}
	for i, off := range d.d.offsets {
		if off <= 0 {
			t.Fatalf("offset[%d] = %d, want > 0", i, off)
		}
	}
	if len(d.d.content) == 0 {
		t.Fatal("content is empty")
	}
}

func TestParseDictID0Rejected(t *testing.T) {
	raw := append([]byte{}, loadTestDict(t)...)
	binary.LittleEndian.PutUint32(raw[4:8], 0)
	_, err := ParseDict(raw)
	if err == nil || !strings.Contains(err.Error(), "dictionary ID 0") {
		t.Fatalf("expected 'dictionary ID 0' error, got: %v", err)
	}
}

func TestParseDictInvalidOffsets(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Locate offset position: after magic(4) + id(4) + huff table + 3 FSE tables.
	// offsets are 12 bytes before content.
	offsetPos := len(raw) - len(d.d.content) - 12

	t.Run("zero", func(t *testing.T) {
		b := append([]byte{}, raw...)
		// Zero out all 3 offsets.
		for i := 0; i < 12; i++ {
			b[offsetPos+i] = 0
		}
		_, err := ParseDict(b)
		if err == nil || !strings.Contains(err.Error(), "invalid offset") {
			t.Fatalf("expected 'invalid offset' error, got: %v", err)
		}
	})

	t.Run("too_large", func(t *testing.T) {
		b := append([]byte{}, raw...)
		// Set first offset to huge value.
		binary.LittleEndian.PutUint32(b[offsetPos:], uint32(len(d.d.content)+1000))
		// Keep other offsets valid.
		binary.LittleEndian.PutUint32(b[offsetPos+4:], 1)
		binary.LittleEndian.PutUint32(b[offsetPos+8:], 1)
		_, err := ParseDict(b)
		if err == nil || !strings.Contains(err.Error(), "initial offset bigger") {
			t.Fatalf("expected 'initial offset bigger' error, got: %v", err)
		}
	})
}

func TestParseDictTruncated(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Cut right before the 12-byte offsets region.
	offsetStart := len(raw) - len(d.d.content) - 12
	truncPoints := []int{8, 12, 20, offsetStart - 1, offsetStart + 6}
	for _, n := range truncPoints {
		if n > len(raw) || n <= 0 {
			continue
		}
		_, err := ParseDict(raw[:n])
		if err == nil {
			t.Fatalf("expected error for truncation at %d bytes", n)
		}
	}
}

func TestParseDictMinSize(t *testing.T) {
	// Exactly 8 (header) + 12 (offsets) = 20 bytes with valid magic + nonzero ID.
	// No room for Huffman/FSE tables.
	b := make([]byte, 20)
	copy(b[:4], dictMagic)
	binary.LittleEndian.PutUint32(b[4:8], 42)
	_, err := ParseDict(b)
	if err == nil {
		t.Fatal("expected error for minimal-size dict with no tables")
	}
}

// --- Parsed Dict Roundtrip ---

func TestParsedDictRoundTrip(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	src := bytes.Repeat([]byte("parsed dict roundtrip test data "), 200)

	for level := BestSpeed; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			w := NewWriter(nil)
			if err := w.SetLevel(level); err != nil {
				t.Fatal(err)
			}
			w.AddDict(d)
			compressed := w.AppendCompress(nil, src)

			r, err := NewReader(bytes.NewReader(compressed))
			if err != nil {
				t.Fatal(err)
			}
			r.AddDict(d)
			got, err := io.ReadAll(r)
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if err != nil {
				t.Fatalf("level %d: %v", level, err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("level %d: mismatch: got %d, want %d bytes", level, len(got), len(src))
			}
		})
	}
}

func TestParsedDictStreamRoundTrip(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	src := bytes.Repeat([]byte("streaming parsed dict test "), 300)

	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.AddDict(d)
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	r.AddDict(d)
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(src))
	}
}

func TestParsedDictMultiBlock(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	// > 128KB to force multiple blocks.
	src := bytes.Repeat([]byte("multiblock dict content xyz "), 6000)

	w := NewWriter(nil)
	w.AddDict(d)
	compressed := w.AppendCompress(nil, src)

	r, err := NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	r.AddDict(d)
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(src))
	}
}

func TestParsedDictDecodeBytes(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	src := bytes.Repeat([]byte("decode-all parsed dict test "), 100)

	w := NewWriter(nil)
	w.AddDict(d)
	compressed := w.AppendCompress(nil, src)

	r, err := NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	r.AddDict(d)
	got, err := r.AppendDecompress(nil, compressed)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(src))
	}
}

// --- Dict ID Handling ---

func TestDictIDMismatch(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	src := bytes.Repeat([]byte("dict id mismatch test "), 50)

	w := NewWriter(nil)
	w.AddDict(d)
	compressed := w.AppendCompress(nil, src)

	// Decode without dict registered → ErrUnknownDictionary.
	r, err := NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	_ = r.Close()
	if err != ErrUnknownDictionary {
		t.Fatalf("expected ErrUnknownDictionary, got: %v", err)
	}

	// Also test DecodeBytes path.
	r2, _ := NewReader(bytes.NewReader(nil))
	_, err = r2.AppendDecompress(nil, compressed)
	_ = r2.Close()
	if err != ErrUnknownDictionary {
		t.Fatalf("DecodeBytes: expected ErrUnknownDictionary, got: %v", err)
	}
}

func TestDictIDFrameEncoding(t *testing.T) {
	raw := loadTestDict(t)
	src := bytes.Repeat([]byte("dict id frame encoding "), 50)

	// Test different ID ranges: 1-byte (<256), 2-byte (<65536), 4-byte (>=65536).
	ids := []uint32{1, 200, 60000, 100000}
	for _, id := range ids {
		t.Run("", func(t *testing.T) {
			b := append([]byte{}, raw...)
			binary.LittleEndian.PutUint32(b[4:8], id)
			d, err := ParseDict(b)
			if err != nil {
				t.Fatal(err)
			}
			if d.ID() != id {
				t.Fatalf("ID: got %d, want %d", d.ID(), id)
			}

			w := NewWriter(nil)
			w.AddDict(d)
			compressed := w.AppendCompress(nil, src)

			r, _ := NewReader(bytes.NewReader(nil))
			r.AddDict(d)
			got, err := r.AppendDecompress(nil, compressed)
			_ = r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, src) {
				t.Fatal("mismatch")
			}
		})
	}
}

func TestMultipleDictsRegistered(t *testing.T) {
	raw := loadTestDict(t)

	// Dict A: ID = 111.
	rawA := append([]byte{}, raw...)
	binary.LittleEndian.PutUint32(rawA[4:8], 111)
	dA, err := ParseDict(rawA)
	if err != nil {
		t.Fatal(err)
	}
	// Dict B: ID = 222.
	rawB := append([]byte{}, raw...)
	binary.LittleEndian.PutUint32(rawB[4:8], 222)
	dB, err := ParseDict(rawB)
	if err != nil {
		t.Fatal(err)
	}

	srcA := bytes.Repeat([]byte("frame A data "), 100)
	srcB := bytes.Repeat([]byte("frame B data "), 100)

	wA := NewWriter(nil)
	wA.AddDict(dA)
	compA := wA.AppendCompress(nil, srcA)

	wB := NewWriter(nil)
	wB.AddDict(dB)
	compB := wB.AppendCompress(nil, srcB)

	// Concatenate two frames.
	combined := append(compA, compB...)

	r, _ := NewReader(bytes.NewReader(nil))
	r.AddDict(dA)
	r.AddDict(dB)
	got, err := r.AppendDecompress(nil, combined)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := append(srcA, srcB...)
	if !bytes.Equal(got, want) {
		t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(want))
	}
}

// --- Dict Content & Offsets ---

func TestParsedDictOffsetsNotDefault(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i, off := range d.d.offsets {
		if off <= 0 {
			t.Fatalf("offset[%d] = %d, want > 0", i, off)
		}
		if off > len(d.d.content) {
			t.Fatalf("offset[%d] = %d, exceeds content length %d", i, off, len(d.d.content))
		}
	}
}

func TestRawDictContentMatchReferences(t *testing.T) {
	dictContent := bytes.Repeat([]byte("AAABBBCCC"), 500)
	src := append([]byte{}, dictContent[:2000]...)
	src = append(src, []byte("unique tail")...)

	w := NewWriter(nil)

	noDictCompressed := w.AppendCompress(nil, src)

	w.SetRawDict(dictContent)
	withDictCompressed := w.AppendCompress(nil, src)

	if len(withDictCompressed) >= len(noDictCompressed) {
		t.Fatalf("dict should shrink output: with=%d, without=%d", len(withDictCompressed), len(noDictCompressed))
	}
}

func TestParsedDictContentSize(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.d.content) == 0 {
		t.Fatal("content is empty")
	}
	if len(d.d.content) >= len(raw) {
		t.Fatalf("content (%d) should be smaller than raw (%d)", len(d.d.content), len(raw))
	}
}

// --- Edge Cases ---

func TestRawDictSmall(t *testing.T) {
	src := bytes.Repeat([]byte("small dict edge case "), 50)
	for _, sz := range []int{1, 8, 16} {
		t.Run("", func(t *testing.T) {
			dictContent := bytes.Repeat([]byte("x"), sz)
			w := NewWriter(nil)
			w.SetRawDict(dictContent)
			compressed := w.AppendCompress(nil, src)

			r, _ := NewReader(bytes.NewReader(compressed))
			r.SetRawDict(dictContent)
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, src) {
				t.Fatal("mismatch")
			}
		})
	}
}

func TestDictWriterReuseAcrossReset(t *testing.T) {
	dictContent := bytes.Repeat([]byte("encode-all dict prefix "), 80)
	src1 := append([]byte{}, dictContent[200:800]...)
	src1 = append(src1, bytes.Repeat([]byte("frame 1 payload "), 50)...)
	src2 := append([]byte{}, dictContent[100:600]...)
	src2 = append(src2, bytes.Repeat([]byte("frame 2 payload "), 50)...)

	for _, frame := range []struct {
		name string
		src  []byte
	}{{"frame1", src1}, {"frame2", src2}} {
		t.Run(frame.name, func(t *testing.T) {
			w := NewWriter(nil)
			w.SetRawDict(dictContent)
			compressed := w.AppendCompress(nil, frame.src)

			r, _ := NewReader(bytes.NewReader(compressed))
			r.SetRawDict(dictContent)
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, frame.src) {
				t.Fatal("mismatch")
			}
		})
	}
}

func TestDictReaderReuseAcrossReset(t *testing.T) {
	dictContent := bytes.Repeat([]byte("encode-all dict prefix "), 80)
	src1 := append([]byte{}, dictContent[200:800]...)
	src1 = append(src1, bytes.Repeat([]byte("reader frame 1 "), 50)...)
	src2 := append([]byte{}, dictContent[100:600]...)
	src2 = append(src2, bytes.Repeat([]byte("reader frame 2 "), 50)...)

	// Compress each frame with its own writer to avoid writer reuse issues.
	compress := func(src []byte) []byte {
		w := NewWriter(nil)
		w.SetRawDict(dictContent)
		return w.AppendCompress(nil, src)
	}
	comp1 := compress(src1)
	comp2 := compress(src2)

	r, _ := NewReader(bytes.NewReader(comp1))
	r.SetRawDict(dictContent)
	got1, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got1, src1) {
		t.Fatal("frame 1 mismatch")
	}

	if err := r.Reset(bytes.NewReader(comp2)); err != nil {
		t.Fatal(err)
	}
	got2, err := io.ReadAll(r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, src2) {
		t.Fatal("frame 2 mismatch")
	}
}
