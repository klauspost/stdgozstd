// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"io"
	"math/rand/v2"
	"testing"
)

func TestRoundTripStreaming(t *testing.T) {
	src := bytes.Repeat([]byte("streaming round-trip test data! "), 500)

	var buf bytes.Buffer
	w := NewWriter(&buf)
	// Write one byte at a time.
	for _, b := range src {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	// Read one byte at a time.
	var got []byte
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			got = append(got, tmp[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Close()

	if !bytes.Equal(got, src) {
		t.Fatalf("mismatch: got %d, want %d bytes", len(got), len(src))
	}
}

func TestRoundTripLarge(t *testing.T) {
	rng := rand.New(rand.NewPCG(12345, 67890))
	src := make([]byte, 2<<20) // 2MB
	for i := range src {
		src[i] = byte(rng.IntN(256))
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("large round-trip mismatch: got %d, want %d", len(got), len(src))
	}
}

func TestConcatenatedFrames(t *testing.T) {
	w := NewWriter(nil)
	frame1 := compressOneShot(t, w, []byte("frame one "))
	frame2 := compressOneShot(t, w, []byte("frame two"))

	combined := append(frame1, frame2...)
	r, err := NewReader(bytes.NewReader(combined))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "frame one frame two" {
		t.Fatalf("got %q", got)
	}
}

func TestIOCopy(t *testing.T) {
	src := bytes.Repeat([]byte("io.Copy integration test "), 1000)

	var compressed bytes.Buffer
	w := NewWriter(&compressed)
	n, err := io.Copy(w, bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(src)) {
		t.Fatalf("copy wrote %d, want %d", n, len(src))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var decompressed bytes.Buffer
	_, err = io.Copy(&decompressed, r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decompressed.Bytes(), src) {
		t.Fatal("io.Copy round-trip mismatch")
	}
}

func TestResetCycles(t *testing.T) {
	w := NewWriter(nil)
	for i := range 50 {
		var buf bytes.Buffer
		w.Reset(&buf)
		data := []byte{byte(i), byte(i + 1), byte(i + 2)}
		data = bytes.Repeat(data, 100)
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r, err := NewReader(bytes.NewReader(buf.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("cycle %d: mismatch", i)
		}
	}
}

func TestAllLevelsRoundTrip(t *testing.T) {
	inputs := []struct {
		name string
		data []byte
	}{
		{"zeros", make([]byte, 50000)},
		{"random", func() []byte {
			b := make([]byte, 50000)
			rng := rand.New(rand.NewPCG(1, 2))
			for i := range b {
				b[i] = byte(rng.IntN(256))
			}
			return b
		}()},
		{"text", bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 1000)},
		{"binary", func() []byte {
			b := make([]byte, 50000)
			for i := range b {
				b[i] = byte(i % 251)
			}
			return b
		}()},
		{"repetitive", bytes.Repeat([]byte{0xAA, 0xBB}, 25000)},
	}

	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			for level := NoCompression; level <= BestCompression; level++ {
				w := NewWriter(nil)
				if err := w.SetLevel(level); err != nil {
					t.Fatal(err)
				}
				compressed := compressOneShot(t, w, input.data)

				r, err := NewReader(bytes.NewReader(compressed))
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(r)
				_ = r.Close()
				if err != nil {
					t.Fatalf("level %d: %v", level, err)
				}
				if !bytes.Equal(got, input.data) {
					t.Fatalf("level %d: mismatch", level)
				}
			}
		})
	}
}
