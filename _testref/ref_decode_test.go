// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstdref

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"

	ref "github.com/klauspost/compress/zstd"
	zstd "github.com/klauspost/stdgozstd"
)

func TestRefParentEncodeAllLevels(t *testing.T) {
	src := testData(32768)
	for _, rl := range []ref.EncoderLevel{ref.SpeedFastest, ref.SpeedDefault, ref.SpeedBetterCompression, ref.SpeedBestCompression} {
		compressed := refEncode(t, src, ref.WithEncoderLevel(rl))
		got := liteDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %v: roundtrip mismatch", rl)
		}
	}
}

func TestRefParentEncodeEmpty(t *testing.T) {
	compressed := refEncode(t, nil, ref.WithZeroFrames(true))
	got := liteDecode(t, compressed)
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}

func TestRefParentEncodeOneByte(t *testing.T) {
	src := []byte{42}
	compressed := refEncode(t, src)
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("single byte roundtrip mismatch")
	}
}

func TestRefParentEncodeSmall(t *testing.T) {
	src := loadTestFile(t, "shortsample")
	compressed := refEncode(t, src)
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("shortsample roundtrip mismatch")
	}
}

func TestRefParentEncodeMedium(t *testing.T) {
	src := loadTestFile(t, "z000028")
	compressed := refEncode(t, src)
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("z000028 roundtrip mismatch")
	}
}

func TestRefParentEncodeLarge(t *testing.T) {
	src := testData(1 << 20)
	for _, rl := range []ref.EncoderLevel{ref.SpeedFastest, ref.SpeedDefault, ref.SpeedBetterCompression, ref.SpeedBestCompression} {
		compressed := refEncode(t, src, ref.WithEncoderLevel(rl))
		got := liteDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %v: 1MB roundtrip mismatch", rl)
		}
	}
}

func TestRefParentEncodeMultiBlock(t *testing.T) {
	src := testData(2*maxCompressedBlockSize + 5000)
	for _, rl := range []ref.EncoderLevel{ref.SpeedFastest, ref.SpeedDefault} {
		compressed := refEncode(t, src, ref.WithEncoderLevel(rl))
		got := liteDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("level %v: multi-block roundtrip mismatch", rl)
		}
	}
}

func TestRefParentEncodeStream(t *testing.T) {
	src := testData(32768)
	compressed := refStreamEncode(t, src)
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("parent stream roundtrip mismatch")
	}
}

func TestRefParentEncodeCRCOn(t *testing.T) {
	src := testData(4096)
	compressed := refEncode(t, src, ref.WithEncoderCRC(true))
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("CRC on roundtrip mismatch")
	}
}

func TestRefParentEncodeCRCOff(t *testing.T) {
	src := testData(4096)
	compressed := refEncode(t, src, ref.WithEncoderCRC(false))
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("CRC off roundtrip mismatch")
	}
}

func TestRefParentEncodeWindowSizes(t *testing.T) {
	src := testData(16384)
	for _, ws := range []int{1 << 10, 1 << 16, 1 << 20, 8 << 20} {
		compressed := refEncode(t, src, ref.WithWindowSize(ws))
		got := liteDecode(t, compressed)
		if !bytes.Equal(src, got) {
			t.Errorf("window %d: roundtrip mismatch", ws)
		}
	}
}

func TestRefParentEncodeContentSize(t *testing.T) {
	src := testData(8192)
	var buf bytes.Buffer
	enc, err := ref.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	enc.ResetContentSize(&buf, int64(len(src)))
	enc.Write(src)
	enc.Close()
	got := liteDecode(t, buf.Bytes())
	if !bytes.Equal(src, got) {
		t.Error("content size roundtrip mismatch")
	}
}

func TestRefParentEncodeConcatenated(t *testing.T) {
	enc, err := ref.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()

	parts := [][]byte{
		testData(2048),
		testData(4096),
		testData(1024),
	}
	var concat []byte
	for _, p := range parts {
		concat = enc.EncodeAll(p, concat)
	}
	want := make([]byte, 0, 2048+4096+1024)
	for _, p := range parts {
		want = append(want, p...)
	}
	got := liteDecode(t, concat)
	if !bytes.Equal(want, got) {
		t.Error("concatenated roundtrip mismatch")
	}
}

func TestRefParentEncodeAllZeros(t *testing.T) {
	src := make([]byte, 100*1024)
	compressed := refEncode(t, src)
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("all-zeros roundtrip mismatch")
	}
}

func TestRefParentEncodeRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(99, 0))
	src := make([]byte, 100*1024)
	for i := range src {
		src[i] = byte(rng.IntN(256))
	}
	compressed := refEncode(t, src)
	got := liteDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("random roundtrip mismatch")
	}
}

func TestRefDecodePrecompressed(t *testing.T) {
	for _, tc := range []struct {
		raw, zst string
	}{
		{"z000028", "z000028.zst"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			want := loadTestFile(t, tc.raw)
			compressed := loadTestFile(t, tc.zst)
			// Verify parent can decode it.
			refGot := refDecode(t, compressed)
			if !bytes.Equal(want, refGot) {
				t.Fatal("parent decode of precompressed file failed")
			}
			got := liteDecode(t, compressed)
			if !bytes.Equal(want, got) {
				t.Errorf("precompressed decode mismatch: want %d, got %d", len(want), len(got))
			}
		})
	}
}

func TestRefDecodeGoodZip(t *testing.T) {
	files := loadGoodZip(t)
	if len(files) == 0 {
		t.Skip("no files in good.zip")
	}
	for name, compressed := range files {
		if !strings.HasSuffix(name, ".zst") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			// Verify parent decodes successfully first.
			want := refDecode(t, compressed)
			got := liteDecode(t, compressed)
			if !bytes.Equal(want, got) {
				t.Errorf("decode mismatch: want %d bytes, got %d", len(want), len(got))
			}
		})
	}
}

func TestRefLiteDecodeReset(t *testing.T) {
	src1 := testData(4096)
	src2 := testData(8192)
	c1 := refEncode(t, src1)
	c2 := refEncode(t, src2)

	r, err := zstd.NewReader(bytes.NewReader(c1))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got1, err := readAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src1, got1) {
		t.Error("first decode mismatch")
	}

	r.Reset(bytes.NewReader(c2))
	got2, err := readAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src2, got2) {
		t.Error("second decode after reset mismatch")
	}
}

func TestRefLiteDecodeMaxWindowSize(t *testing.T) {
	src := testData(16384)
	// Encode with a large window size.
	compressed := refEncode(t, src, ref.WithWindowSize(8<<20))

	// Try to decode with a small max window — should fail.
	r, err := zstd.NewReader(bytes.NewReader(compressed), zstd.WithDecoderMaxWindow(1<<10))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = readAll(r)
	if err == nil {
		t.Error("expected error for small max window size, got nil")
	}
}
