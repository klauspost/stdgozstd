// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstdref

import (
	"bytes"
	"io"
	"math/rand"
	"runtime/debug"
	"testing"

	"github.com/klauspost/compress/dict"
	ref "github.com/klauspost/compress/zstd"
	zstd "github.com/klauspost/stdgozstd"
)

func FuzzRefEncoderCompat(f *testing.F) {
	f.Add([]byte("hello world"), 3)
	f.Add(testData(1024), 1)
	f.Add(testData(4096), 8)
	f.Add(make([]byte, 100), 0)

	enc := zstd.NewEncoder()
	dec, err := ref.NewReader(nil)
	if err != nil {
		f.Fatal(err)
	}
	defer dec.Close()
	var compressed, got []byte

	f.Fuzz(func(t *testing.T, data []byte, level int) {
		if level < 0 {
			level = -level
		}
		level = level % 10
		if len(data) > 1<<20 {
			return
		}
		_ = enc.SetLevel(level)
		compressed = enc.AppendCompress(compressed[:0], data)
		got, err = dec.DecodeAll(compressed, got[:0])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, got) {
			t.Errorf("roundtrip mismatch at level %d, len=%d", level, len(data))
		}
	})
}

func FuzzRefDecoderCompat(f *testing.F) {
	debug.SetGCPercent(20)
	f.Add([]byte("hello world"), 0)
	f.Add(testData(1024), 1)
	f.Add(testData(4096), 3)

	levels := []ref.EncoderLevel{ref.SpeedFastest, ref.SpeedDefault, ref.SpeedBetterCompression, ref.SpeedBestCompression}
	encs := make([]*ref.Encoder, len(levels))
	for i, l := range levels {
		enc, err := ref.NewWriter(nil, ref.WithEncoderLevel(l), ref.WithZeroFrames(true), ref.WithEncoderConcurrency(1))
		if err != nil {
			f.Fatal(err)
		}
		encs[i] = enc
	}
	liteDec := zstd.NewDecoder()

	var compressed, got []byte
	f.Fuzz(func(t *testing.T, data []byte, levelHint int) {
		if levelHint < 0 {
			levelHint = -levelHint
		}
		enc := encs[levelHint%len(encs)]

		if len(data) > 1<<20 {
			return
		}

		compressed = enc.EncodeAll(data, compressed[:0])
		got, err := liteDec.AppendDecompress(got[:0], compressed)
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

	dec := zstd.NewDecoder()
	dec.SetMaxWindowSize(maxWindow)
	liteDec := zstd.NewReader(bytes.NewReader(nil), dec)
	var refResult []byte
	f.Fuzz(func(t *testing.T, data []byte) {
		// Empty input: parent accepts (returns nil), lite rejects (ErrCorrupted).
		if len(data) == 0 || len(data) > 1<<16 {
			return
		}

		var refErr error
		refResult, refErr = refDec.DecodeAll(data, refResult[:0])

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

func FuzzRefRawDictCompat(f *testing.F) {
	f.Add(testData(256), testData(128))
	f.Add(testData(1024), testData(512))

	enc := zstd.NewEncoder()
	var compressed, got []byte
	refDec, err := ref.NewReader(nil)
	if err != nil {
		f.Fatal(err)
	}
	defer refDec.Close()
	refEnc, err := ref.NewWriter(io.Discard, ref.WithEncoderLevel(ref.SpeedFastest))
	if err != nil {
		f.Fatal(err)
	}
	defer refEnc.Close()
	dec := zstd.NewDecoder()

	f.Fuzz(func(t *testing.T, dictData, payload []byte) {
		if len(dictData) == 0 || len(payload) == 0 {
			return
		}
		if len(dictData) > 8192 {
			dictData = dictData[:8192]
		}
		if len(payload) > 1<<20 {
			payload = payload[:1<<20]
		}

		// Encode with new std, decode with reference.
		enc.SetRawDict(dictData)
		if len(payload) > 0 {
			enc.SetLevel(int(payload[0] % 10))
		}
		compressed = enc.AppendCompress(compressed[:0], payload)
		err = refDec.ResetWithOptions(nil, ref.WithDecoderDictRaw(0, dictData))
		if err != nil {
			t.Fatal(err)
		}
		got, err = refDec.DecodeAll(compressed, got[:0])
		if !bytes.Equal(payload, got) {
			t.Error("dict roundtrip mismatch (std encoded -> ref decoded)")
		}

		// Encode with reference, decode with new std.
		err = refEnc.ResetWithOptions(nil, ref.WithEncoderDictRaw(0, dictData))
		if err != nil {
			t.Fatal(err)
		}
		compressed = refEnc.EncodeAll(payload, compressed[:0])
		dec.SetRawDict(dictData)
		got, err = dec.AppendDecompress(got[:0], compressed)
		if err != nil {
			var refErr error
			got, refErr = refDec.DecodeAll(compressed, got[:0])
			if refErr != nil {
				t.Fatal("invalid encoded data: ", refErr, "got:", err)
			}
			t.Fatal("unable to decode: ", err)
		}
		if !bytes.Equal(payload, got) {
			t.Error("dict roundtrip mismatch (ref encoded -> std decoded)")
		}
	})
}

func FuzzRefDictCompat(f *testing.F) {
	dictFor := func(data []byte) []byte {
		var tmp [][]byte
		rng := rand.New(rand.NewSource(int64(len(data))))
		for range 10 {
			off := rng.Intn(len(data) / 2)
			length := rng.Intn(len(data) - off)
			tmp = append(tmp, data[off:off+length])
		}
		d, err := dict.BuildZstdDict(tmp, dict.Options{HashBytes: 5, ZstdLevel: ref.SpeedDefault, Output: nil, MaxDictSize: 96 << 10})
		if err != nil {
			f.Fatal(err)
		}
		return d
	}
	for i := range 64 {
		td := testData(64 << (i / 8))
		f.Add(dictFor(td), td)
	}
	f.Add(dictFor(testData(128)), testData(128))
	f.Add(dictFor(testData(1024)), testData(512))

	enc := zstd.NewEncoder()
	dec := zstd.NewDecoder()
	refDec, err := ref.NewReader(nil)
	if err != nil {
		f.Fatal(err)
	}
	defer refDec.Close()
	refEnc, err := ref.NewWriter(io.Discard, ref.WithEncoderLevel(ref.SpeedFastest))
	if err != nil {
		f.Fatal(err)
	}
	defer refEnc.Close()

	var compressed, got []byte
	f.Fuzz(func(t *testing.T, dictData, payload []byte) {
		if len(dictData) == 0 || len(payload) == 0 {
			return
		}
		if len(payload) > 1<<20 {
			payload = payload[:1<<20]
		}
		refEncErr := refEnc.ResetWithOptions(nil, ref.WithEncoderDict(dictData))
		gotDict, err := zstd.ParseDict(dictData)
		if err != nil || refEncErr != nil {
			if err != nil && refEncErr != nil {
				// Both errored, fine.
				return
			}
			t.Fatalf("refEnc: %v != ParseDict: %v", refEncErr, err)
		}

		// Encode with new std, decode with reference.
		enc.AddDict(gotDict)
		if len(payload) > 0 {
			enc.SetLevel(int(payload[0] % 10))
		}
		compressed = enc.AppendCompress(compressed[:0], payload)
		err = refDec.ResetWithOptions(nil, ref.WithDecoderDicts(dictData))
		if err != nil {
			t.Fatal(err)
		}
		got, err = refDec.DecodeAll(compressed, got[:0])
		if !bytes.Equal(payload, got) {
			t.Error("dict roundtrip mismatch")
		}

		// Encode with reference, decode with new std.
		compressed = refEnc.EncodeAll(payload, compressed[:0])
		dec.AddDict(gotDict)
		got, err = dec.AppendDecompress(got[:0], compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, got) {
			t.Error("dict roundtrip mismatch (ref encoded -> std decoded)")
		}
	})
}
