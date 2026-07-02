// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
)

func TestZeroValueReaderAppendCompress(t *testing.T) {
	frame := buildRawFrame([]byte("decode zero"))
	var d Decoder
	got, err := d.AppendDecompress(nil, frame)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "decode zero" {
		t.Fatalf("got %q, want %q", got, "decode zero")
	}
}

func TestAppendDecompressConcurrent(t *testing.T) {
	src := bytes.Repeat([]byte("concurrent decompress test data! "), 500)
	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)

	d := NewDecoder()

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			got, err := d.AppendDecompress(nil, compressed)
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

func TestAppendDecompressEmpty(t *testing.T) {
	var d Decoder
	for _, src := range [][]byte{nil, {}} {
		_, err := d.AppendDecompress(nil, src)
		if !errors.Is(err, &ErrCorrupted{}) {
			t.Fatalf("expected ErrCorrupted for %v, got %v", src, err)
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected io.ErrUnexpectedEOF wrapped for %v, got %v", src, err)
		}
	}
}

func TestSetMaxSizeValidation(t *testing.T) {
	d := NewDecoder()
	if err := d.SetMaxSize(-1); err == nil {
		t.Fatal("expected error for -1")
	}
	if err := d.SetMaxSize(0); err != nil {
		t.Fatalf("0 must disable the limit: %v", err)
	}
	if err := d.SetMaxSize(100); err != nil {
		t.Fatalf("positive limit must be valid: %v", err)
	}
}

// TestSetMaxSizeAppendDecompress covers the declared-size early rejection (frames
// from AppendCompress carry FrameContentSize), the exact-limit boundary, dst
// prefix exclusion, and disabling.
func TestSetMaxSizeAppendDecompress(t *testing.T) {
	src := bytes.Repeat([]byte("bomb "), 1000)
	compressed := NewEncoder().AppendCompress(nil, src)

	d := NewDecoder()
	if err := d.SetMaxSize(int64(len(src)) - 1); err != nil {
		t.Fatal(err)
	}
	_, err := d.AppendDecompress(nil, compressed)
	var de *ErrDecodedSizeExceeded
	if !errors.As(err, &de) {
		t.Fatalf("expected ErrDecodedSizeExceeded, got %v", err)
	}
	if de.Allowed != int64(len(src))-1 {
		t.Fatalf("Allowed = %d, want %d", de.Allowed, int64(len(src))-1)
	}
	// A resource-limit error is not stream corruption.
	if errors.Is(err, &ErrCorrupted{}) {
		t.Fatal("ErrDecodedSizeExceeded must not match ErrCorrupted")
	}

	// Exactly the limit succeeds.
	if err := d.SetMaxSize(int64(len(src))); err != nil {
		t.Fatal(err)
	}
	got, err := d.AppendDecompress(nil, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}

	// The caller's existing dst prefix must not count against the budget.
	prefix := []byte("prefix-not-counted")
	got, err = d.AppendDecompress(prefix, compressed)
	if err != nil {
		t.Fatalf("dst prefix must be excluded from the limit: %v", err)
	}
	want := append(append([]byte{}, prefix...), src...)
	if !bytes.Equal(got, want) {
		t.Fatal("prefix mismatch")
	}

	// Disabling restores unlimited decoding.
	if err := d.SetMaxSize(0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AppendDecompress(nil, compressed); err != nil {
		t.Fatalf("limit disabled: %v", err)
	}
}

// TestSetMaxSizeAppendDecompressNoContentSize covers the mid-decode guard: a
// multi-block streamed frame omits FrameContentSize, so the limit must be
// enforced while blocks are decoded, not only from the declared size.
func TestSetMaxSizeAppendDecompressNoContentSize(t *testing.T) {
	src := bytes.Repeat([]byte("no fcs bomb "), 40000) // multi-block, no FrameContentSize
	var buf bytes.Buffer
	w := NewWriter(&buf, nil)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	compressed := buf.Bytes()

	d := NewDecoder()
	if err := d.SetMaxSize(int64(len(src)) / 2); err != nil {
		t.Fatal(err)
	}
	_, err := d.AppendDecompress(nil, compressed)
	var de *ErrDecodedSizeExceeded
	if !errors.As(err, &de) {
		t.Fatalf("expected ErrDecodedSizeExceeded from mid-decode guard, got %v", err)
	}

	// Without a limit the same frame decodes fully.
	if err := d.SetMaxSize(0); err != nil {
		t.Fatal(err)
	}
	got, err := d.AppendDecompress(nil, compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}
