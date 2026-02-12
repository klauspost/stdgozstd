// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"io"
	"testing"
)

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("hello world"))
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(bytes.Repeat([]byte("abcdef"), 1000))
	f.Add(make([]byte, 65536))
	w := NewWriter(nil)
	r, _ := NewReader(bytes.NewReader(nil))
	var buf bytes.Buffer

	f.Fuzz(func(t *testing.T, data []byte) {
		buf.Reset()
		w.Reset(nil)
		if len(data) > 0 {
			_ = w.SetLevel(int(data[0] % BestCompression))
		}
		w.Reset(&buf)
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		_ = r.Reset(bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(data))
		}
	})
}

func FuzzNewReader(f *testing.F) {
	// Seed with valid compressed data.
	seed := func(src []byte) []byte {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		w.Write(src)
		w.Close()
		return buf.Bytes()
	}
	f.Add(seed([]byte("test")))
	f.Add(seed([]byte("testtesttesttesttesttesttesttest")))
	f.Add(seed(bytes.Repeat([]byte{0}, 100000)))
	f.Add(seed([]byte{}))
	f.Add([]byte{0x28, 0xb5, 0x2f, 0xfd}) // just magic
	r, err := NewReader(nil)
	if err != nil {
		f.Fatal(err)
	}
	defer r.Close()

	f.Fuzz(func(t *testing.T, data []byte) {
		err = r.Reset(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, io.Reader(r))
		err = r.Reset(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = r.WriteTo(io.Discard)
	})
}

func FuzzStreamRoundTrip(f *testing.F) {
	f.Add([]byte{3, 0}, []byte("hello world"))
	f.Add([]byte{3, 0}, bytes.Repeat([]byte("hello world"), 1000))
	f.Add([]byte{3, 1}, bytes.Repeat([]byte("hello world"), 10000))
	f.Add([]byte{23, 0}, bytes.Repeat([]byte("a"), 10000))
	f.Add([]byte{0, 0}, []byte{})
	f.Add([]byte{9, 5}, bytes.Repeat([]byte("abcdef"), 1000))
	f.Add([]byte{0x85, 0xff}, make([]byte, 65536))
	w := NewWriter(nil)
	r, _ := NewReader(bytes.NewReader(nil))

	f.Fuzz(func(t *testing.T, cfg, data []byte) {
		if len(cfg) != 2 {
			return
		}
		level := int(cfg[0]&0x0f) % (BestCompression + 1)
		crc := cfg[0]&0x10 != 0
		lowMem := cfg[0]&0x20 != 0
		split := int(cfg[1])
		if len(data) > 0 {
			split = split % len(data)
		} else {
			split = 0
		}

		var buf bytes.Buffer
		_ = w.SetLevel(level)
		w.SetCRC(crc)
		w.SetLowMemory(lowMem)
		w.Reset(&buf)
		_, _ = w.Write(data[:split])
		_, _ = w.Write(data[split:])
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		_ = r.Reset(bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(data))
		}
	})
}

func FuzzDictRoundTrip(f *testing.F) {
	f.Add([]byte{3, 10}, bytes.Repeat([]byte("the quick brown fox "), 50))
	f.Add([]byte{0, 0}, []byte("small"))
	f.Add([]byte{0, 0}, []byte("smallsmallsmallsmallsmall"))
	w := NewWriter(nil)
	r, _ := NewReader(bytes.NewReader(nil))

	f.Fuzz(func(t *testing.T, cfg, data []byte) {
		if len(cfg) < 2 || len(data) < 2 {
			return
		}
		level := int(cfg[0]&0x0f) % (BestCompression + 1)
		crc := cfg[0]&0x10 != 0
		dictSplit := int(cfg[1]) % len(data)
		if dictSplit == 0 {
			dictSplit = 1
		}
		dictPart := data[:dictSplit]
		dataPart := data[dictSplit:]

		var buf bytes.Buffer
		_ = w.SetLevel(level)
		w.SetCRC(crc)
		w.SetRawDict(dictPart)
		w.Reset(&buf)
		if _, err := w.Write(dataPart); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		_ = r.Reset(bytes.NewReader(buf.Bytes()))
		r.SetRawDict(dictPart)
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(got, dataPart) {
			t.Fatalf("dict round-trip mismatch: got %d bytes, want %d", len(got), len(dataPart))
		}
	})
}

func FuzzParseDict(f *testing.F) {
	f.Add([]byte{0x37, 0xa4, 0x30, 0xec, 1, 0, 0, 0})
	f.Add([]byte("not a dict"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := ParseDict(data)
		if err != nil {
			return
		}
		if d.ID() == 0 {
			t.Fatal("parsed dict has ID 0")
		}
		if !bytes.Equal(d.Bytes(), data) {
			t.Fatal("Bytes() != input")
		}
		b, err := d.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var d2 Dict
		if err := d2.UnmarshalBinary(b); err != nil {
			t.Fatal(err)
		}
		if d2.ID() != d.ID() {
			t.Fatalf("ID mismatch after roundtrip: %d != %d", d2.ID(), d.ID())
		}
		if !bytes.Equal(d2.Bytes(), d.Bytes()) {
			t.Fatal("Bytes mismatch after roundtrip")
		}
	})
}

func FuzzReaderReset(f *testing.F) {
	f.Add(byte(3), []byte("hello reset world"))
	f.Add(byte(0), []byte{})
	f.Add(byte(9), bytes.Repeat([]byte("reset"), 500))
	w := NewWriter(nil)
	r, _ := NewReader(bytes.NewReader(nil))
	var buf bytes.Buffer

	f.Fuzz(func(t *testing.T, levelByte byte, data []byte) {
		level := int(levelByte) % (BestCompression + 1)

		compress := func(level int) []byte {
			var b bytes.Buffer
			_ = w.SetLevel(level)
			w.Reset(&b)
			w.Write(data)
			w.Close()
			return b.Bytes()
		}
		compressed1 := compress(level)

		level2 := (level + 1) % (BestCompression + 1)
		compressed2 := compress(level2)

		_ = r.Reset(bytes.NewReader(compressed1))
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("first read: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("first read mismatch")
		}

		_ = r.Reset(bytes.NewReader(compressed2))
		got, err = io.ReadAll(r)
		if err != nil {
			t.Fatalf("second read after Reset: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatal("second read mismatch")
		}

		_ = r.Reset(bytes.NewReader(compressed2))
		buf.Reset()
		n, err := r.WriteTo(&buf)
		if err != nil {
			t.Fatalf("WriteTo read after Reset: %v", err)
		}
		if n != int64(buf.Len()) {
			t.Fatalf("WriteTo read after Reset: got %d bytes, want %d", n, buf.Len())
		}
		if !bytes.Equal(buf.Bytes(), data) {
			t.Fatal("WriteTo read mismatch")
		}
	})
}
