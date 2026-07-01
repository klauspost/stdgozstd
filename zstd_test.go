// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
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

	r := NewReader(bytes.NewReader(buf.Bytes()))
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

	r := NewReader(bytes.NewReader(buf.Bytes()))
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
	frame1 := w.AppendCompress(nil, []byte("frame one "))
	frame2 := w.AppendCompress(nil, []byte("frame two"))

	combined := append(frame1, frame2...)
	r := NewReader(bytes.NewReader(combined))
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

	r := NewReader(bytes.NewReader(compressed.Bytes()))
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

		r := NewReader(bytes.NewReader(buf.Bytes()))
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
				compressed := w.AppendCompress(nil, input.data)

				r := NewReader(bytes.NewReader(compressed))
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

func TestErrCorruptedError(t *testing.T) {
	tests := []struct {
		name string
		err  *ErrCorrupted
		want string
	}{
		{"msg only", &ErrCorrupted{msg: "bad data"}, "bad data"},
		{"err only", &ErrCorrupted{err: io.ErrUnexpectedEOF}, "unexpected EOF"},
		{"msg+err", &ErrCorrupted{msg: "reading block", err: io.ErrUnexpectedEOF}, "reading block: unexpected EOF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrCorruptedIs(t *testing.T) {
	err := corruptedError("test")
	if !errors.Is(err, &ErrCorrupted{}) {
		t.Fatal("errors.Is should match any *ErrCorrupted")
	}
}

func TestErrCorruptedUnwrap(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	err := &ErrCorrupted{msg: "wrapper", err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should expose inner error")
	}
	plain := corruptedError("no inner")
	if plain.Unwrap() != nil {
		t.Fatal("Unwrap should return nil when no inner error")
	}
}

func TestErrCorruptedNotIsOther(t *testing.T) {
	err := corruptedError("x")
	if errors.Is(err, io.EOF) {
		t.Fatal("ErrCorrupted should not match io.EOF")
	}
}

func TestErrWindowSizeExceededError(t *testing.T) {
	err := &ErrWindowSizeExceeded{Allowed: 1024, Requested: 4096}
	s := err.Error()
	if !bytes.Contains([]byte(s), []byte("1024")) || !bytes.Contains([]byte(s), []byte("4096")) {
		t.Fatalf("Error() should contain both values: %q", s)
	}
}

func TestErrWindowSizeExceededIs(t *testing.T) {
	err := &ErrWindowSizeExceeded{Allowed: 1, Requested: 2}
	if !errors.Is(err, &ErrWindowSizeExceeded{}) {
		t.Fatal("errors.Is should match any *ErrWindowSizeExceeded")
	}
	if errors.Is(err, &ErrCorrupted{}) {
		t.Fatal("should not match ErrCorrupted")
	}
}

func TestConcurrentReadersFromSameFrame(t *testing.T) {
	src := bytes.Repeat([]byte("shared frame data "), 500)
	w := NewWriter(nil)
	compressed := w.AppendCompress(nil, src)

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			r := NewReader(bytes.NewReader(compressed))
			got, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, src) {
				errs <- bytes.ErrTooLarge
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestAppendConcurrentBidirectional(t *testing.T) {
	inputs := [][]byte{
		bytes.Repeat([]byte("short"), 10),
		bytes.Repeat([]byte("medium payload with some variation: "), 500),
		randTestBytes(64*1024, 77),
		make([]byte, 50000),
	}

	w := NewWriter(nil)
	r := NewReader(nil)

	// Pre-compress all inputs.
	compressed := make([][]byte, len(inputs))
	for i, src := range inputs {
		compressed[i] = w.AppendCompress(nil, src)
	}

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	for g := range goroutines {
		idx := g % len(inputs)
		go func() {
			defer wg.Done()
			src := inputs[idx]

			// Compress.
			c := w.AppendCompress(nil, src)

			// Decompress the pre-made frame.
			got, err := r.AppendDecompress(nil, compressed[idx])
			if err != nil {
				errs <- fmt.Errorf("decompress %d: %w", idx, err)
				return
			}
			if !bytes.Equal(got, src) {
				errs <- fmt.Errorf("decompress %d: mismatch", idx)
				return
			}

			// Decompress what we just compressed.
			got, err = r.AppendDecompress(nil, c)
			if err != nil {
				errs <- fmt.Errorf("re-decompress %d: %w", idx, err)
				return
			}
			if !bytes.Equal(got, src) {
				errs <- fmt.Errorf("re-decompress %d: mismatch", idx)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func randTestBytes(n int, seed uint64) []byte {
	b := make([]byte, n)
	rng := rand.New(rand.NewPCG(seed, seed+1))
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return b
}

func TestZeroValueReaderWriteTo(t *testing.T) {
	frame := buildRawFrame([]byte("zero writeto"))
	var r Reader
	if err := r.Reset(bytes.NewReader(frame)); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := r.WriteTo(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 || buf.String() != "zero writeto" {
		t.Fatalf("got %d %q", n, buf.String())
	}
	r.Close()
}

func TestZeroValueReaderConfig(t *testing.T) {
	var r Reader
	if err := r.SetMaxWindowSize(MaxWindowSize); err != nil {
		t.Fatal(err)
	}
	frame := buildRawFrame([]byte("config"))
	got, err := r.AppendDecompress(nil, frame)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "config" {
		t.Fatalf("got %q", got)
	}
	r.Close()
}

func TestZeroValueWriterReset(t *testing.T) {
	src := bytes.Repeat([]byte("zero reset "), 100)
	var buf bytes.Buffer
	var w Writer
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()))
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}

func TestZeroValueWriterReadFrom(t *testing.T) {
	src := bytes.Repeat([]byte("zero readfrom "), 100)
	var buf bytes.Buffer
	var w Writer
	w.Reset(&buf)
	n, err := w.ReadFrom(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(src)) {
		t.Fatalf("ReadFrom returned %d, want %d", n, len(src))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r := NewReader(bytes.NewReader(buf.Bytes()))
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}
