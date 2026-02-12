// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"
)

func compressOneShot(t testing.TB, w *Writer, src []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func roundTrip(t *testing.T, src []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, err := w.Write(src)
	if err != nil {
		t.Fatal("write:", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal("new reader:", err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal("readall:", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(src))
	}
}

func TestWriterEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Empty close with no writes produces nothing.
	if buf.Len() != 0 {
		t.Fatalf("expected 0 bytes, got %d", buf.Len())
	}
}

func TestWriterOneByte(t *testing.T) {
	roundTrip(t, []byte{42})
}

func TestWriterSmall(t *testing.T) {
	roundTrip(t, []byte("hello, zstd world!"))
}

func TestWriterMedium(t *testing.T) {
	src := make([]byte, 1000)
	for i := range src {
		src[i] = byte(i % 251)
	}
	roundTrip(t, src)
}

func TestWriterBlockBoundary(t *testing.T) {
	// Exactly one block.
	src := make([]byte, maxCompressedBlockSize)
	for i := range src {
		src[i] = byte(i * 7)
	}
	roundTrip(t, src)
}

func TestWriterMultiBlock(t *testing.T) {
	// More than one block.
	src := make([]byte, maxCompressedBlockSize+1000)
	for i := range src {
		src[i] = byte(i * 3)
	}
	roundTrip(t, src)
}

func TestWriterLarge(t *testing.T) {
	src := make([]byte, 1<<20) // 1MB
	rng := rand.New(rand.NewPCG(12345, 67890))
	for i := range src {
		src[i] = byte(rng.IntN(256))
	}
	roundTrip(t, src)
}

func TestWriterCompressible(t *testing.T) {
	// Highly compressible data.
	src := bytes.Repeat([]byte("ABCDEFGH"), 10000)
	roundTrip(t, src)
}

func TestWriterAllZeros(t *testing.T) {
	roundTrip(t, make([]byte, 50000))
}

func TestWriterMultipleSmallWrites(t *testing.T) {
	src := []byte("the quick brown fox jumps over the lazy dog, repeatedly. ")
	src = bytes.Repeat(src, 100)

	var buf bytes.Buffer
	w := NewWriter(&buf)
	for i := 0; i < len(src); i += 7 {
		end := min(i+7, len(src))
		_, err := w.Write(src[i:end])
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestWriterFlush(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	part1 := []byte("first part of data. ")
	part2 := []byte("second part of data.")

	_, err := w.Write(part1)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	// After flush, some bytes should have been written.
	if buf.Len() == 0 {
		t.Fatal("expected output after flush")
	}

	_, err = w.Write(part2)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	want := append(part1, part2...)
	if !bytes.Equal(got, want) {
		t.Fatal("mismatch after flush")
	}
}

func TestWriterReset(t *testing.T) {
	src1 := []byte("first stream content")
	src2 := []byte("second stream content after reset")

	var buf1, buf2 bytes.Buffer
	w := NewWriter(&buf1)
	_, _ = w.Write(src1)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w.Reset(&buf2)
	_, _ = w.Write(src2)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify both streams.
	for i, tc := range []struct {
		compressed []byte
		want       []byte
	}{
		{buf1.Bytes(), src1},
		{buf2.Bytes(), src2},
	} {
		r, err := NewReader(bytes.NewReader(tc.compressed))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Fatalf("stream %d: mismatch", i)
		}
	}
}

func TestWriterWriteAfterClose(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, _ = w.Write([]byte("data"))
	_ = w.Close()

	_, err := w.Write([]byte("more"))
	if err != ErrEncoderClosed {
		t.Fatalf("expected ErrEncoderClosed, got %v", err)
	}
}

func TestWriterDoubleClose(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, _ = w.Write([]byte("data"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Second close should not panic or error.
	if err := w.Close(); err != nil {
		t.Fatal("second close:", err)
	}
}

func TestWriterSetLevel(t *testing.T) {
	w := NewWriter(nil)
	if err := w.SetLevel(DefaultCompression); err != nil {
		t.Fatal(err)
	}
	if err := w.SetLevel(NoCompression); err != nil {
		t.Fatal(err)
	}
	if err := w.SetLevel(BestSpeed); err != nil {
		t.Fatal(err)
	}
	if err := w.SetLevel(BestCompression); err != nil {
		t.Fatal(err)
	}
	if err := w.SetLevel(10); err == nil {
		t.Fatal("expected error for invalid level")
	}
	if err := w.SetLevel(-2); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestWriterReadFrom(t *testing.T) {
	src := bytes.Repeat([]byte("ReadFrom test data! "), 1000)

	var buf bytes.Buffer
	w := NewWriter(&buf)
	n, err := w.ReadFrom(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(src)) {
		t.Fatalf("ReadFrom returned %d, want %d", n, len(src))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("ReadFrom round-trip mismatch")
	}
}

func TestWriterCRCEnabled(t *testing.T) {
	src := []byte("checksum test data")
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, _ = w.Write(src)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify readable (CRC checked by reader).
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestWriterNoCRC(t *testing.T) {
	src := []byte("no checksum test data, slightly longer to be interesting")
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.SetCRC(false)
	w.Reset(&buf)
	_, _ = w.Write(src)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestWriterWriteCountCorrect(t *testing.T) {
	src := []byte("verify write count returns correct values")
	var buf bytes.Buffer
	w := NewWriter(&buf)

	n, err := w.Write(src[:10])
	if err != nil || n != 10 {
		t.Fatalf("Write(10) = %d, %v", n, err)
	}
	n, err = w.Write(src[10:])
	if err != nil || n != len(src)-10 {
		t.Fatalf("Write(rest) = %d, %v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterSingleByteWrites(t *testing.T) {
	src := []byte("byte by byte writing test, long enough to have content")

	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, b := range src {
		_, err := w.Write([]byte{b})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestWriterSmallBuffer(t *testing.T) {
	src := bytes.Repeat([]byte("small buffer read test "), 200)

	var buf bytes.Buffer
	w := NewWriter(&buf)
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
	defer func() { _ = r.Close() }()

	// Read in small chunks.
	var got []byte
	tmp := make([]byte, 13)
	for {
		n, err := r.Read(tmp)
		got = append(got, tmp[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func roundTripLevel(t *testing.T, src []byte, level int) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.SetLevel(level); err != nil {
		t.Fatal(err)
	}
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal("write:", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal("new reader:", err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("readall (level %d): %v", level, err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("round-trip mismatch at level %d: got %d bytes, want %d", level, len(got), len(src))
	}
}

func TestAllLevelsSmall(t *testing.T) {
	src := []byte("the quick brown fox jumps over the lazy dog, repeatedly! ")
	src = bytes.Repeat(src, 50)
	for level := NoCompression; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			roundTripLevel(t, src, level)
		})
	}
	// DefaultCompression
	roundTripLevel(t, src, DefaultCompression)
}

func TestAllLevelsMedium(t *testing.T) {
	src := make([]byte, 50000)
	rng := rand.New(rand.NewPCG(42, 99))
	for i := range src {
		src[i] = byte(rng.IntN(64))
	}
	for level := NoCompression; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			roundTripLevel(t, src, level)
		})
	}
}

func TestAllLevelsLarge(t *testing.T) {
	src := make([]byte, 1<<20)
	rng := rand.New(rand.NewPCG(7777, 8888))
	for i := range src {
		src[i] = byte(rng.IntN(256))
	}
	for level := NoCompression; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			roundTripLevel(t, src, level)
		})
	}
}

func TestAllLevelsCompressible(t *testing.T) {
	src := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), 5000)
	for level := NoCompression; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			roundTripLevel(t, src, level)
		})
	}
}

func TestAllLevelsAllZeros(t *testing.T) {
	src := make([]byte, 100000)
	for level := NoCompression; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			roundTripLevel(t, src, level)
		})
	}
}

func TestAllLevelsMultiBlock(t *testing.T) {
	src := make([]byte, maxCompressedBlockSize*2+5000)
	for i := range src {
		src[i] = byte(i % 251)
	}
	for level := 1; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			roundTripLevel(t, src, level)
		})
	}
}

func TestAllLevelsOneByte(t *testing.T) {
	src := []byte{42}
	for level := NoCompression; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			roundTripLevel(t, src, level)
		})
	}
}

func TestLevelSwitching(t *testing.T) {
	src := bytes.Repeat([]byte("level switching test "), 200)
	w := NewWriter(nil)
	for level := 1; level <= BestCompression; level++ {
		var buf bytes.Buffer
		_ = w.SetLevel(level)
		w.Reset(&buf)
		_, _ = w.Write(src)
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
			t.Fatalf("level %d: %v", level, err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("level %d: mismatch", level)
		}
	}
}

func TestZeroValueWriterStream(t *testing.T) {
	src := bytes.Repeat([]byte("zero stream "), 100)
	var buf bytes.Buffer
	var w Writer
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
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

