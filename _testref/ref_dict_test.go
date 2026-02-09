// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstdref

import (
	"bytes"
	"testing"

	ref "github.com/klauspost/compress/zstd"
	zstd "github.com/klauspost/stdgozstd"
)

func loadDicts(t *testing.T) ([]byte, *zstd.Dict) {
	t.Helper()
	raw := loadTestFile(t, "d0.dict")
	d, err := zstd.ParseDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, d
}

func TestRefDictParseBoth(t *testing.T) {
	raw, liteDict := loadDicts(t)

	refDict, err := ref.InspectDictionary(raw)
	if err != nil {
		t.Fatal(err)
	}

	if liteDict.ID() != refDict.ID() {
		t.Errorf("ID mismatch: lite=%d ref=%d", liteDict.ID(), refDict.ID())
	}
	if !bytes.Equal(liteDict.Bytes(), raw) {
		t.Error("Bytes() mismatch")
	}
}

func TestRefDictMarshalRoundTrip(t *testing.T) {
	_, liteDict := loadDicts(t)

	b, err := liteDict.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var d2 zstd.Dict
	if err := d2.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	if liteDict.ID() != d2.ID() {
		t.Errorf("ID mismatch after roundtrip: %d vs %d", liteDict.ID(), d2.ID())
	}

	// Encode with original, decode with unmarshalled.
	src := testData(4096)
	w := zstd.NewWriter(nil)
	w.AddDict(liteDict)
	compressed := w.AppendCompress(nil, src)

	r, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.AddDict(&d2)
	got, err := readAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, got) {
		t.Error("marshal roundtrip decode mismatch")
	}
}

func TestRefDictParsedLiteEncodeParentDecode(t *testing.T) {
	raw, liteDict := loadDicts(t)
	src := testData(8192)

	for _, level := range []int{1, 3, 5, 8} {
		w := zstd.NewWriter(nil)
		w.SetLevel(level)
		w.AddDict(liteDict)
		compressed := w.AppendCompress(nil, src)

		got := refDecode(t, compressed, ref.WithDecoderDicts(raw))
		if !bytes.Equal(src, got) {
			t.Errorf("parsed dict level %d: lite→parent mismatch", level)
		}
	}
}

func TestRefDictParsedParentEncodeLiteDecode(t *testing.T) {
	raw, liteDict := loadDicts(t)
	src := testData(8192)

	for _, rl := range []ref.EncoderLevel{ref.SpeedFastest, ref.SpeedDefault, ref.SpeedBetterCompression, ref.SpeedBestCompression} {
		compressed := refEncode(t, src, ref.WithEncoderLevel(rl), ref.WithEncoderDict(raw))

		r, err := zstd.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}
		r.AddDict(liteDict)
		got, err := readAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("level %v: %v", rl, err)
		}
		if !bytes.Equal(src, got) {
			t.Errorf("parsed dict level %v: parent→lite mismatch", rl)
		}
	}
}

func TestRefDictRawLiteEncodeParentDecode(t *testing.T) {
	rawDict := testData(2048)
	src := testData(8192)

	for _, level := range []int{1, 3, 5, 8, 9} {
		w := zstd.NewWriter(nil)
		w.SetLevel(level)
		w.SetRawDict(rawDict)
		compressed := w.AppendCompress(nil, src)

		got := refDecode(t, compressed, ref.WithDecoderDictRaw(0, rawDict))
		if !bytes.Equal(src, got) {
			t.Errorf("raw dict level %d: lite→parent mismatch", level)
		}
	}
}

func TestRefDictRawParentEncodeLiteDecode(t *testing.T) {
	rawDict := testData(2048)
	src := testData(8192)

	for _, rl := range []ref.EncoderLevel{ref.SpeedFastest, ref.SpeedDefault, ref.SpeedBetterCompression, ref.SpeedBestCompression} {
		compressed := refEncode(t, src, ref.WithEncoderLevel(rl), ref.WithEncoderDictRaw(0, rawDict))

		r, err := zstd.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}
		r.SetRawDict(rawDict)
		got, err := readAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("level %v: %v", rl, err)
		}
		if !bytes.Equal(src, got) {
			t.Errorf("raw dict level %v: parent→lite mismatch", rl)
		}
	}
}

func TestRefDictParsedAppendToBothDirs(t *testing.T) {
	raw, liteDict := loadDicts(t)
	src := testData(4096)

	// lite → parent
	w := zstd.NewWriter(nil)
	w.AddDict(liteDict)
	compressed := w.AppendCompress(nil, src)
	got := refDecode(t, compressed, ref.WithDecoderDicts(raw))
	if !bytes.Equal(src, got) {
		t.Error("parsed AppendTo lite→parent mismatch")
	}

	// parent → lite
	compressed = refEncode(t, src, ref.WithEncoderDict(raw))
	r, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.AddDict(liteDict)
	got, err = readAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, got) {
		t.Error("parsed AppendTo parent→lite mismatch")
	}
}

func TestRefDictRawAppendToBothDirs(t *testing.T) {
	rawDict := testData(2048)
	src := testData(4096)

	// lite → parent
	w := zstd.NewWriter(nil)
	w.SetRawDict(rawDict)
	compressed := w.AppendCompress(nil, src)
	got := refDecode(t, compressed, ref.WithDecoderDictRaw(0, rawDict))
	if !bytes.Equal(src, got) {
		t.Error("raw AppendTo lite→parent mismatch")
	}

	// parent → lite
	compressed = refEncode(t, src, ref.WithEncoderDictRaw(0, rawDict))
	r, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.SetRawDict(rawDict)
	got, err = readAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, got) {
		t.Error("raw AppendTo parent→lite mismatch")
	}
}

func TestRefDictStreamBothDirs(t *testing.T) {
	raw, liteDict := loadDicts(t)
	src := testData(16384)

	// lite stream → parent decode
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf)
	w.AddDict(liteDict)
	w.Write(src)
	w.Close()
	got := refDecode(t, buf.Bytes(), ref.WithDecoderDicts(raw))
	if !bytes.Equal(src, got) {
		t.Error("dict stream lite→parent mismatch")
	}

	// parent stream → lite decode
	compressed := refStreamEncode(t, src, ref.WithEncoderDict(raw))
	r, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.AddDict(liteDict)
	got, err = readAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, got) {
		t.Error("dict stream parent→lite mismatch")
	}
}

func TestRefDictClearAndReuse(t *testing.T) {
	_, liteDict := loadDicts(t)
	src := testData(4096)

	w := zstd.NewWriter(nil)
	w.AddDict(liteDict)
	// Encode with dict.
	_ = w.AppendCompress(nil, src)

	// Clear dict.
	w.AddDict(nil)
	compressed := w.AppendCompress(nil, src)

	// Should decode without dict.
	got := refDecode(t, compressed)
	if !bytes.Equal(src, got) {
		t.Error("clear dict roundtrip mismatch")
	}
}
