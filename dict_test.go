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

func loadTestDict(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/d0.dict")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseDict(t *testing.T) {
	t.Run("format", func(t *testing.T) {
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
	})

	t.Run("offsets_not_default", func(t *testing.T) {
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
	})

	t.Run("content_size", func(t *testing.T) {
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
	})

	t.Run("id_frame_encoding", func(t *testing.T) {
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

				e := NewEncoder()
				e.AddDict(d)
				compressed := e.AppendCompress(nil, src)

				dec := NewDecoder()
				dec.AddDict(d)
				got, err := dec.AppendDecompress(nil, compressed)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, src) {
					t.Fatal("mismatch")
				}
			})
		}
	})
}

func TestParseDict_Errors(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if _, err := ParseDict(nil); err == nil {
			t.Fatal("expected error for nil input")
		}
	})

	t.Run("short", func(t *testing.T) {
		if _, err := ParseDict(make([]byte, 10)); err == nil {
			t.Fatal("expected error for short input")
		}
	})

	t.Run("bad_magic", func(t *testing.T) {
		bad := make([]byte, 100)
		bad[0], bad[1], bad[2], bad[3] = 0x00, 0x00, 0x00, 0x00
		if _, err := ParseDict(bad); !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("id0", func(t *testing.T) {
		raw := append([]byte{}, loadTestDict(t)...)
		binary.LittleEndian.PutUint32(raw[4:8], 0)
		_, err := ParseDict(raw)
		if err == nil || !strings.Contains(err.Error(), "dictionary ID 0") {
			t.Fatalf("expected 'dictionary ID 0' error, got: %v", err)
		}
	})

	t.Run("invalid_offsets", func(t *testing.T) {
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
			for i := range 12 {
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
	})

	t.Run("truncated", func(t *testing.T) {
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
	})

	t.Run("min_size", func(t *testing.T) {
		// Exactly 8 (header) + 12 (offsets) = 20 bytes with valid magic + nonzero ID.
		// No room for Huffman/FSE tables.
		b := make([]byte, 20)
		copy(b[:4], dictMagic)
		binary.LittleEndian.PutUint32(b[4:8], 42)
		_, err := ParseDict(b)
		if err == nil {
			t.Fatal("expected error for minimal-size dict with no tables")
		}
	})
}

func TestDict_NilSafety(t *testing.T) {
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

func TestDict_MarshalBinary(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var d2 Dict
	if err := d2.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	if d2.ID() != d.ID() {
		t.Fatalf("ID: %d != %d", d2.ID(), d.ID())
	}
	if !bytes.Equal(d2.Bytes(), d.Bytes()) {
		t.Fatal("Bytes mismatch")
	}
}

func TestEncoder_SetRawDict(t *testing.T) {
	t.Run("round_trip_all_levels", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("dictionary prefix content here! "), 100)
		src := append([]byte{}, dictContent[500:1500]...)
		src = append(src, []byte("and some unique new content")...)
		src = bytes.Repeat(src, 10)

		for level := BestSpeed; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				var buf bytes.Buffer
				e := NewEncoder()
				if err := e.SetLevel(level); err != nil {
					t.Fatal(err)
				}
				e.SetRawDict(dictContent)
				w := NewWriter(&buf, e)
				w.Reset(&buf)
				if _, err := w.Write(src); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}

				dec := NewDecoder()
				dec.SetRawDict(dictContent)
				r := NewReader(bytes.NewReader(buf.Bytes()), dec)
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
	})

	t.Run("append_to", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("encode-all dict prefix "), 80)
		src := append([]byte{}, dictContent[200:800]...)
		src = append(src, bytes.Repeat([]byte("extra payload "), 50)...)

		for level := BestSpeed; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				e := NewEncoder()
				if err := e.SetLevel(level); err != nil {
					t.Fatal(err)
				}
				e.SetRawDict(dictContent)
				compressed := e.AppendCompress(nil, src)

				dec := NewDecoder()
				dec.SetRawDict(dictContent)
				r := NewReader(bytes.NewReader(compressed), dec)
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
	})

	t.Run("improves_compression", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog "), 100)
		// Data that heavily references dict content.
		src := append([]byte{}, dictContent[:2000]...)
		src = append(src, []byte("tiny unique suffix")...)

		e := NewEncoder()

		// Without dict.
		noDictCompressed := e.AppendCompress(nil, src)

		// With dict.
		e.SetRawDict(dictContent)
		withDictCompressed := e.AppendCompress(nil, src)

		if len(withDictCompressed) > len(noDictCompressed) {
			t.Fatalf("dict should help: with=%d, without=%d", len(withDictCompressed), len(noDictCompressed))
		}
	})

	t.Run("content_match", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("AAABBBCCC"), 500)
		src := append([]byte{}, dictContent[:2000]...)
		src = append(src, []byte("unique tail")...)

		e := NewEncoder()

		noDictCompressed := e.AppendCompress(nil, src)

		e.SetRawDict(dictContent)
		withDictCompressed := e.AppendCompress(nil, src)

		if len(withDictCompressed) >= len(noDictCompressed) {
			t.Fatalf("dict should shrink output: with=%d, without=%d", len(withDictCompressed), len(noDictCompressed))
		}
	})

	t.Run("small", func(t *testing.T) {
		src := bytes.Repeat([]byte("small dict edge case "), 50)
		for _, sz := range []int{8, 16} {
			t.Run("", func(t *testing.T) {
				dictContent := bytes.Repeat([]byte("x"), sz)
				e := NewEncoder()
				e.SetRawDict(dictContent)
				compressed := e.AppendCompress(nil, src)

				dec := NewDecoder()
				dec.SetRawDict(dictContent)
				r := NewReader(bytes.NewReader(compressed), dec)
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
	})

	t.Run("too_short", func(t *testing.T) {
		src := bytes.Repeat([]byte("short dict ignored "), 50)

		// Compress without dict.
		e := NewEncoder()
		want := e.AppendCompress(nil, src)

		// SetRawDict with < 8 bytes should be silently ignored.
		for _, sz := range []int{1, 4, 7} {
			t.Run("", func(t *testing.T) {
				e2 := NewEncoder()
				e2.SetRawDict(bytes.Repeat([]byte("x"), sz))
				got := e2.AppendCompress(nil, src)
				if !bytes.Equal(got, want) {
					t.Fatalf("sz=%d: short dict was not ignored (output differs)", sz)
				}
			})
		}

		// Reader side: short dict should not register.
		dec := NewDecoder()
		dec.SetRawDict(bytes.Repeat([]byte("x"), 3))
		got, err := dec.AppendDecompress(nil, want)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("reader mismatch")
		}
	})

	t.Run("short_no_op", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("real dict "), 50)
		src := bytes.Repeat([]byte("data "), 100)

		// Compress with dict.
		e := NewEncoder()
		e.SetRawDict(dictContent)
		withDict := e.AppendCompress(nil, src)

		// Short dict should be a no-op — dict stays.
		e.SetRawDict([]byte("x"))
		stillWithDict := e.AppendCompress(nil, src)

		if !bytes.Equal(withDict, stillWithDict) {
			t.Fatal("short SetRawDict should not have cleared dict")
		}
	})

	t.Run("nil_clear", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("dict content "), 50)
		src := []byte("hello world, compressed without dict")

		var buf bytes.Buffer
		e := NewEncoder()
		e.SetRawDict(dictContent)
		e.SetRawDict(nil) // clear dict
		w := NewWriter(&buf, e)
		w.Reset(&buf)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := NewReader(bytes.NewReader(buf.Bytes()), nil)
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("nil_clear_via_adddict", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("dict content "), 50)
		src := []byte("hello world, compressed without dict")

		var buf bytes.Buffer
		e := NewEncoder()
		e.SetRawDict(dictContent)
		e.AddDict(nil) // clear dict
		w := NewWriter(&buf, e)
		w.Reset(&buf)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := NewReader(bytes.NewReader(buf.Bytes()), nil)
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("reuse", func(t *testing.T) {
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
				e := NewEncoder()
				e.SetRawDict(dictContent)
				compressed := e.AppendCompress(nil, frame.src)

				dec := NewDecoder()
				dec.SetRawDict(dictContent)
				r := NewReader(bytes.NewReader(compressed), dec)
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
	})
}

func TestEncoder_AddDict(t *testing.T) {
	t.Run("round_trip", func(t *testing.T) {
		raw := loadTestDict(t)
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		src := bytes.Repeat([]byte("parsed dict roundtrip test data "), 200)

		for level := BestSpeed; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				e := NewEncoder()
				if err := e.SetLevel(level); err != nil {
					t.Fatal(err)
				}
				e.AddDict(d)
				compressed := e.AppendCompress(nil, src)

				dec := NewDecoder()
				dec.AddDict(d)
				r := NewReader(bytes.NewReader(compressed), dec)
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
	})

	t.Run("stream_round_trip", func(t *testing.T) {
		raw := loadTestDict(t)
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		src := bytes.Repeat([]byte("streaming parsed dict test "), 300)

		var buf bytes.Buffer
		e := NewEncoder()
		e.AddDict(d)
		w := NewWriter(&buf, e)
		w.Reset(&buf)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		dec := NewDecoder()
		dec.AddDict(d)
		r := NewReader(bytes.NewReader(buf.Bytes()), dec)
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(src))
		}
	})

	t.Run("multi_block", func(t *testing.T) {
		raw := loadTestDict(t)
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		// > 128KB to force multiple blocks.
		src := bytes.Repeat([]byte("multiblock dict content xyz "), 6000)

		e := NewEncoder()
		e.AddDict(d)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.AddDict(d)
		r := NewReader(bytes.NewReader(compressed), dec)
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(src))
		}
	})

	t.Run("append_compress", func(t *testing.T) {
		raw := loadTestDict(t)
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		src := bytes.Repeat([]byte("decode-all parsed dict test "), 100)

		e := NewEncoder()
		e.AddDict(d)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.AddDict(d)
		got, err := dec.AppendDecompress(nil, compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(src))
		}
	})
}

func TestDecoder_SetRawDict(t *testing.T) {
	t.Run("nil_clear", func(t *testing.T) {
		raw := loadTestDict(t)
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		src := bytes.Repeat([]byte("reader raw dict nil clear "), 50)

		e := NewEncoder()
		e.AddDict(d)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.AddDict(d)
		r := NewReader(bytes.NewReader(compressed), dec)
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch before clear")
		}

		dec.SetRawDict(nil) // clear all dicts
		if err := r.Reset(bytes.NewReader(compressed)); err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(r)
		_ = r.Close()
		if err != ErrUnknownDictionary {
			t.Fatalf("expected ErrUnknownDictionary, got: %v", err)
		}
	})

	t.Run("short_no_op", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("real dict content "), 50)
		src := bytes.Repeat([]byte("data "), 100)

		e := NewEncoder()
		e.SetRawDict(dictContent)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.SetRawDict(dictContent)

		// Short dict should be a no-op — real dict stays.
		dec.SetRawDict([]byte("short"))

		got, err := dec.AppendDecompress(nil, compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch — short dict should not have cleared previous")
		}
	})

	t.Run("exactly_8", func(t *testing.T) {
		dict := []byte("12345678")
		src := bytes.Repeat([]byte("exactly8 "), 100)

		e := NewEncoder()
		e.SetRawDict(dict)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.SetRawDict(dict)
		got, err := dec.AppendDecompress(nil, compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("reuse_across_reset", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("encode-all dict prefix "), 80)
		src1 := append([]byte{}, dictContent[200:800]...)
		src1 = append(src1, bytes.Repeat([]byte("reader frame 1 "), 50)...)
		src2 := append([]byte{}, dictContent[100:600]...)
		src2 = append(src2, bytes.Repeat([]byte("reader frame 2 "), 50)...)

		// Compress each frame with its own writer to avoid writer reuse issues.
		compress := func(src []byte) []byte {
			e := NewEncoder()
			e.SetRawDict(dictContent)
			return e.AppendCompress(nil, src)
		}
		comp1 := compress(src1)
		comp2 := compress(src2)

		dec := NewDecoder()
		dec.SetRawDict(dictContent)
		r := NewReader(bytes.NewReader(comp1), dec)
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
	})

	t.Run("switch_between_frames", func(t *testing.T) {
		dict1 := bytes.Repeat([]byte("dict one content "), 50)
		dict2 := bytes.Repeat([]byte("dict two content "), 50)
		src := bytes.Repeat([]byte("payload "), 100)

		e1 := NewEncoder()
		e1.SetRawDict(dict1)
		frame1 := e1.AppendCompress(nil, src)

		e2 := NewEncoder()
		e2.SetRawDict(dict2)
		frame2 := e2.AppendCompress(nil, src)

		// Reader switches dicts between frames.
		dec := NewDecoder()
		dec.SetRawDict(dict1)
		r := NewReader(bytes.NewReader(frame1), dec)
		got1, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got1, src) {
			t.Fatal("frame 1 mismatch")
		}

		dec.SetRawDict(dict2)
		if err := r.Reset(bytes.NewReader(frame2)); err != nil {
			t.Fatal(err)
		}
		got2, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got2, src) {
			t.Fatal("frame 2 mismatch")
		}
	})
}

func TestDecoder_AddDict(t *testing.T) {
	t.Run("nil_clear", func(t *testing.T) {
		raw := loadTestDict(t)
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		src := bytes.Repeat([]byte("reader add dict nil clear "), 50)

		e := NewEncoder()
		e.AddDict(d)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.AddDict(d)
		r := NewReader(bytes.NewReader(compressed), dec)
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch before clear")
		}

		dec.AddDict(nil)
		if err := r.Reset(bytes.NewReader(compressed)); err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(r)
		_ = r.Close()
		if err != ErrUnknownDictionary {
			t.Fatalf("expected ErrUnknownDictionary, got: %v", err)
		}
	})

	t.Run("multiple_registered", func(t *testing.T) {
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

		eA := NewEncoder()
		eA.AddDict(dA)
		compA := eA.AppendCompress(nil, srcA)

		eB := NewEncoder()
		eB.AddDict(dB)
		compB := eB.AppendCompress(nil, srcB)

		// Concatenate two frames.
		combined := append(compA, compB...)

		dec := NewDecoder()
		dec.AddDict(dA)
		dec.AddDict(dB)
		got, err := dec.AppendDecompress(nil, combined)
		if err != nil {
			t.Fatal(err)
		}
		want := append(srcA, srcB...)
		if !bytes.Equal(got, want) {
			t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(want))
		}
	})

	t.Run("nil_clear_multiple", func(t *testing.T) {
		raw := loadTestDict(t)
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		dictContent := bytes.Repeat([]byte("raw dict for multi clear "), 100)
		src := bytes.Repeat([]byte("multi dict clear test "), 50)

		// Compress with parsed dict.
		eParsed := NewEncoder()
		eParsed.AddDict(d)
		compParsed := eParsed.AppendCompress(nil, src)

		// Compress with raw dict.
		eRaw := NewEncoder()
		eRaw.SetRawDict(dictContent)
		compRaw := eRaw.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.AddDict(d)
		dec.SetRawDict(dictContent)

		// Both should decode.
		got, err := dec.AppendDecompress(nil, compParsed)
		if err != nil {
			t.Fatalf("parsed dict decode: %v", err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("parsed dict mismatch")
		}
		got, err = dec.AppendDecompress(nil, compRaw)
		if err != nil {
			t.Fatalf("raw dict decode: %v", err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("raw dict mismatch")
		}

		dec.AddDict(nil) // clear all

		_, err = dec.AppendDecompress(nil, compParsed)
		if err != ErrUnknownDictionary {
			t.Fatalf("parsed dict after clear: expected ErrUnknownDictionary, got: %v", err)
		}
		_, err = dec.AppendDecompress(nil, compRaw)
		// Raw dict (ID 0) doesn't embed a dict ID in the frame, so the error
		// is a corruption from bad offsets rather than ErrUnknownDictionary.
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("raw dict after clear: expected ErrCorrupted, got: %v", err)
		}
	})
}

func TestNoDictDecodeWithDict(t *testing.T) {
	// Compress without dict, decompress with dict set — should still work.
	src := bytes.Repeat([]byte("no dict needed "), 200)
	dictContent := []byte("some dict content that is not used")

	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	dec := NewDecoder()
	dec.SetRawDict(dictContent)
	r := NewReader(bytes.NewReader(compressed), dec)
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

func TestDictIDMismatch(t *testing.T) {
	raw := loadTestDict(t)
	d, err := ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	src := bytes.Repeat([]byte("dict id mismatch test "), 50)

	e := NewEncoder()
	e.AddDict(d)
	compressed := e.AppendCompress(nil, src)

	// Decode without dict registered → ErrUnknownDictionary.
	r := NewReader(bytes.NewReader(compressed), nil)
	_, err = io.ReadAll(r)
	_ = r.Close()
	if err != ErrUnknownDictionary {
		t.Fatalf("expected ErrUnknownDictionary, got: %v", err)
	}

	// Also test DecodeBytes path.
	dec := NewDecoder()
	_, err = dec.AppendDecompress(nil, compressed)
	if err != ErrUnknownDictionary {
		t.Fatalf("DecodeBytes: expected ErrUnknownDictionary, got: %v", err)
	}
}
