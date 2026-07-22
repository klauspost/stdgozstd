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

func TestReader_Read(t *testing.T) {
	t.Run("raw_block", func(t *testing.T) {
		frame := buildRawFrame([]byte("hello"))
		r := mustReader(t, bytes.NewReader(frame))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello" {
			t.Fatalf("got %q, want %q", got, "hello")
		}
	})

	t.Run("rle_block", func(t *testing.T) {
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD) // magic
		buf = append(buf, 0x20)                   // FHD single segment
		buf = append(buf, 0x05)                   // FCS = 5
		// Block header: last=1, type=RLE(1), size=5
		bh := uint32(1) | (1 << 1) | (5 << 3)
		buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
		buf = append(buf, 'A') // repeated byte

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "AAAAA" {
			t.Fatalf("got %q, want %q", got, "AAAAA")
		}
	})

	t.Run("compressed_raw_literals", func(t *testing.T) {
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

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello" {
			t.Fatalf("got %q, want %q", got, "hello")
		}
	})

	t.Run("compressed_rle_literals", func(t *testing.T) {
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

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "BBBBB" {
			t.Fatalf("got %q, want %q", got, "BBBBB")
		}
	})

	t.Run("multi_block", func(t *testing.T) {
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

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "abcde" {
			t.Fatalf("got %q, want %q", got, "abcde")
		}
	})

	t.Run("multi_frame", func(t *testing.T) {
		frame1 := buildRawFrame([]byte("hi"))
		frame2 := buildRawFrame([]byte("lo"))
		data := append(frame1, frame2...)

		r := mustReader(t, bytes.NewReader(data))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hilo" {
			t.Fatalf("got %q, want %q", got, "hilo")
		}
	})

	t.Run("checksum", func(t *testing.T) {
		frame := buildRawFrameChecksum([]byte("abc"))
		r := mustReader(t, bytes.NewReader(frame))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "abc" {
			t.Fatalf("got %q, want %q", got, "abc")
		}
	})

	t.Run("empty_frame", func(t *testing.T) {
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
		buf = append(buf, 0x20) // FHD
		buf = append(buf, 0x00) // FCS = 0
		// Block: last=1, raw, size=0
		buf = append(buf, 0x01, 0x00, 0x00)

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty, got %d bytes", len(got))
		}
	})

	t.Run("windowed_frame", func(t *testing.T) {
		// Non-single-segment frame with explicit window descriptor
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
		buf = append(buf, 0x00) // FHD: not single segment, no checksum
		buf = append(buf, 0x00) // window descriptor: log=10, base=1024
		bh := uint32(1) | (0 << 1) | (5 << 3)
		buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
		buf = append(buf, 'h', 'e', 'l', 'l', 'o')

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello" {
			t.Fatalf("got %q, want %q", got, "hello")
		}
	})

	t.Run("min_window_size", func(t *testing.T) {
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

		r := mustReader(t, bytes.NewReader(buf))
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatal("expected empty")
		}
	})

	t.Run("small_buffer", func(t *testing.T) {
		frame := buildRawFrame([]byte("hello world"))
		r := mustReader(t, bytes.NewReader(frame))
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
	})
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

func TestReader_Read_Errors(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		r := mustReader(t, bytes.NewReader(nil))
		defer func() { _ = r.Close() }()
		_, err := io.ReadAll(r)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected io.ErrUnexpectedEOF wrapped, got %v", err)
		}
	})

	t.Run("bad_checksum", func(t *testing.T) {
		frame := buildRawFrameChecksum([]byte("abc"))
		// Corrupt the checksum (last 4 bytes)
		frame[len(frame)-1] ^= 0xFF
		r := mustReader(t, bytes.NewReader(frame))
		defer func() { _ = r.Close() }()
		_, err := io.ReadAll(r)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("frame_size_mismatch", func(t *testing.T) {
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
		buf = append(buf, 0x20) // FHD
		buf = append(buf, 0x05) // FCS says 5 bytes
		// But only 3 bytes of content
		bh := uint32(1) | (0 << 1) | (3 << 3)
		buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
		buf = append(buf, 'a', 'b', 'c')

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		_, err := io.ReadAll(r)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("magic_mismatch", func(t *testing.T) {
		r := mustReader(t, bytes.NewReader([]byte{0x00, 0x00, 0x00, 0x00}))
		defer func() { _ = r.Close() }()
		_, err := io.ReadAll(r)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("truncated_block_header", func(t *testing.T) {
		// Valid frame header, then only 2 of 3 block header bytes.
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
		buf = append(buf, 0x20)
		buf = append(buf, 0x05)
		buf = append(buf, 0x01, 0x00) // only 2 bytes of block header

		r := mustReader(t, bytes.NewReader(buf))
		_, err := io.ReadAll(r)
		r.Close()
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("truncated_block_data", func(t *testing.T) {
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
		buf = append(buf, 0x20)
		buf = append(buf, 0x05)
		// Block header: last=1, raw, size=5
		bh := uint32(1) | (0 << 1) | (5 << 3)
		buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))
		buf = append(buf, 'a', 'b') // only 2 of 5 bytes

		r := mustReader(t, bytes.NewReader(buf))
		_, err := io.ReadAll(r)
		r.Close()
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("reserved_block_type", func(t *testing.T) {
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
		buf = append(buf, 0x20)
		buf = append(buf, 0x00)
		// Block header: last=1, type=reserved(3), size=0
		bh := uint32(1) | (3 << 1) | (0 << 3)
		buf = append(buf, byte(bh), byte(bh>>8), byte(bh>>16))

		r := mustReader(t, bytes.NewReader(buf))
		_, err := io.ReadAll(r)
		r.Close()
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("reserved_frame_header_bit", func(t *testing.T) {
		var buf []byte
		buf = append(buf, 0x28, 0xB5, 0x2F, 0xFD)
		// FHD with reserved bit 3 set.
		buf = append(buf, 0x08)

		r := mustReader(t, bytes.NewReader(buf))
		_, err := io.ReadAll(r)
		r.Close()
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("only_skippable", func(t *testing.T) {
		var buf []byte
		buf = append(buf, 0x50, 0x2A, 0x4D, 0x18)
		buf = append(buf, 0x01, 0x00, 0x00, 0x00)
		buf = append(buf, 0xFF)

		r := mustReader(t, bytes.NewReader(buf))
		_, err := io.ReadAll(r)
		r.Close()
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("io_error", func(t *testing.T) {
		frame := buildRawFrame([]byte("hello world, this is data"))
		readErr := errors.New("disk read error")

		// Truncate so the reader fails mid-block.
		er := &errReader{data: frame[:len(frame)-5], err: readErr}
		r := mustReader(t, er)
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
	})
}

func TestReader_Read_Skippable(t *testing.T) {
	t.Run("single", func(t *testing.T) {
		var buf []byte
		// Skippable frame (type 0)
		buf = append(buf, 0x50, 0x2A, 0x4D, 0x18)
		buf = append(buf, 0x03, 0x00, 0x00, 0x00) // skip 3 bytes
		buf = append(buf, 0xAA, 0xBB, 0xCC)
		// Real frame
		buf = append(buf, buildRawFrame([]byte("hi"))...)

		r := mustReader(t, bytes.NewReader(buf))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hi" {
			t.Fatalf("got %q, want %q", got, "hi")
		}
	})

	t.Run("multiple", func(t *testing.T) {
		var buf []byte
		for i := range 3 {
			buf = append(buf, byte(0x50+i), 0x2A, 0x4D, 0x18)
			buf = append(buf, 0x02, 0x00, 0x00, 0x00)
			buf = append(buf, 0xAA, 0xBB)
		}
		buf = append(buf, buildRawFrame([]byte("ok"))...)

		r := mustReader(t, bytes.NewReader(buf))
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "ok" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("types", func(t *testing.T) {
		for typ := byte(0x50); typ <= 0x5F; typ++ {
			var buf []byte
			buf = append(buf, typ, 0x2A, 0x4D, 0x18)
			buf = append(buf, 0x00, 0x00, 0x00, 0x00) // 0 bytes to skip
			buf = append(buf, buildRawFrame([]byte("x"))...)

			r := mustReader(t, bytes.NewReader(buf))
			got, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				t.Fatalf("type 0x%02X: %v", typ, err)
			}
			if string(got) != "x" {
				t.Fatalf("type 0x%02X: got %q", typ, got)
			}
		}
	})
}

func TestReader_Read_AfterClose(t *testing.T) {
	frame := buildRawFrame([]byte("test"))
	r := mustReader(t, bytes.NewReader(frame))
	_ = r.Close()
	_, err := r.Read(make([]byte, 10))
	if err != ErrDecoderClosed {
		t.Fatalf("expected ErrDecoderClosed, got %v", err)
	}
}

func TestReader_WriteTo(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		frame := buildRawFrame([]byte("hello"))
		r := mustReader(t, bytes.NewReader(frame))
		defer r.Close()
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 5 || buf.String() != "hello" {
			t.Fatalf("got %d %q, want 5 %q", n, buf.String(), "hello")
		}
	})

	t.Run("empty_frame", func(t *testing.T) {
		var frame []byte
		frame = append(frame, 0x28, 0xB5, 0x2F, 0xFD)
		frame = append(frame, 0x20)
		frame = append(frame, 0x00)
		frame = append(frame, 0x01, 0x00, 0x00)

		r := mustReader(t, bytes.NewReader(frame))
		defer r.Close()
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 || buf.Len() != 0 {
			t.Fatalf("got %d bytes, want 0", n)
		}
	})

	t.Run("multi_block", func(t *testing.T) {
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

		r := mustReader(t, bytes.NewReader(frame))
		defer r.Close()
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 5 || buf.String() != "abcde" {
			t.Fatalf("got %d %q, want 5 %q", n, buf.String(), "abcde")
		}
	})

	t.Run("multi_frame", func(t *testing.T) {
		data := append(buildRawFrame([]byte("hi")), buildRawFrame([]byte("lo"))...)
		r := mustReader(t, bytes.NewReader(data))
		defer r.Close()
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 4 || buf.String() != "hilo" {
			t.Fatalf("got %d %q, want 4 %q", n, buf.String(), "hilo")
		}
	})

	t.Run("checksum", func(t *testing.T) {
		frame := buildRawFrameChecksum([]byte("abc"))
		r := mustReader(t, bytes.NewReader(frame))
		defer r.Close()
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 3 || buf.String() != "abc" {
			t.Fatalf("got %d %q, want 3 %q", n, buf.String(), "abc")
		}
	})

	t.Run("compressed", func(t *testing.T) {
		src := bytes.Repeat([]byte("compressed data for WriteTo test "), 100)
		for level := BestSpeed; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				e := mustEncoder(t, WithEncoderLevel(level))
				compressed := e.AppendCompress(nil, src)

				r := mustReader(t, bytes.NewReader(compressed))
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
	})

	t.Run("compressed_multi_block", func(t *testing.T) {
		// > 128KB to force multiple blocks.
		src := make([]byte, maxCompressedBlockSize*2+5000)
		for i := range src {
			src[i] = byte(i % 251)
		}
		for level := BestSpeed; level <= BestCompression; level++ {
			e := mustEncoder(t, WithEncoderLevel(level))
			compressed := e.AppendCompress(nil, src)

			r := mustReader(t, bytes.NewReader(compressed))
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
	})

	t.Run("large", func(t *testing.T) {
		rng := rand.New(rand.NewSource(42))
		src := make([]byte, 1<<20)
		rng.Read(src)

		e := mustEncoder(t)
		compressed := e.AppendCompress(nil, src)

		r := mustReader(t, bytes.NewReader(compressed))
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
	})

	t.Run("dict", func(t *testing.T) {
		dictContent := bytes.Repeat([]byte("dictionary prefix content here! "), 100)
		src := append([]byte{}, dictContent[500:1500]...)
		src = append(src, []byte("and some unique new content")...)
		src = bytes.Repeat(src, 10)

		t.Run("raw", func(t *testing.T) {
			e := mustEncoder(t, WithEncoderRawDict(dictContent))
			compressed := e.AppendCompress(nil, src)

			r := mustReader(t, bytes.NewReader(compressed), WithDecoderRawDict(dictContent))
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
			e := mustEncoder(t, WithEncoderDict(d))
			compressed := e.AppendCompress(nil, src)

			r := mustReader(t, bytes.NewReader(compressed), WithDecoderDict(d))
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
	})

	t.Run("skippable", func(t *testing.T) {
		var data []byte
		// Skippable frame (type 0)
		data = append(data, 0x50, 0x2A, 0x4D, 0x18)
		data = append(data, 0x03, 0x00, 0x00, 0x00)
		data = append(data, 0xAA, 0xBB, 0xCC)
		// Real frame
		data = append(data, buildRawFrame([]byte("hi"))...)

		r := mustReader(t, bytes.NewReader(data))
		defer r.Close()
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 || buf.String() != "hi" {
			t.Fatalf("got %d %q, want 2 %q", n, buf.String(), "hi")
		}
	})

	t.Run("matches_read_all", func(t *testing.T) {
		inputs := [][]byte{
			[]byte("hello"),
			bytes.Repeat([]byte("x"), 1000),
			{},
		}
		for _, src := range inputs {
			e := mustEncoder(t)
			compressed := e.AppendCompress(nil, src)

			// ReadAll
			r1 := mustReader(t, bytes.NewReader(compressed))
			readResult, err := io.ReadAll(r1)
			r1.Close()
			if err != nil {
				t.Fatal(err)
			}

			// WriteTo
			r2 := mustReader(t, bytes.NewReader(compressed))
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
	})

	t.Run("after_partial_read", func(t *testing.T) {
		frame := buildRawFrame([]byte("hello world"))
		r := mustReader(t, bytes.NewReader(frame))
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
	})
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

func TestReader_WriteTo_Errors(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		r := mustReader(t, bytes.NewReader(nil))
		defer r.Close()
		var buf bytes.Buffer
		_, err := r.WriteTo(&buf)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected io.ErrUnexpectedEOF wrapped, got %v", err)
		}
	})

	t.Run("bad_checksum", func(t *testing.T) {
		frame := buildRawFrameChecksum([]byte("abc"))
		frame[len(frame)-1] ^= 0xFF
		r := mustReader(t, bytes.NewReader(frame))
		defer r.Close()
		var buf bytes.Buffer
		_, err := r.WriteTo(&buf)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("frame_size_mismatch", func(t *testing.T) {
		var frame []byte
		frame = append(frame, 0x28, 0xB5, 0x2F, 0xFD)
		frame = append(frame, 0x20)
		frame = append(frame, 0x05) // FCS says 5
		bh := uint32(1) | (0 << 1) | (3 << 3)
		frame = append(frame, byte(bh), byte(bh>>8), byte(bh>>16))
		frame = append(frame, 'a', 'b', 'c') // only 3 bytes

		r := mustReader(t, bytes.NewReader(frame))
		defer r.Close()
		var buf bytes.Buffer
		_, err := r.WriteTo(&buf)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted, got %v", err)
		}
	})

	t.Run("after_close", func(t *testing.T) {
		r := mustReader(t, bytes.NewReader(buildRawFrame([]byte("test"))))
		r.Close()
		_, err := r.WriteTo(&bytes.Buffer{})
		if err != ErrDecoderClosed {
			t.Fatalf("expected ErrDecoderClosed, got %v", err)
		}
	})

	t.Run("write_error", func(t *testing.T) {
		frame := buildRawFrame([]byte("hello world"))
		r := mustReader(t, bytes.NewReader(frame))
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
	})

	t.Run("partial_write_error", func(t *testing.T) {
		src := bytes.Repeat([]byte("partial write test "), 500)
		e := mustEncoder(t)
		compressed := e.AppendCompress(nil, src)

		writeErr := errors.New("out of space")
		r := mustReader(t, bytes.NewReader(compressed))
		// Accept 0 bytes — fail immediately.
		ew := &errWriter{n: 0, err: writeErr}
		_, err := r.WriteTo(ew)
		r.Close()
		if err != writeErr {
			t.Fatalf("expected writeErr, got %v", err)
		}
	})
}

func TestReader_Reset(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		frame1 := buildRawFrame([]byte("first"))
		frame2 := buildRawFrame([]byte("second"))

		r := mustReader(t, bytes.NewReader(frame1))
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
	})

	t.Run("nil_input", func(t *testing.T) {
		r := mustReader(t, nil)
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
	})

	t.Run("nil_closes", func(t *testing.T) {
		r := mustReader(t, bytes.NewReader(buildRawFrame([]byte("x"))))
		if err := r.Reset(nil); err != nil {
			t.Fatal(err)
		}
		_, err := r.Read(make([]byte, 1))
		if err != ErrDecoderClosed {
			t.Fatalf("expected ErrDecoderClosed after Reset(nil), got %v", err)
		}
	})

	t.Run("reuse_after_close", func(t *testing.T) {
		frame1 := buildRawFrame([]byte("before"))
		frame2 := buildRawFrame([]byte("after"))

		r := mustReader(t, bytes.NewReader(frame1))
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
	})

	t.Run("reuse_after_close_writeto", func(t *testing.T) {
		frame1 := buildRawFrame([]byte("before"))
		frame2 := buildRawFrame([]byte("after"))

		r := mustReader(t, bytes.NewReader(frame1))
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
	})

	t.Run("reuse_after_close_append_decompress", func(t *testing.T) {
		frame1 := buildRawFrame([]byte("before"))
		frame2 := buildRawFrame([]byte("after"))

		dec := mustDecoder(t)
		r := mustReader(t, bytes.NewReader(frame1))
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
	})

	t.Run("reuse_multiple", func(t *testing.T) {
		r := mustReader(t, bytes.NewReader(buildRawFrame([]byte("init"))))

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
	})

	t.Run("change_max_size", func(t *testing.T) {
		payload := bytes.Repeat([]byte("x"), 4096)
		frame := mustEncoder(t).AppendCompress(nil, payload)

		// A small limit rejects the stream.
		r := mustReader(t, bytes.NewReader(frame), WithDecoderMaxSize(100))
		defer func() { _ = r.Close() }()
		var de *ErrDecodedSizeExceeded
		if _, err := io.ReadAll(r); !errors.As(err, &de) {
			t.Fatalf("expected ErrDecodedSizeExceeded, got %v", err)
		}

		// Resetting with a larger limit accepts the same stream.
		if err := r.Reset(bytes.NewReader(frame), WithDecoderMaxSize(int64(len(payload)))); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("mismatch")
		}
	})

	t.Run("invalid_option", func(t *testing.T) {
		r := mustReader(t, nil)
		if err := r.Reset(bytes.NewReader(nil), WithDecoderMaxSize(-1)); err == nil {
			t.Fatal("expected error for negative max size")
		}
	})
}

func TestReader_ZeroValue(t *testing.T) {
	t.Run("read", func(t *testing.T) {
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
	})

	t.Run("write_to", func(t *testing.T) {
		frame := buildRawFrame([]byte("zero writeto"))
		var r Reader
		if err := r.Reset(bytes.NewReader(frame)); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if n != 12 || buf.String() != "zero writeto" {
			t.Fatalf("got %d %q", n, buf.String())
		}
		r.Close()
	})
}

func TestReader_MaxWindowExceeded(t *testing.T) {
	// Compress with default window (~4 MiB at level 3).
	src := bytes.Repeat([]byte("window size test "), 200)
	enc := mustEncoder(t)
	compressed := enc.AppendCompress(nil, src)

	// Decoding with a tiny max window should fail.
	r := mustReader(t, bytes.NewReader(compressed), WithDecoderMaxWindow(MinWindowSize))
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

func TestReader_MaxWindowAllowed(t *testing.T) {
	src := bytes.Repeat([]byte("max window "), 200)
	e := mustEncoder(t)
	compressed := e.AppendCompress(nil, src)

	r := mustReader(t, bytes.NewReader(compressed), WithDecoderMaxWindow(MaxWindowSize))
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

// TestWithDecoderMaxSizeStreamingRead covers the running per-block check on the streaming
// Read path and its sticky-error behavior.
func TestWithDecoderMaxSizeStreamingRead(t *testing.T) {
	src := bytes.Repeat([]byte("streaming decode bomb guard "), 12000) // multi-block
	var buf bytes.Buffer
	w := mustWriter(t, &buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	compressed := buf.Bytes()

	r := mustReader(t, bytes.NewReader(compressed), WithDecoderMaxSize(int64(len(src))/2))
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

	// A fresh reader with a higher limit succeeds.
	r = mustReader(t, bytes.NewReader(compressed), WithDecoderMaxSize(int64(len(src))))
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

// TestWithDecoderMaxSizeWriteToNoFlush verifies an over-limit frame produces no output
// through WriteTo (the guard runs before the block is written).
func TestWithDecoderMaxSizeWriteToNoFlush(t *testing.T) {
	src := bytes.Repeat([]byte("x"), 4000)
	compressed := mustEncoder(t).AppendCompress(nil, src)

	r := mustReader(t, bytes.NewReader(compressed), WithDecoderMaxSize(int64(len(src))-1))
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
