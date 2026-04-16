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

func TestRefConstantsMatch(t *testing.T) {
	if zstd.MinWindowSize != ref.MinWindowSize {
		t.Errorf("MinWindowSize: lite=%d ref=%d", zstd.MinWindowSize, ref.MinWindowSize)
	}
	if zstd.MaxWindowSize != ref.MaxWindowSize {
		t.Errorf("MaxWindowSize: lite=%d ref=%d", zstd.MaxWindowSize, ref.MaxWindowSize)
	}
}

func TestRefCorruptDataBothReject(t *testing.T) {
	src := testData(4096)
	compressed := liteEncode(t, src, 3)

	// Corrupt a byte in the middle of the compressed data.
	bad := make([]byte, len(compressed))
	copy(bad, compressed)
	bad[len(bad)/2] ^= 0xff

	// Lite should error.
	r, err := zstd.NewReader(bytes.NewReader(bad))
	if err == nil {
		_, err = io.ReadAll(r)
		r.Close()
	}
	liteErr := err != nil

	// Parent should error.
	dec, err := ref.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	_, refErr := dec.DecodeAll(bad, nil)

	if refErr != nil && !liteErr {
		t.Error("parent rejected corrupt data but lite did not")
	}
}

func TestRefTruncatedStreamBothReject(t *testing.T) {
	src := testData(8192)
	compressed := liteEncode(t, src, 3)

	// Truncate to 75% of the compressed data.
	trunc := compressed[:len(compressed)*3/4]

	r, err := zstd.NewReader(bytes.NewReader(trunc))
	if err == nil {
		_, err = io.ReadAll(r)
		r.Close()
	}
	liteErr := err != nil

	dec, err := ref.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	_, refErr := dec.DecodeAll(trunc, nil)

	if refErr != nil && !liteErr {
		t.Error("parent rejected truncated data but lite did not")
	}
}

func TestRefBadChecksumBothReject(t *testing.T) {
	src := testData(4096)
	w := zstd.NewWriter(nil)
	w.SetCRC(true)
	compressed := w.AppendCompress(nil, src)

	// Flip the last byte (CRC is at the end).
	bad := make([]byte, len(compressed))
	copy(bad, compressed)
	bad[len(bad)-1] ^= 0xff

	r, err := zstd.NewReader(bytes.NewReader(bad))
	if err == nil {
		_, err = io.ReadAll(r)
		r.Close()
	}
	liteErr := err != nil

	dec, err := ref.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	_, refErr := dec.DecodeAll(bad, nil)

	if refErr != nil && !liteErr {
		t.Error("parent rejected bad checksum but lite did not")
	}
}

func TestRefWriteAfterClose(t *testing.T) {
	// Lite encoder.
	var buf bytes.Buffer
	w := zstd.NewWriter(&buf)
	w.Write([]byte("hello"))
	w.Close()
	_, liteErr := w.Write([]byte("more"))

	// Parent encoder.
	var buf2 bytes.Buffer
	enc, err := ref.NewWriter(&buf2)
	if err != nil {
		t.Fatal(err)
	}
	enc.Write([]byte("hello"))
	enc.Close()
	_, refErr := enc.Write([]byte("more"))

	if (liteErr == nil) != (refErr == nil) {
		t.Errorf("write-after-close behavior differs: lite=%v ref=%v", liteErr, refErr)
	}
}
