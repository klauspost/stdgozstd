// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"io"
	"testing"
)

func TestByteBufReadSmall(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	buf := byteBuf(data)
	got, err := buf.readSmall(3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("got %v, want [1 2 3]", got)
	}
	if len(buf) != 5 {
		t.Fatalf("remaining: got %d, want 5", len(buf))
	}
}

func TestByteBufReadBig(t *testing.T) {
	data := []byte{10, 20, 30, 40, 50, 60, 70, 80}
	buf := byteBuf(data)
	got, err := buf.readBig(6, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{10, 20, 30, 40, 50, 60}) {
		t.Fatalf("got %v, want [10 20 30 40 50 60]", got)
	}
	if len(buf) != 2 {
		t.Fatalf("remaining: got %d, want 2", len(buf))
	}
}

func TestByteBufReadByte(t *testing.T) {
	data := []byte{0xAA, 0xBB, 0xCC}
	buf := byteBuf(data)
	for i, want := range data {
		got, err := buf.readByte()
		if err != nil {
			t.Fatalf("byte %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("byte %d: got %#x, want %#x", i, got, want)
		}
	}
}

func TestByteBufEOF(t *testing.T) {
	buf := byteBuf([]byte{1, 2})

	if _, err := buf.readSmall(3); err != io.ErrUnexpectedEOF {
		t.Fatalf("readSmall: got %v, want ErrUnexpectedEOF", err)
	}

	buf = byteBuf([]byte{1})
	_, _ = buf.readByte()
	if _, err := buf.readByte(); err != io.ErrUnexpectedEOF {
		t.Fatalf("readByte: got %v, want ErrUnexpectedEOF", err)
	}

	buf = byteBuf([]byte{1})
	if _, err := buf.readBig(5, nil); err != io.ErrUnexpectedEOF {
		t.Fatalf("readBig: got %v, want ErrUnexpectedEOF", err)
	}
}

func TestReaderWrapper(t *testing.T) {
	data := []byte("Hello, World!")
	rw := &readerWrapper{r: bytes.NewReader(data)}

	got, err := rw.readSmall(5)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "Hello" {
		t.Fatalf("readSmall: got %q, want %q", got, "Hello")
	}

	got2, err := rw.readBig(2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != ", " {
		t.Fatalf("readBig: got %q, want %q", got2, ", ")
	}

	b, err := rw.readByte()
	if err != nil {
		t.Fatal(err)
	}
	if b != 'W' {
		t.Fatalf("readByte: got %c, want W", b)
	}
}
