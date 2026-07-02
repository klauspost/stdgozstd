// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
)

func roundTrip(t *testing.T, src []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	_, err := w.Write(src)
	if err != nil {
		t.Fatal("write:", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	w := NewWriter(&buf, nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty frame for empty input")
	}
	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty decode, got %d bytes", len(got))
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
	w := NewWriter(&buf, nil)
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

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	w := NewWriter(&buf, nil)
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

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	w := NewWriter(&buf1, nil)
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
		r := NewReader(bytes.NewReader(tc.compressed), nil)
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
	w := NewWriter(&buf, nil)
	_, _ = w.Write([]byte("data"))
	_ = w.Close()

	_, err := w.Write([]byte("more"))
	if err != ErrEncoderClosed {
		t.Fatalf("expected ErrEncoderClosed, got %v", err)
	}
}

func TestWriterDoubleClose(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
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
	e := NewEncoder()
	if err := e.SetLevel(DefaultCompression); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLevel(NoCompression); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLevel(BestSpeed); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLevel(BestCompression); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLevel(10); err == nil {
		t.Fatal("expected error for invalid level")
	}
	if err := e.SetLevel(-2); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestNoCompressionIsRaw(t *testing.T) {
	src := bytes.Repeat([]byte("hello world! this is highly compressible data. "), 1000)
	var buf bytes.Buffer
	e := NewEncoder()
	if err := e.SetLevel(NoCompression); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(&buf, e)
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < len(src) {
		t.Errorf("NoCompression reduced size: src=%d compressed=%d", len(src), buf.Len())
	}
	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("roundtrip mismatch")
	}

	// Also test AppendCompress path.
	buf.Reset()
	e2 := NewEncoder()
	if err := e2.SetLevel(NoCompression); err != nil {
		t.Fatal(err)
	}
	compressed := e2.AppendCompress(nil, src)
	if len(compressed) < len(src) {
		t.Errorf("AppendCompress NoCompression reduced size: src=%d compressed=%d", len(src), len(compressed))
	}
	r2 := NewReader(bytes.NewReader(compressed), nil)
	defer r2.Close()
	got2, err := io.ReadAll(r2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, src) {
		t.Fatal("AppendCompress roundtrip mismatch")
	}
}

func TestWriterAppendCompress(t *testing.T) {
	src := bytes.Repeat([]byte("encode all test data! "), 500)
	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	r := NewReader(bytes.NewReader(compressed), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("AppendCompress round-trip mismatch")
	}
}

func TestWriterAppendCompressEmpty(t *testing.T) {
	e := NewEncoder()
	c1 := e.AppendCompress(nil, nil)
	c2 := e.AppendCompress(nil, []byte{})
	if len(c1) == 0 {
		t.Fatal("expected non-empty frame for nil input")
	}
	if !bytes.Equal(c1, c2) {
		t.Fatal("nil and empty should produce identical frames")
	}
	r := NewReader(bytes.NewReader(c1), nil)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty decode, got %d bytes", len(got))
	}
}

func TestWriterAppendCompressMultiBlock(t *testing.T) {
	src := make([]byte, maxCompressedBlockSize*2+1000)
	for i := range src {
		src[i] = byte(i % 200)
	}
	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	r := NewReader(bytes.NewReader(compressed), nil)
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("AppendCompress multi-block round-trip mismatch")
	}
}

func TestWriterReadFrom(t *testing.T) {
	src := bytes.Repeat([]byte("ReadFrom test data! "), 1000)

	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
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

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	w := NewWriter(&buf, nil)
	_, _ = w.Write(src)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify readable (CRC checked by reader).
	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	e := NewEncoder()
	e.SetCRC(false)
	w := NewWriter(&buf, e)
	w.Reset(&buf)
	_, _ = w.Write(src)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	w := NewWriter(&buf, nil)

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
	w := NewWriter(&buf, nil)
	for _, b := range src {
		_, err := w.Write([]byte{b})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	w := NewWriter(&buf, nil)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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
	e := NewEncoder()
	if err := e.SetLevel(level); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(&buf, e)
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal("write:", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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

func TestAppendCompressAllLevels(t *testing.T) {
	src := bytes.Repeat([]byte("AppendCompress test data across levels! "), 500)
	for level := 1; level <= BestCompression; level++ {
		t.Run("", func(t *testing.T) {
			e := NewEncoder()
			if err := e.SetLevel(level); err != nil {
				t.Fatal(err)
			}
			compressed := e.AppendCompress(nil, src)
			r := NewReader(bytes.NewReader(compressed), nil)
			defer func() { _ = r.Close() }()
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("AppendCompress mismatch at level %d", level)
			}
		})
	}
}

func TestLevelSwitching(t *testing.T) {
	src := bytes.Repeat([]byte("level switching test "), 200)
	e := NewEncoder()
	w := NewWriter(nil, e)
	for level := 1; level <= BestCompression; level++ {
		var buf bytes.Buffer
		_ = e.SetLevel(level)
		w.Reset(&buf)
		_, _ = w.Write(src)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := NewReader(bytes.NewReader(buf.Bytes()), nil)
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

func TestZeroValueWriter(t *testing.T) {
	src := bytes.Repeat([]byte("zero value writer "), 100)
	var e Encoder
	compressed := e.AppendCompress(nil, src)

	r := NewReader(bytes.NewReader(compressed), nil)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
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

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestZeroValueWriterConfig(t *testing.T) {
	src := bytes.Repeat([]byte("zero config "), 100)
	var e Encoder
	e.SetCRC(false)
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

func TestResetContentSize(t *testing.T) {
	src := []byte("content size test data, exactly this long")
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	w.ResetContentSize(&buf, int64(len(src)))
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestResetContentSizeNegative(t *testing.T) {
	src := []byte("negative content size means unknown")
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	w.ResetContentSize(&buf, -1)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestResetContentSizeMismatch(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	w.ResetContentSize(&buf, 100)
	if _, err := w.Write([]byte("short")); err != nil {
		t.Fatal(err)
	}
	err := w.Close()
	if err == nil {
		t.Fatal("expected error for content size mismatch")
	}
}

func TestAppendCompressPreExistingDst(t *testing.T) {
	src := []byte("data to compress")
	e := NewEncoder()
	prefix := []byte("HEADER:")
	got := e.AppendCompress(prefix, src)
	if !bytes.HasPrefix(got, []byte("HEADER:")) {
		t.Fatalf("prefix not preserved: %x", got[:7])
	}
	// Decompress the frame after the prefix.
	var d Decoder
	dec, err := d.AppendDecompress(nil, got[7:])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, src) {
		t.Fatal("mismatch")
	}
}

func TestAppendCompressEmptyWithCRC(t *testing.T) {
	e := NewEncoder()
	e.SetCRC(true)
	frame := e.AppendCompress(nil, nil)

	var d Decoder
	got, err := d.AppendDecompress(nil, frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}

	// Verify CRC is present: frame with CRC should be longer than without.
	e.SetCRC(false)
	noCRC := e.AppendCompress(nil, nil)
	if len(frame) <= len(noCRC) {
		t.Fatalf("empty CRC frame (%d) should be longer than no-CRC (%d)", len(frame), len(noCRC))
	}
}

func TestAppendCompressEmptyNoCRC(t *testing.T) {
	e := NewEncoder()
	e.SetCRC(false)
	frame := e.AppendCompress(nil, nil)

	var d Decoder
	got, err := d.AppendDecompress(nil, frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}
}

func TestAppendCompressReuse(t *testing.T) {
	e := NewEncoder()
	src1 := []byte("first payload")
	src2 := []byte("second payload, different content")

	c1 := e.AppendCompress(nil, src1)
	c2 := e.AppendCompress(nil, src2)

	var d Decoder
	got1, err := d.AppendDecompress(nil, c1)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := d.AppendDecompress(nil, c2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got1, src1) || !bytes.Equal(got2, src2) {
		t.Fatal("reuse mismatch")
	}
}

func TestAppendCompressConcurrentAllLevels(t *testing.T) {
	src := bytes.Repeat([]byte("concurrent levels "), 500)

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	for i := range goroutines {
		level := i % (BestCompression + 1)
		go func() {
			defer wg.Done()
			le := NewEncoder()
			_ = le.SetLevel(level)
			compressed := le.AppendCompress(nil, src)
			var d Decoder
			got, err := d.AppendDecompress(nil, compressed)
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, src) {
				errs <- bytes.ErrTooLarge
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestFlushEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
}

func TestFlushMultiple(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)

	parts := []string{"alpha.", "beta.", "gamma."}
	for _, p := range parts {
		if _, err := w.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha.beta.gamma." {
		t.Fatalf("got %q", got)
	}
}

func TestReadFromEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	n, err := w.ReadFrom(bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ReadFrom empty returned %d", n)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}
}

type limitedErrReader struct {
	n   int
	err error
}

func (r *limitedErrReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, r.err
	}
	n := min(len(p), r.n)
	for i := range n {
		p[i] = 'x'
	}
	r.n -= n
	return n, nil
}

func TestReadFromError(t *testing.T) {
	readErr := errors.New("source error")
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	_, err := w.ReadFrom(&limitedErrReader{n: 100, err: readErr})
	if err != readErr {
		t.Fatalf("expected readErr, got %v", err)
	}
}

func TestReadFromLarge(t *testing.T) {
	src := make([]byte, maxCompressedBlockSize*2+1000)
	for i := range src {
		src[i] = byte(i % 199)
	}

	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
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

	r := NewReader(bytes.NewReader(buf.Bytes()), nil)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}
