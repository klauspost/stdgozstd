// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstdref

import (
	"archive/zip"
	"bytes"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	ref "github.com/klauspost/compress/zstd"
	zstd "github.com/klauspost/stdgozstd"
)

const maxCompressedBlockSize = 128 << 10

func refLevel(liteLevel int) ref.EncoderLevel {
	switch {
	case liteLevel <= 2:
		return ref.SpeedFastest
	case liteLevel <= 4:
		return ref.SpeedDefault
	case liteLevel <= 7:
		return ref.SpeedBetterCompression
	default:
		return ref.SpeedBestCompression
	}
}

func refEncode(t testing.TB, src []byte, opts ...ref.EOption) []byte {
	t.Helper()
	enc, err := ref.NewWriter(nil, append([]ref.EOption{ref.WithZeroFrames(true), ref.WithEncoderConcurrency(1)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(src, nil)
}

func refDecode(t testing.TB, compressed []byte, opts ...ref.DOption) []byte {
	t.Helper()
	dec, err := ref.NewReader(nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	got, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func liteEncode(t testing.TB, src []byte, level int) []byte {
	t.Helper()
	e, err := zstd.NewEncoder(zstd.WithEncoderLevel(level))
	if err != nil {
		t.Fatal(err)
	}
	return e.AppendCompress(nil, src)
}

func liteEncodeOpts(t testing.TB, src []byte, opts ...zstd.EncoderOption) []byte {
	t.Helper()
	e, err := zstd.NewEncoder(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return e.AppendCompress(nil, src)
}

func liteDecode(t testing.TB, compressed []byte) []byte {
	t.Helper()
	d, err := zstd.NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.AppendDecompress(nil, compressed)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func liteDecodeOpts(t testing.TB, compressed []byte, opts ...zstd.DecoderOption) []byte {
	t.Helper()
	r, err := zstd.NewReader(bytes.NewReader(compressed), opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func loadTestFile(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func loadGoodZip(t testing.TB) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(filepath.Join("testdata", "good.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := make(map[string][]byte)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

func testData(size int) []byte {
	r := rand.New(rand.NewPCG(42, 0))
	b := make([]byte, size)
	phrases := []string{
		"the quick brown fox jumps over the lazy dog ",
		"hello world this is a test of compression ",
		"abcdefghijklmnopqrstuvwxyz0123456789 ",
		"repeated patterns help compression ratios ",
	}
	pos := 0
	for pos < size {
		switch r.IntN(4) {
		case 0: // text phrase
			p := phrases[r.IntN(len(phrases))]
			n := copy(b[pos:], p)
			pos += n
		case 1: // random bytes
			n := min(r.IntN(64)+1, size-pos)
			for i := range n {
				b[pos+i] = byte(r.IntN(256))
			}
			pos += n
		case 2: // zeros
			n := min(r.IntN(128)+1, size-pos)
			pos += n
		case 3: // back-reference (repeat previous)
			if pos > 64 {
				l := min(r.IntN(64)+4, size-pos)
				off := r.IntN(min(pos, 1024)) + 1
				for i := range l {
					b[pos+i] = b[pos-off+i%off]
				}
				pos += l
			}
		}
	}
	return b
}

func readAll(r *zstd.Reader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}
