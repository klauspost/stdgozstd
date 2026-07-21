// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"sync"
	"testing"
)

func TestDecoder_ZeroValue(t *testing.T) {
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

func TestDecoder_AppendDecompress(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
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
	})

	t.Run("pre_existing_dst", func(t *testing.T) {
		src := []byte("appended after prefix")
		e := mustEncoder(t)
		compressed := e.AppendCompress(nil, src)

		var d Decoder
		prefix := []byte("PREFIX:")
		got, err := d.AppendDecompress(prefix, compressed)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "PREFIX:appended after prefix" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("large", func(t *testing.T) {
		// > 1 MiB to exercise the non-prealloc path in runDecoder.
		rng := rand.New(rand.NewSource(99))
		src := make([]byte, 1<<20+50000)
		rng.Read(src)

		e := mustEncoder(t)
		compressed := e.AppendCompress(nil, src)

		var d Decoder
		got, err := d.AppendDecompress(nil, compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("multi_frame_crc", func(t *testing.T) {
		e := mustEncoder(t, WithEncoderCRC(true))
		frame1 := e.AppendCompress(nil, []byte("frame one "))
		frame2 := e.AppendCompress(nil, []byte("frame two"))
		combined := append(frame1, frame2...)

		var d Decoder
		got, err := d.AppendDecompress(nil, combined)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "frame one frame two" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestDecoder_AppendDecompress_Concurrent(t *testing.T) {
	t.Run("same_decoder", func(t *testing.T) {
		src := bytes.Repeat([]byte("concurrent decompress test data! "), 500)
		e := mustEncoder(t)
		compressed := e.AppendCompress(nil, src)

		d := mustDecoder(t)

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
	})

	t.Run("different_data", func(t *testing.T) {
		e := mustEncoder(t)
		d := mustDecoder(t)

		const goroutines = 8
		var wg sync.WaitGroup
		wg.Add(goroutines)
		errs := make(chan error, goroutines)

		for i := range goroutines {
			src := bytes.Repeat([]byte{byte('A' + i)}, 5000)
			compressed := e.AppendCompress(nil, src)
			go func() {
				defer wg.Done()
				got, err := d.AppendDecompress(nil, compressed)
				if err != nil {
					errs <- err
					return
				}
				if !bytes.Equal(got, src) {
					errs <- errors.New("mismatch")
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
	})

	t.Run("zero_value", func(t *testing.T) {
		src := bytes.Repeat([]byte("zero value concurrent decode "), 200)
		compressed := mustEncoder(t).AppendCompress(nil, src)
		var d Decoder

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
	})
}

func TestWithDecoderMaxSize(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		if _, err := NewDecoder(WithDecoderMaxSize(-1)); err == nil {
			t.Fatal("expected error for -1")
		}
		mustDecoder(t, WithDecoderMaxSize(0))
		mustDecoder(t, WithDecoderMaxSize(100))
	})

	// append_decompress covers the declared-size early rejection (frames from
	// AppendCompress carry FrameContentSize), the exact-limit boundary, dst
	// prefix exclusion, and disabling.
	t.Run("append_decompress", func(t *testing.T) {
		src := bytes.Repeat([]byte("bomb "), 1000)
		compressed := mustEncoder(t).AppendCompress(nil, src)

		d := mustDecoder(t, WithDecoderMaxSize(int64(len(src))-1))
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
		d = mustDecoder(t, WithDecoderMaxSize(int64(len(src))))
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
		d = mustDecoder(t, WithDecoderMaxSize(0))
		if _, err := d.AppendDecompress(nil, compressed); err != nil {
			t.Fatalf("limit disabled: %v", err)
		}
	})

	// no_content_size covers the mid-decode guard: a multi-block streamed frame
	// omits FrameContentSize, so the limit must be enforced while blocks are
	// decoded, not only from the declared size.
	t.Run("no_content_size", func(t *testing.T) {
		src := bytes.Repeat([]byte("no fcs bomb "), 40000) // multi-block, no FrameContentSize
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		compressed := buf.Bytes()

		d := mustDecoder(t, WithDecoderMaxSize(int64(len(src))/2))
		_, err := d.AppendDecompress(nil, compressed)
		var de *ErrDecodedSizeExceeded
		if !errors.As(err, &de) {
			t.Fatalf("expected ErrDecodedSizeExceeded from mid-decode guard, got %v", err)
		}

		// Without a limit the same frame decodes fully.
		d = mustDecoder(t, WithDecoderMaxSize(0))
		got, err := d.AppendDecompress(nil, compressed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	// multi_frame verifies the limit is cumulative across concatenated frames
	// within a single AppendDecompress call.
	t.Run("multi_frame", func(t *testing.T) {
		part := bytes.Repeat([]byte("frame "), 500)
		e := mustEncoder(t)
		var concatenated []byte
		concatenated = e.AppendCompress(concatenated, part)
		concatenated = e.AppendCompress(concatenated, part)

		// Enough for one frame but not both.
		d := mustDecoder(t, WithDecoderMaxSize(int64(len(part))+100))
		_, err := d.AppendDecompress(nil, concatenated)
		var de *ErrDecodedSizeExceeded
		if !errors.As(err, &de) {
			t.Fatalf("expected cumulative ErrDecodedSizeExceeded, got %v", err)
		}

		// Enough for both frames succeeds.
		d = mustDecoder(t, WithDecoderMaxSize(int64(2*len(part))))
		got, err := d.AppendDecompress(nil, concatenated)
		if err != nil {
			t.Fatal(err)
		}
		want := append(append([]byte{}, part...), part...)
		if !bytes.Equal(got, want) {
			t.Fatal("mismatch")
		}
	})
}

func TestWithDecoderMaxWindow(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		if _, err := NewDecoder(WithDecoderMaxWindow(0)); err == nil {
			t.Fatal("expected error for 0")
		}
		if _, err := NewDecoder(WithDecoderMaxWindow(-1)); err == nil {
			t.Fatal("expected error for -1")
		}
		if _, err := NewDecoder(WithDecoderMaxWindow(MaxWindowSize + 1)); err == nil {
			t.Fatal("expected error for too large")
		}
		mustDecoder(t, WithDecoderMaxWindow(MinWindowSize))
		mustDecoder(t, WithDecoderMaxWindow(MaxWindowSize))
	})

	t.Run("one_shot", func(t *testing.T) {
		d := mustDecoder(t, WithDecoderMaxWindow(MaxWindowSize))
		frame := buildRawFrame([]byte("config"))
		got, err := d.AppendDecompress(nil, frame)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "config" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestDecoderReuseAcrossReaders(t *testing.T) {
	e := mustEncoder(t)
	uno := bytes.Repeat([]byte("uno "), 300)
	dos := bytes.Repeat([]byte("dos "), 300)
	f1 := e.AppendCompress(nil, uno)
	f2 := e.AppendCompress(nil, dos)

	for _, tc := range []struct{ frame, want []byte }{{f1, uno}, {f2, dos}} {
		r := mustReader(t, bytes.NewReader(tc.frame))
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Fatal("mismatch")
		}
	}
}

// TestDecoderReconfigureBetweenStreams verifies the max-window limit is honored
// per reader: a tiny window rejects a wide frame while a large window accepts it.
// The window is now fixed at construction, so each phase uses a fresh reader.
func TestDecoderReconfigureBetweenStreams(t *testing.T) {
	src := bytes.Repeat([]byte("decoder reconfigure "), 300)
	compressed := mustEncoder(t).AppendCompress(nil, src)

	r := mustReader(t, bytes.NewReader(compressed))
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}

	// A fresh reader with a tiny window must now fail.
	r = mustReader(t, bytes.NewReader(compressed), WithDecoderMaxWindow(MinWindowSize))
	_, err = io.ReadAll(r)
	var we *ErrWindowSizeExceeded
	if !errors.As(err, &we) {
		t.Fatalf("expected ErrWindowSizeExceeded after tightening, got %v", err)
	}

	// A fresh reader with a large window succeeds.
	r = mustReader(t, bytes.NewReader(compressed), WithDecoderMaxWindow(MaxWindowSize))
	got, err = io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}
