// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstdref

import (
	"bytes"
	"io"
	"testing"

	ref "github.com/klauspost/compress/zstd"
	zstd "github.com/klauspost/stdgozstd"
)

func FuzzRefEncoderCompat(f *testing.F) {
	f.Add([]byte("hello world"), 3)
	f.Add(testData(1024), 1)
	f.Add(testData(4096), 8)
	f.Add(make([]byte, 100), 0)

	w := zstd.NewWriter(nil)
	dec, err := ref.NewReader(nil)
	if err != nil {
		f.Fatal(err)
	}
	defer dec.Close()

	f.Fuzz(func(t *testing.T, data []byte, level int) {
		if level < 0 {
			level = -level
		}
		level = level % 10
		if len(data) > 1<<20 {
			return
		}
		_ = w.SetLevel(level)
		compressed := w.AppendCompress(nil, data)
		got, err := dec.DecodeAll(compressed, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, got) {
			t.Errorf("roundtrip mismatch at level %d, len=%d", level, len(data))
		}
	})
}

func FuzzRefDecoderCompat(f *testing.F) {
	f.Add([]byte("hello world"), 0)
	f.Add(testData(1024), 1)
	f.Add(testData(4096), 3)

	levels := []ref.EncoderLevel{ref.SpeedFastest, ref.SpeedDefault, ref.SpeedBetterCompression, ref.SpeedBestCompression}
	encs := make([]*ref.Encoder, len(levels))
	for i, l := range levels {
		enc, err := ref.NewWriter(nil, ref.WithEncoderLevel(l), ref.WithZeroFrames(true))
		if err != nil {
			f.Fatal(err)
		}
		encs[i] = enc
	}
	liteDec, err := zstd.NewReader(nil)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, data []byte, levelHint int) {
		if levelHint < 0 {
			levelHint = -levelHint
		}
		enc := encs[levelHint%len(encs)]

		if len(data) > 1<<20 {
			return
		}

		compressed := enc.EncodeAll(data, nil)
		_ = liteDec.Reset(bytes.NewReader(nil))
		got, err := liteDec.AppendDecompress(nil, compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, got) {
			t.Errorf("roundtrip mismatch at ref level %v, len=%d", levels[levelHint%len(levels)], len(data))
		}
	})
}

func FuzzRefBothDecoders(f *testing.F) {
	f.Add([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x04, 0x00, 0x01, 0x00, 0x00})
	f.Add([]byte("not zstd at all"))
	f.Add([]byte{})

	const maxDecode = 16 << 20
	const maxWindow = 16 << 20

	refDec, err := ref.NewReader(nil, ref.WithDecoderMaxMemory(maxDecode), ref.WithDecoderMaxWindow(maxWindow))
	if err != nil {
		f.Fatal(err)
	}
	defer refDec.Close()

	liteDec, err := zstd.NewReader(bytes.NewReader(nil))
	if err != nil {
		f.Fatal(err)
	}
	liteDec.SetMaxWindowSize(maxWindow)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Empty input: parent accepts (returns nil), lite rejects (ErrCorrupted).
		if len(data) == 0 || len(data) > 1<<16 {
			return
		}

		refResult, refErr := refDec.DecodeAll(data, nil)

		_ = liteDec.Reset(bytes.NewReader(data))
		liteResult, liteErr := io.ReadAll(liteDec)

		if refErr == nil && liteErr != nil {
			t.Errorf("parent decoded OK but lite failed: %v", liteErr)
		}
		if refErr == nil && liteErr == nil && !bytes.Equal(refResult, liteResult) {
			t.Error("both decoded but results differ")
		}
	})
}

func FuzzRefDictCompat(f *testing.F) {
	f.Add(testData(256), testData(128))
	f.Add(testData(1024), testData(512))

	w := zstd.NewWriter(nil)

	f.Fuzz(func(t *testing.T, dictData, payload []byte) {
		if len(dictData) == 0 || len(payload) == 0 {
			return
		}
		if len(dictData) > 8192 {
			dictData = dictData[:8192]
		}
		if len(payload) > 8192 {
			payload = payload[:8192]
		}

		w.SetRawDict(dictData)
		compressed := w.AppendCompress(nil, payload)

		got := refDecode(t, compressed, ref.WithDecoderDictRaw(0, dictData))
		if !bytes.Equal(payload, got) {
			t.Error("dict roundtrip mismatch")
		}
	})
}
