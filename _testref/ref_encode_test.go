// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstdref

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"

	ref "github.com/klauspost/compress/zstd"
	zstd "github.com/klauspost/stdgozstd"
)

func TestRefLiteAppendToLevels(t *testing.T) {
	src := testData(32768)
	for _, level := range []int{zstd.DefaultCompression, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9} {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: roundtrip mismatch", level)
		}
	}
}

func TestRefLiteEncodeEmpty(t *testing.T) {
	w := zstd.NewWriter(nil)
	compressed := w.AppendCompress(nil, nil)
	if len(compressed) == 0 {
		t.Fatal("expected non-empty frame for nil input")
	}
	got := refDecode(t, compressed)
	if len(got) != 0 {
		t.Fatalf("expected empty decode, got %d bytes", len(got))
	}

	// Streaming: Write nothing, Close.
	var buf bytes.Buffer
	w.Reset(&buf)
	w.Close()
	if buf.Len() == 0 {
		t.Fatal("expected non-empty frame from streaming empty close")
	}
	got = refDecode(t, buf.Bytes())
	if len(got) != 0 {
		t.Fatalf("expected empty decode from stream, got %d bytes", len(got))
	}
}

func TestRefLiteEncodeEmptyAllLevels(t *testing.T) {
	for level := 0; level <= 9; level++ {
		compressed := liteEncode(t, nil, level)
		got := refDecode(t, compressed)
		if len(got) != 0 {
			t.Errorf("level %d: expected empty decode, got %d bytes", level, len(got))
		}
	}
}

func TestRefLiteEncodeOneByte(t *testing.T) {
	src := []byte{42}
	for level := 0; level <= 9; level++ {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: roundtrip mismatch for single byte", level)
		}
	}
}

func TestRefLiteEncodeSmall(t *testing.T) {
	src := loadTestFile(t, "shortsample")
	for level := 0; level <= 9; level++ {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: roundtrip mismatch", level)
		}
	}
}

func TestRefLiteEncodeMedium(t *testing.T) {
	src := loadTestFile(t, "z000028")
	for level := 0; level <= 9; level++ {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: roundtrip mismatch", level)
		}
	}
}

func TestRefLiteEncodeLarge(t *testing.T) {
	src := testData(1 << 20)
	for _, level := range []int{1, 3, 5, 8} {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: roundtrip mismatch for 1MB", level)
		}
	}
}

func TestRefLiteEncodeMultiBlock(t *testing.T) {
	src := testData(2*maxCompressedBlockSize + 5000)
	for _, level := range []int{1, 3, 5, 8} {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: multi-block roundtrip mismatch", level)
		}
	}
}

func TestRefLiteEncodeStream(t *testing.T) {
	src := testData(32768)
	for _, level := range []int{1, 3, 5, 8} {
		var buf bytes.Buffer
		w := zstd.NewWriter(&buf)
		w.SetLevel(level)
		w.Write(src)
		w.Close()
		got := refDecode(t, buf.Bytes())
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: stream roundtrip mismatch", level)
		}
	}
}

func TestRefLiteEncodeStreamMultiWrite(t *testing.T) {
	src := testData(4096)
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf)
	for i := 0; i < len(src); i += 7 {
		end := min(i+7, len(src))
		w.Write(src[i:end])
	}
	w.Close()
	got := refDecode(t, buf.Bytes())
	if !bytes.Equal(src, got) {
		t.Error("multi-write stream roundtrip mismatch")
	}
}

func TestRefLiteEncodeStreamByteByByte(t *testing.T) {
	src := testData(512)
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf)
	for _, b := range src {
		w.Write([]byte{b})
	}
	w.Close()
	got := refDecode(t, buf.Bytes())
	if !bytes.Equal(src, got) {
		t.Error("byte-by-byte stream roundtrip mismatch")
	}
}

func TestRefLiteEncodeFlush(t *testing.T) {
	src1 := testData(2048)
	src2 := testData(1024)
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf)
	w.Write(src1)
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	w.Write(src2)
	w.Close()

	want := make([]byte, 0, len(src1)+len(src2))
	want = append(want, src1...)
	want = append(want, src2...)
	got := refDecode(t, buf.Bytes())
	if !bytes.Equal(want, got) {
		t.Error("flush stream roundtrip mismatch")
	}
}

func TestRefLiteEncodeReadFrom(t *testing.T) {
	src := testData(32768)
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf)
	w.ReadFrom(bytes.NewReader(src))
	w.Close()
	got := refDecode(t, buf.Bytes())
	if !bytes.Equal(src, got) {
		t.Error("ReadFrom roundtrip mismatch")
	}
}

func TestRefLiteEncodeCRCOn(t *testing.T) {
	src := testData(4096)
	w := zstd.NewWriter(nil)
	w.SetCRC(true)
	compressed := w.AppendCompress(nil, src)
	got := refDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("CRC on roundtrip mismatch")
	}
}

func TestRefLiteEncodeCRCOff(t *testing.T) {
	src := testData(4096)
	w := zstd.NewWriter(nil)
	w.SetCRC(false)
	compressed := w.AppendCompress(nil, src)
	got := refDecode(t, compressed, ref.IgnoreChecksum(true))
	if !bytes.Equal(src, got) {
		t.Error("CRC off roundtrip mismatch")
	}
}

func TestRefLiteEncodeWindowSizes(t *testing.T) {
	for _, ws := range []int{1 << 10, 1 << 16, 1 << 20, 4 << 20, 8 << 20} {
		// Data must fit within the window for proper encoding.
		dataSize := min(ws/2, 16384)
		if dataSize < 64 {
			dataSize = 64
		}
		src := testData(dataSize)
		compressed := liteEncodeOpts(t, src, func(w *zstd.Writer) {
			w.SetLevel(3)
			w.SetWindowSize(ws)
		})
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("window %d: roundtrip mismatch", ws)
		}
	}
}

func TestRefLiteEncodeContentSize(t *testing.T) {
	src := testData(8192)
	var buf bytes.Buffer
	w := zstd.NewWriter(nil)
	w.ResetContentSize(&buf, int64(len(src)))
	w.Write(src)
	w.Close()
	got := refDecode(t, buf.Bytes())
	if !bytes.Equal(src, got) {
		t.Error("content size roundtrip mismatch")
	}
}

func TestRefLiteEncodeLowMem(t *testing.T) {
	src := testData(32768)
	compressed := liteEncodeOpts(t, src, func(w *zstd.Writer) {
		w.SetLevel(3)
		w.SetLowMemory(true)
	})
	got := refDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("lowmem roundtrip mismatch")
	}
}

func TestRefLiteEncodeResetCycles(t *testing.T) {
	w := zstd.NewWriter(nil)
	for i := range 10 {
		src := testData(4096 + i*1024)
		var buf bytes.Buffer
		w.Reset(&buf)
		w.Write(src)
		w.Close()
		got := refDecode(t, buf.Bytes())
		if !bytes.Equal(src, got) {
			t.Errorf("cycle %d: roundtrip mismatch", i)
		}
	}
}

func TestRefLiteEncodeConcatenated(t *testing.T) {
	parts := [][]byte{
		testData(2048),
		testData(4096),
		testData(1024),
	}
	w := zstd.NewWriter(nil)
	var concat []byte
	for _, p := range parts {
		concat = w.AppendCompress(concat, p)
	}
	want := make([]byte, 0, 2048+4096+1024)
	for _, p := range parts {
		want = append(want, p...)
	}
	got := refDecode(t, concat)
	if !bytes.Equal(want, got) {
		t.Error("concatenated roundtrip mismatch")
	}
}

func TestRefLiteEncodeAllZeros(t *testing.T) {
	src := make([]byte, 100*1024)
	for level := 0; level <= 9; level++ {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: all-zeros roundtrip mismatch", level)
		}
	}
}

func TestRefLiteEncodeRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(99, 0))
	src := make([]byte, 100*1024)
	for i := range src {
		src[i] = byte(rng.IntN(256))
	}
	for level := 0; level <= 9; level++ {
		compressed := liteEncode(t, src, level)
		got := refDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %d: random roundtrip mismatch", level)
		}
	}
}

// liteStreamEncode encodes via streaming Write+Close at a given level.
func liteStreamEncode(t testing.TB, src []byte, level int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf)
	w.SetLevel(level)
	w.Write(src)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// refStreamEncode encodes via parent streaming Write+Close.
func refStreamEncode(t testing.TB, src []byte, opts ...ref.EOption) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := ref.NewWriter(&buf, opts...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// refStreamDecode decodes via parent streaming Read.
func refStreamDecode(t testing.TB, compressed []byte, opts ...ref.DOption) []byte {
	t.Helper()
	dec, err := ref.NewReader(bytes.NewReader(compressed), opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	got, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
