// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"sync"
	"testing"

	"github.com/klauspost/stdgozstd/internal/xxhash"
)

// buildRawFrame creates a minimal zstd frame with raw blocks.
// singleSegment=true, no checksum.
func buildRawFrame(data []byte) []byte {
	var buf []byte
	// Magic
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	// FHD: single segment, no checksum
	buf = append(buf, 0x20)
	// FCS: 1 byte
	buf = append(buf, byte(len(data)))
	// Block header: last=1, type=raw, size=len(data)
	bh := uint32(1) | (0 << 1) | (uint32(len(data)) << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	// Block data
	buf = append(buf, data...)
	return buf
}

// buildRawFrameChecksum creates a frame with checksum.
func buildRawFrameChecksum(data []byte) []byte {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	// FHD: single segment, WITH checksum (bit 2)
	buf = append(buf, 0x24)
	buf = append(buf, byte(len(data)))
	bh := uint32(1) | (0 << 1) | (uint32(len(data)) << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	buf = append(buf, data...)
	// Compute xxhash64 checksum, take low 32 bits
	d := newXXHash(data)
	crc := uint32(d)
	buf = append(buf, byte(crc), byte(crc>>8), byte(crc>>16), byte(crc>>24))
	return buf
}

func newXXHash(data []byte) uint64 {
	h := xxhash.New()
	_, _ = h.Write(data)
	return h.Sum64()
}

func TestReadRawBlock(t *testing.T) {
	frame := buildRawFrame([]byte("hello"))
	r := NewReader(bytes.NewReader(frame), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestReadRLEBlock(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD) // magic
	buf = append(buf, 0x20)                   // FHD single segment
	buf = append(buf, 0x05)                   // FCS = 5
	// Block header: last=1, type=RLE(1), size=5
	bh := uint32(1) | (1 << 1) | (5 << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	buf = append(buf, 'A') // repeated byte

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "AAAAA" {
		t.Fatalf("got %q, want %q", got, "AAAAA")
	}
}

func TestReadCompressedRawLiterals(t *testing.T) {
	// Compressed block with raw literals and 0 sequences.
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20) // FHD
	buf = append(buf, 0x05) // FCS = 5

	// Compressed data: literals header (raw, size=5) + "hello" + seq header (0)
	compData := []byte{
		5 << 3, // literals header: raw, sizeFormat=0, size=5
		'h', 'e', 'l', 'l', 'o',
		0x00, // 0 sequences
	}
	bh := uint32(1) | (2 << 1) | (uint32(len(compData)) << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	buf = append(buf, compData...)

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestReadCompressedRLELiterals(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20) // FHD
	buf = append(buf, 0x05) // FCS = 5

	// Compressed data: literals header (RLE, size=5) + 'B' + seq header (0)
	compData := []byte{
		(5 << 3) | 1, // literals header: RLE, sizeFormat=0, size=5
		'B',
		0x00, // 0 sequences
	}
	bh := uint32(1) | (2 << 1) | (uint32(len(compData)) << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	buf = append(buf, compData...)

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "BBBBB" {
		t.Fatalf("got %q, want %q", got, "BBBBB")
	}
}

func TestReadMultiBlock(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20) // FHD
	buf = append(buf, 0x05) // FCS = 5

	// Block 1: not last, raw, size=3
	bh1 := uint32(0) | (0 << 1) | (3 << 3)
	buf = append(buf, byte(bh1), byte(bh1>>8), byte(bh1>>16))
	buf = append(buf, 'a', 'b', 'c')

	// Block 2: last, raw, size=2
	bh2 := uint32(1) | (0 << 1) | (2 << 3)
	buf = append(buf, byte(bh2), byte(bh2>>8), byte(bh2>>16))
	buf = append(buf, 'd', 'e')

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abcde" {
		t.Fatalf("got %q, want %q", got, "abcde")
	}
}

func TestReadMultiFrame(t *testing.T) {
	frame1 := buildRawFrame([]byte("hi"))
	frame2 := buildRawFrame([]byte("lo"))
	data := append(frame1, frame2...)

	r := NewReader(bytes.NewReader(data), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hilo" {
		t.Fatalf("got %q, want %q", got, "hilo")
	}
}

func TestReadChecksum(t *testing.T) {
	frame := buildRawFrameChecksum([]byte("abc"))
	r := NewReader(bytes.NewReader(frame), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" {
		t.Fatalf("got %q, want %q", got, "abc")
	}
}

func TestReadBadChecksum(t *testing.T) {
	frame := buildRawFrameChecksum([]byte("abc"))
	// Corrupt the checksum (last 4 bytes)
	frame[len(frame)-1] ^= 0xFF
	r := NewReader(bytes.NewReader(frame), nil)
	defer func() { _ = r.Close() }()
	_, err := io.ReadAll(r)
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestReadFrameSizeMismatch(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20) // FHD
	buf = append(buf, 0x05) // FCS says 5 bytes
	// But only 3 bytes of content
	bh := uint32(1) | (0 << 1) | (3 << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	buf = append(buf, 'a', 'b', 'c')

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	_, err := io.ReadAll(r)
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestReadMagicMismatch(t *testing.T) {
	r := NewReader(bytes.NewReader([]byte{0x00, 0x00, 0x00, 0x00}), nil)
	defer func() { _ = r.Close() }()
	_, err := io.ReadAll(r)
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestReadEmpty(t *testing.T) {
	r := NewReader(bytes.NewReader(nil), nil)
	defer func() { _ = r.Close() }()
	_, err := io.ReadAll(r)
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF wrapped, got %v", err)
	}
}

func TestReadEmptyFrame(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20) // FHD
	buf = append(buf, 0x00) // FCS = 0
	// Block: last=1, raw, size=0
	buf = append(buf, 0x01, 0x00, 0x00)

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}
}

func TestReadSkippableFrame(t *testing.T) {
	var buf []byte
	// Skippable frame (type 0)
	buf = append(buf, 0x50, 0x2A, 0x4D, 0x18)
	buf = append(buf, 0x03, 0x00, 0x00, 0x00) // skip 3 bytes
	buf = append(buf, 0xAA, 0xBB, 0xCC)
	// Real frame
	buf = append(buf, buildRawFrame([]byte("hi"))...)

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Fatalf("got %q, want %q", got, "hi")
	}
}

func TestReadSmallBuffer(t *testing.T) {
	frame := buildRawFrame([]byte("hello world"))
	r := NewReader(bytes.NewReader(frame), nil)
	defer func() { _ = r.Close() }()

	// Read one byte at a time
	var got []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestReadReset(t *testing.T) {
	frame1 := buildRawFrame([]byte("first"))
	frame2 := buildRawFrame([]byte("second"))

	r := NewReader(bytes.NewReader(frame1), nil)
	defer func() { _ = r.Close() }()

	got1, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got1) != "first" {
		t.Fatalf("got %q, want %q", got1, "first")
	}

	if err := r.Reset(bytes.NewReader(frame2)); err != nil {
		t.Fatal(err)
	}
	got2, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != "second" {
		t.Fatalf("got %q, want %q", got2, "second")
	}
}

func TestReadAfterClose(t *testing.T) {
	frame := buildRawFrame([]byte("test"))
	r := NewReader(bytes.NewReader(frame), nil)
	_ = r.Close()
	_, err := r.Read(make([]byte, 10))
	if err != ErrDecoderClosed {
		t.Fatalf("expected ErrDecoderClosed, got %v", err)
	}
}

func TestReadWindowedFrame(t *testing.T) {
	// Non-single-segment frame with explicit window descriptor
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x00) // FHD: not single segment, no checksum
	buf = append(buf, 0x00) // window descriptor: log=10, base=1024
	bh := uint32(1) | (0 << 1) | (5 << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	buf = append(buf, 'h', 'e', 'l', 'l', 'o')

	r := NewReader(bytes.NewReader(buf), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

func TestNewReaderNilInput(t *testing.T) {
	r := NewReader(nil, nil)
	frame := buildRawFrame([]byte("after nil"))
	if err := r.Reset(bytes.NewReader(frame)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "after nil" {
		t.Fatalf("got %q, want %q", got, "after nil")
	}
	r.Close()
}

func TestReaderResetNilClosesReader(t *testing.T) {
	r := NewReader(bytes.NewReader(buildRawFrame([]byte("x"))), nil)
	if err := r.Reset(nil); err != nil {
		t.Fatal(err)
	}
	_, err := r.Read(make([]byte, 1))
	if err != ErrDecoderClosed {
		t.Fatalf("expected ErrDecoderClosed after Reset(nil), got %v", err)
	}
}

func TestWriteToBasic(t *testing.T) {
	frame := buildRawFrame([]byte("hello"))
	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || buf.String() != "hello" {
		t.Fatalf("got %d %q, want 5 %q", n, buf.String(), "hello")
	}
}

func TestWriteToEmpty(t *testing.T) {
	r := NewReader(bytes.NewReader(nil), nil)
	defer r.Close()
	var buf bytes.Buffer
	_, err := r.WriteTo(&buf)
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF wrapped, got %v", err)
	}
}

func TestWriteToEmptyFrame(t *testing.T) {
	var frame []byte
	frame = append(frame, 0x28, 0xB5, 0x2F, 0xFD)
	frame = append(frame, 0x20)
	frame = append(frame, 0x00)
	frame = append(frame, 0x01, 0x00, 0x00)

	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || buf.Len() != 0 {
		t.Fatalf("got %d bytes, want 0", n)
	}
}

func TestWriteToMultiBlock(t *testing.T) {
	var frame []byte
	frame = append(frame, 0x28, 0xB5, 0x2F, 0xFD)
	frame = append(frame, 0x20)
	frame = append(frame, 0x05)
	bh1 := uint32(0) | (0 << 1) | (3 << 3)
	frame = append(frame, byte(bh1), byte(bh1>>8), byte(bh1>>16))
	frame = append(frame, 'a', 'b', 'c')
	bh2 := uint32(1) | (0 << 1) | (2 << 3)
	frame = append(frame, byte(bh2), byte(bh2>>8), byte(bh2>>16))
	frame = append(frame, 'd', 'e')

	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || buf.String() != "abcde" {
		t.Fatalf("got %d %q, want 5 %q", n, buf.String(), "abcde")
	}
}

func TestWriteToMultiFrame(t *testing.T) {
	data := append(buildRawFrame([]byte("hi")), buildRawFrame([]byte("lo"))...)
	r := NewReader(bytes.NewReader(data), nil)
	defer r.Close()
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || buf.String() != "hilo" {
		t.Fatalf("got %d %q, want 4 %q", n, buf.String(), "hilo")
	}
}

func TestWriteToChecksum(t *testing.T) {
	frame := buildRawFrameChecksum([]byte("abc"))
	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || buf.String() != "abc" {
		t.Fatalf("got %d %q, want 3 %q", n, buf.String(), "abc")
	}
}

func TestWriteToBadChecksum(t *testing.T) {
	frame := buildRawFrameChecksum([]byte("abc"))
	frame[len(frame)-1] ^= 0xFF
	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()
	var buf bytes.Buffer
	_, err := r.WriteTo(&buf)
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestWriteToFrameSizeMismatch(t *testing.T) {
	var frame []byte
	frame = append(frame, 0x28, 0xB5, 0x2F, 0xFD)
	frame = append(frame, 0x20)
	frame = append(frame, 0x05) // FCS says 5
	bh := uint32(1) | (0 << 1) | (3 << 3)
	frame = append(frame, byte(bh), byte(bh>>8), byte(bh>>16))
	frame = append(frame, 'a', 'b', 'c') // only 3 bytes

	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()
	var buf bytes.Buffer
	_, err := r.WriteTo(&buf)
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestWriteToAfterClose(t *testing.T) {
	r := NewReader(bytes.NewReader(buildRawFrame([]byte("test"))), nil)
	r.Close()
	_, err := r.WriteTo(&bytes.Buffer{})
	if err != ErrDecoderClosed {
		t.Fatalf("expected ErrDecoderClosed, got %v", err)
	}
}

func TestWriteToAfterPartialRead(t *testing.T) {
	frame := buildRawFrame([]byte("hello world"))
	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()

	// Read only 5 bytes.
	p := make([]byte, 5)
	n, err := r.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(p[:n]) != "hello" {
		t.Fatalf("partial read: got %q, want %q", p[:n], "hello")
	}

	// WriteTo should drain remaining.
	var buf bytes.Buffer
	wn, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if wn != 6 || buf.String() != " world" {
		t.Fatalf("got %d %q, want 6 %q", wn, buf.String(), " world")
	}
}

type errWriter struct {
	n   int
	err error
}

func (w *errWriter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, w.err
	}
	n := min(len(p), w.n)
	w.n -= n
	return n, nil
}

func TestWriteToWriteError(t *testing.T) {
	frame := buildRawFrame([]byte("hello world"))
	r := NewReader(bytes.NewReader(frame), nil)
	defer r.Close()

	writeErr := errors.New("disk full")
	ew := &errWriter{n: 0, err: writeErr}
	_, err := r.WriteTo(ew)
	if err != writeErr {
		t.Fatalf("expected writeErr, got %v", err)
	}

	// Error should be sticky.
	_, err = r.WriteTo(&bytes.Buffer{})
	if err != writeErr {
		t.Fatalf("expected sticky error, got %v", err)
	}
}

func TestWriteToCompressed(t *testing.T) {
	src := bytes.Repeat([]byte("compressed data for WriteTo test "), 100)
	for level := BestSpeed; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			e := NewEncoder()
			if err := e.SetLevel(level); err != nil {
				t.Fatal(err)
			}
			compressed := e.AppendCompress(nil, src)

			r := NewReader(bytes.NewReader(compressed), nil)
			defer r.Close()
			var buf bytes.Buffer
			_, err := r.WriteTo(&buf)
			if err != nil {
				t.Fatalf("level %d: %v", level, err)
			}
			if !bytes.Equal(buf.Bytes(), src) {
				t.Fatalf("level %d: mismatch: got %d, want %d bytes", level, buf.Len(), len(src))
			}
		})
	}
}

func TestWriteToLarge(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	src := make([]byte, 1<<20)
	rng.Read(src)

	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	r := NewReader(bytes.NewReader(compressed), nil)
	defer r.Close()
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(src)) {
		t.Fatalf("wrote %d, want %d", n, len(src))
	}
	if !bytes.Equal(buf.Bytes(), src) {
		t.Fatal("content mismatch")
	}
}

func TestWriteToDict(t *testing.T) {
	dictContent := bytes.Repeat([]byte("dictionary prefix content here! "), 100)
	src := append([]byte{}, dictContent[500:1500]...)
	src = append(src, []byte("and some unique new content")...)
	src = bytes.Repeat(src, 10)

	t.Run("raw", func(t *testing.T) {
		e := NewEncoder()
		e.SetRawDict(dictContent)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.SetRawDict(dictContent)
		r := NewReader(bytes.NewReader(compressed), dec)
		defer r.Close()
		var buf bytes.Buffer
		_, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf.Bytes(), src) {
			t.Fatal("raw dict mismatch")
		}
	})

	t.Run("parsed", func(t *testing.T) {
		raw, err := os.ReadFile("testdata/d0.dict")
		if err != nil {
			t.Fatal(err)
		}
		d, err := ParseDict(raw)
		if err != nil {
			t.Fatal(err)
		}
		e := NewEncoder()
		e.AddDict(d)
		compressed := e.AppendCompress(nil, src)

		dec := NewDecoder()
		dec.AddDict(d)
		r := NewReader(bytes.NewReader(compressed), dec)
		defer r.Close()
		var buf bytes.Buffer
		_, err = r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf.Bytes(), src) {
			t.Fatal("parsed dict mismatch")
		}
	})
}

func TestReaderReuseAfterClose(t *testing.T) {
	frame1 := buildRawFrame([]byte("before"))
	frame2 := buildRawFrame([]byte("after"))

	r := NewReader(bytes.NewReader(frame1), nil)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before" {
		t.Fatalf("got %q, want %q", got, "before")
	}

	r.Close()

	if err := r.Reset(bytes.NewReader(frame2)); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "after" {
		t.Fatalf("got %q, want %q", got, "after")
	}
}

func TestReaderReuseAfterCloseWriteTo(t *testing.T) {
	frame1 := buildRawFrame([]byte("before"))
	frame2 := buildRawFrame([]byte("after"))

	r := NewReader(bytes.NewReader(frame1), nil)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before" {
		t.Fatalf("got %q, want %q", got, "before")
	}

	r.Close()

	if err := r.Reset(bytes.NewReader(frame2)); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 || buf.String() != "after" {
		t.Fatalf("got %d %q, want 5 %q", n, buf.String(), "after")
	}
}

func TestReaderReuseAfterCloseAppendCompress(t *testing.T) {
	frame1 := buildRawFrame([]byte("before"))
	frame2 := buildRawFrame([]byte("after"))

	dec := NewDecoder()
	r := NewReader(bytes.NewReader(frame1), dec)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before" {
		t.Fatalf("got %q, want %q", got, "before")
	}

	r.Close()

	if err := r.Reset(bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	result, err := dec.AppendDecompress(nil, frame2)
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != "after" {
		t.Fatalf("got %q, want %q", result, "after")
	}
}

func TestReaderReuseAfterCloseMultiple(t *testing.T) {
	r := NewReader(bytes.NewReader(buildRawFrame([]byte("init"))), nil)

	for i := range 5 {
		want := []byte{byte('A' + i), byte('0' + i)}
		frame := buildRawFrame(want)

		if err := r.Reset(bytes.NewReader(frame)); err != nil {
			t.Fatalf("cycle %d reset: %v", i, err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("cycle %d read: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("cycle %d: got %q, want %q", i, got, want)
		}
		r.Close()
	}
}

func TestWriteToMatchesReadAll(t *testing.T) {
	inputs := [][]byte{
		[]byte("hello"),
		bytes.Repeat([]byte("x"), 1000),
		{},
	}
	for _, src := range inputs {
		e := NewEncoder()
		compressed := e.AppendCompress(nil, src)

		// ReadAll
		r1 := NewReader(bytes.NewReader(compressed), nil)
		readResult, err := io.ReadAll(r1)
		r1.Close()
		if err != nil {
			t.Fatal(err)
		}

		// WriteTo
		r2 := NewReader(bytes.NewReader(compressed), nil)
		var buf bytes.Buffer
		_, err = r2.WriteTo(&buf)
		r2.Close()
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(readResult, buf.Bytes()) {
			t.Fatalf("ReadAll vs WriteTo mismatch for input len %d", len(src))
		}
	}
}

func TestWriteToSkippableFrame(t *testing.T) {
	var data []byte
	// Skippable frame (type 0)
	data = append(data, 0x50, 0x2A, 0x4D, 0x18)
	data = append(data, 0x03, 0x00, 0x00, 0x00)
	data = append(data, 0xAA, 0xBB, 0xCC)
	// Real frame
	data = append(data, buildRawFrame([]byte("hi"))...)

	r := NewReader(bytes.NewReader(data), nil)
	defer r.Close()
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || buf.String() != "hi" {
		t.Fatalf("got %d %q, want 2 %q", n, buf.String(), "hi")
	}
}

func TestZeroValueReader(t *testing.T) {
	frame := buildRawFrame([]byte("zero value"))
	var r Reader
	if err := r.Reset(bytes.NewReader(frame)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(&r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "zero value" {
		t.Fatalf("got %q, want %q", got, "zero value")
	}
	r.Close()
}

func TestSetMaxWindowSize(t *testing.T) {
	// Compress with default window (~4 MiB at level 3).
	src := bytes.Repeat([]byte("window size test "), 200)
	enc := NewEncoder()
	compressed := enc.AppendCompress(nil, src)

	// Decoding with a tiny max window should fail.
	d := NewDecoder()
	if err := d.SetMaxWindowSize(MinWindowSize); err != nil {
		t.Fatal(err)
	}
	r := NewReader(bytes.NewReader(compressed), d)
	_, err := io.ReadAll(r)
	r.Close()
	var e *ErrWindowSizeExceeded
	if !errors.As(err, &e) {
		t.Fatalf("expected ErrWindowSizeExceeded, got %v", err)
	}
	if e.Allowed != MinWindowSize {
		t.Fatalf("Allowed = %d, want %d", e.Allowed, MinWindowSize)
	}
}

func TestSetMaxWindowSizeMax(t *testing.T) {
	src := bytes.Repeat([]byte("max window "), 200)
	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	d := NewDecoder()
	if err := d.SetMaxWindowSize(MaxWindowSize); err != nil {
		t.Fatal(err)
	}
	r := NewReader(bytes.NewReader(compressed), d)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestSetMaxWindowSizeValidation(t *testing.T) {
	d := NewDecoder()
	if err := d.SetMaxWindowSize(0); err == nil {
		t.Fatal("expected error for 0")
	}
	if err := d.SetMaxWindowSize(-1); err == nil {
		t.Fatal("expected error for -1")
	}
	if err := d.SetMaxWindowSize(MaxWindowSize + 1); err == nil {
		t.Fatal("expected error for too large")
	}
	if err := d.SetMaxWindowSize(MinWindowSize); err != nil {
		t.Fatalf("MinWindowSize should be valid: %v", err)
	}
	if err := d.SetMaxWindowSize(MaxWindowSize); err != nil {
		t.Fatalf("MaxWindowSize should be valid: %v", err)
	}
}

func TestReadTruncatedBlockHeader(t *testing.T) {
	// Valid frame header, then only 2 of 3 block header bytes.
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20)
	buf = append(buf, 0x05)
	buf = append(buf, 0x01, 0x00) // only 2 bytes of block header

	r := NewReader(bytes.NewReader(buf), nil)
	_, err := io.ReadAll(r)
	r.Close()
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestReadTruncatedBlockData(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20)
	buf = append(buf, 0x05)
	// Block header: last=1, raw, size=5
	bh := uint32(1) | (0 << 1) | (5 << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
	buf = append(buf, 'a', 'b') // only 2 of 5 bytes

	r := NewReader(bytes.NewReader(buf), nil)
	_, err := io.ReadAll(r)
	r.Close()
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestReadReservedBlockType(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	buf = append(buf, 0x20)
	buf = append(buf, 0x00)
	// Block header: last=1, type=reserved(3), size=0
	bh := uint32(1) | (3 << 1) | (0 << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))

	r := NewReader(bytes.NewReader(buf), nil)
	_, err := io.ReadAll(r)
	r.Close()
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestReadWindowSizeTooSmall(t *testing.T) {
	// Frame with window descriptor encoding a size below MinWindowSize is not
	// possible via the encoding (minimum windowLog is 10 = 1024 = MinWindowSize),
	// so test the single-segment path: FCS=0 with SingleSegment set.
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	// FHD: not single segment, no checksum
	buf = append(buf, 0x00)
	// Window descriptor: log=10 base, frac=0 → 1024 (MinWindowSize). Valid.
	buf = append(buf, 0x00)
	bh := uint32(1) | (0 << 1) | (0 << 3)
	buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))

	r := NewReader(bytes.NewReader(buf), nil)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("expected empty")
	}
}

func TestReadReservedFrameHeaderBit(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
	// FHD with reserved bit 3 set.
	buf = append(buf, 0x08)

	r := NewReader(bytes.NewReader(buf), nil)
	_, err := io.ReadAll(r)
	r.Close()
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

type errReader struct {
	data []byte
	off  int
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	if r.off >= len(r.data) {
		return n, r.err
	}
	return n, nil
}

func TestReadIOError(t *testing.T) {
	frame := buildRawFrame([]byte("hello world, this is data"))
	readErr := errors.New("disk read error")

	// Truncate so the reader fails mid-block.
	er := &errReader{data: frame[:len(frame)-5], err: readErr}
	r := NewReader(er, nil)
	_, err := io.ReadAll(r)
	r.Close()
	if err == nil {
		t.Fatal("expected error")
	}

	// Error should be sticky.
	_, err2 := r.Read(make([]byte, 1))
	if err2 == nil {
		t.Fatal("expected sticky error")
	}
}

func TestReadMultipleSkippableFrames(t *testing.T) {
	var buf []byte
	for i := range 3 {
		buf = append(buf, byte(0x50+i), 0x2A, 0x4D, 0x18)
		buf = append(buf, 0x02, 0x00, 0x00, 0x00)
		buf = append(buf, 0xAA, 0xBB)
	}
	buf = append(buf, buildRawFrame([]byte("ok"))...)

	r := NewReader(bytes.NewReader(buf), nil)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestReadOnlySkippableFrames(t *testing.T) {
	var buf []byte
	buf = append(buf, 0x50, 0x2A, 0x4D, 0x18)
	buf = append(buf, 0x01, 0x00, 0x00, 0x00)
	buf = append(buf, 0xFF)

	r := NewReader(bytes.NewReader(buf), nil)
	_, err := io.ReadAll(r)
	r.Close()
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatalf("expected ErrCorrupted, got %v", err)
	}
}

func TestReadSkippableFrameTypes(t *testing.T) {
	for typ := byte(0x50); typ <= 0x5F; typ++ {
		var buf []byte
		buf = append(buf, typ, 0x2A, 0x4D, 0x18)
		buf = append(buf, 0x00, 0x00, 0x00, 0x00) // 0 bytes to skip
		buf = append(buf, buildRawFrame([]byte("x"))...)

		r := NewReader(bytes.NewReader(buf), nil)
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("type 0x%02X: %v", typ, err)
		}
		if string(got) != "x" {
			t.Fatalf("type 0x%02X: got %q", typ, got)
		}
	}
}

func TestAppendDecompressPreExistingDst(t *testing.T) {
	src := []byte("appended after prefix")
	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	var d Decoder
	prefix := []byte("PREFIX:")
	got, err := d.AppendDecompress(prefix, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PREFIX:appended after prefix" {
		t.Fatalf("got %q", got)
	}
}

func TestAppendDecompressConcurrentDifferentData(t *testing.T) {
	e := NewEncoder()
	d := NewDecoder()

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	for i := range goroutines {
		src := bytes.Repeat([]byte{byte('A' + i)}, 5000)
		compressed := e.AppendCompress(nil, src)
		go func() {
			defer wg.Done()
			got, err := d.AppendDecompress(nil, compressed)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, src) {
				errs <- errors.New("mismatch")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestAppendDecompressLargeFrame(t *testing.T) {
	// > 1 MiB to exercise the non-prealloc path in runDecoder.
	rng := rand.New(rand.NewSource(99))
	src := make([]byte, 1<<20+50000)
	rng.Read(src)

	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	var d Decoder
	got, err := d.AppendDecompress(nil, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestAppendDecompressMultiFrameWithCRC(t *testing.T) {
	e := NewEncoder()
	e.SetCRC(true)
	frame1 := e.AppendCompress(nil, []byte("frame one "))
	frame2 := e.AppendCompress(nil, []byte("frame two"))
	combined := append(frame1, frame2...)

	var d Decoder
	got, err := d.AppendDecompress(nil, combined)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "frame one frame two" {
		t.Fatalf("got %q", got)
	}
}

func TestWriteToCompressedMultiBlock(t *testing.T) {
	// > 128KB to force multiple blocks.
	src := make([]byte, maxCompressedBlockSize*2+5000)
	for i := range src {
		src[i] = byte(i % 251)
	}
	for level := BestSpeed; level <= BestCompression; level++ {
		e := NewEncoder()
		_ = e.SetLevel(level)
		compressed := e.AppendCompress(nil, src)

		r := NewReader(bytes.NewReader(compressed), nil)
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		r.Close()
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		if n != int64(len(src)) {
			t.Fatalf("level %d: wrote %d, want %d", level, n, len(src))
		}
		if !bytes.Equal(buf.Bytes(), src) {
			t.Fatalf("level %d: mismatch", level)
		}
	}
}

func TestWriteToPartialWriteError(t *testing.T) {
	src := bytes.Repeat([]byte("partial write test "), 500)
	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	writeErr := errors.New("out of space")
	r := NewReader(bytes.NewReader(compressed), nil)
	// Accept 0 bytes — fail immediately.
	ew := &errWriter{n: 0, err: writeErr}
	_, err := r.WriteTo(ew)
	r.Close()
	if err != writeErr {
		t.Fatalf("expected writeErr, got %v", err)
	}
}

// TestSetMaxSizeStreamingRead covers the running per-block check on the streaming
// Read path and its sticky-error behavior.
func TestSetMaxSizeStreamingRead(t *testing.T) {
	src := bytes.Repeat([]byte("streaming decode bomb guard "), 12000) // multi-block
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	compressed := buf.Bytes()

	d := NewDecoder()
	if err := d.SetMaxSize(int64(len(src)) / 2); err != nil {
		t.Fatal(err)
	}
	r := NewReader(bytes.NewReader(compressed), d)
	_, err := io.ReadAll(r)
	var de *ErrDecodedSizeExceeded
	if !errors.As(err, &de) {
		t.Fatalf("expected ErrDecodedSizeExceeded, got %v", err)
	}
	// The error is sticky until Reset.
	if _, err := r.Read(make([]byte, 8)); !errors.As(err, &de) {
		t.Fatalf("limit error must be sticky, got %v", err)
	}
	_ = r.Close()

	// Raising the limit on the same decoder and resetting the reader succeeds.
	if err := d.SetMaxSize(int64(len(src))); err != nil {
		t.Fatal(err)
	}
	r = NewReader(bytes.NewReader(compressed), d)
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

// TestSetMaxSizeWriteToNoFlush verifies an over-limit frame produces no output
// through WriteTo (the guard runs before the block is written).
func TestSetMaxSizeWriteToNoFlush(t *testing.T) {
	src := bytes.Repeat([]byte("x"), 4000)
	compressed := NewEncoder().AppendCompress(nil, src)

	d := NewDecoder()
	if err := d.SetMaxSize(int64(len(src)) - 1); err != nil {
		t.Fatal(err)
	}
	r := NewReader(bytes.NewReader(compressed), d)
	var out bytes.Buffer
	n, err := r.WriteTo(&out)
	_ = r.Close()
	var de *ErrDecodedSizeExceeded
	if !errors.As(err, &de) {
		t.Fatalf("expected ErrDecodedSizeExceeded, got %v", err)
	}
	if n != 0 || out.Len() != 0 {
		t.Fatalf("over-budget data leaked to writer: n=%d out=%d", n, out.Len())
	}
}

// TestSetMaxSizeMultiFrame verifies the limit is cumulative across concatenated
// frames within a single AppendDecompress call.
func TestSetMaxSizeMultiFrame(t *testing.T) {
	part := bytes.Repeat([]byte("frame "), 500)
	e := NewEncoder()
	var concatenated []byte
	concatenated = e.AppendCompress(concatenated, part)
	concatenated = e.AppendCompress(concatenated, part)

	d := NewDecoder()
	// Enough for one frame but not both.
	if err := d.SetMaxSize(int64(len(part)) + 100); err != nil {
		t.Fatal(err)
	}
	_, err := d.AppendDecompress(nil, concatenated)
	var de *ErrDecodedSizeExceeded
	if !errors.As(err, &de) {
		t.Fatalf("expected cumulative ErrDecodedSizeExceeded, got %v", err)
	}

	// Enough for both frames succeeds.
	if err := d.SetMaxSize(int64(2 * len(part))); err != nil {
		t.Fatal(err)
	}
	got, err := d.AppendDecompress(nil, concatenated)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, part...), part...)
	if !bytes.Equal(got, want) {
		t.Fatal("mismatch")
	}
}

func TestDecoderReuseAcrossReaders(t *testing.T) {
	e := NewEncoder()
	uno := bytes.Repeat([]byte("uno "), 300)
	dos := bytes.Repeat([]byte("dos "), 300)
	f1 := e.AppendCompress(nil, uno)
	f2 := e.AppendCompress(nil, dos)

	d := NewDecoder()
	for _, tc := range []struct{ frame, want []byte }{{f1, uno}, {f2, dos}} {
		r := NewReader(bytes.NewReader(tc.frame), d)
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Fatal("mismatch")
		}
	}
}

// TestDecoderReconfigureBetweenStreams tightens then loosens the max window on a
// bound Decoder between Reset cycles and verifies the new limit is honored.
func TestDecoderReconfigureBetweenStreams(t *testing.T) {
	src := bytes.Repeat([]byte("decoder reconfigure "), 300)
	compressed := NewEncoder().AppendCompress(nil, src)

	d := NewDecoder()
	r := NewReader(bytes.NewReader(compressed), d)

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}

	// Tighten the SAME decoder, reuse the SAME reader: must now fail.
	if err := d.SetMaxWindowSize(MinWindowSize); err != nil {
		t.Fatal(err)
	}
	if err := r.Reset(bytes.NewReader(compressed)); err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	var we *ErrWindowSizeExceeded
	if !errors.As(err, &we) {
		t.Fatalf("expected ErrWindowSizeExceeded after tightening, got %v", err)
	}

	// Loosen again: succeeds.
	if err := d.SetMaxWindowSize(MaxWindowSize); err != nil {
		t.Fatal(err)
	}
	if err := r.Reset(bytes.NewReader(compressed)); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}
