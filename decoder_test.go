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
		e := NewEncoder()
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

		e := NewEncoder()
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
		e := NewEncoder()
		e.SetCRC(true)
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
	})

	t.Run("different_data", func(t *testing.T) {
		e := NewEncoder()
		d := NewDecoder()

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
}

func TestDecoder_SetMaxSize(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
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
	})

	// append_decompress covers the declared-size early rejection (frames from
	// AppendCompress carry FrameContentSize), the exact-limit boundary, dst
	// prefix exclusion, and disabling.
	t.Run("append_decompress", func(t *testing.T) {
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
	})

	// no_content_size covers the mid-decode guard: a multi-block streamed frame
	// omits FrameContentSize, so the limit must be enforced while blocks are
	// decoded, not only from the declared size.
	t.Run("no_content_size", func(t *testing.T) {
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
	})

	// multi_frame verifies the limit is cumulative across concatenated frames
	// within a single AppendDecompress call.
	t.Run("multi_frame", func(t *testing.T) {
		part := bytes.Repeat([]byte("frame "), 500)
		e := NewEncoder()
		var concatenated []byte
		concatenated = e.AppendCompress(concatenated, part)
		concatenated = e.AppendCompress(concatenated, part)

		d := NewDecoder()
		// Enough for one frame but not both.
		if err := d.SetMaxSize(int64(len(part)) + 100); err != nil {
			t.Fatal(err)
		}
		_, err := d.AppendDecompress(nil, concatenated)
		var de *ErrDecodedSizeExceeded
		if !errors.As(err, &de) {
			t.Fatalf("expected cumulative ErrDecodedSizeExceeded, got %v", err)
		}

		// Enough for both frames succeeds.
		if err := d.SetMaxSize(int64(2 * len(part))); err != nil {
			t.Fatal(err)
		}
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

func TestDecoder_SetMaxWindowSize(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		d := NewDecoder()
		if err := d.SetMaxWindowSize(0); err == nil {
			t.Fatal("expected error for 0")
		}
		if err := d.SetMaxWindowSize(-1); err == nil {
			t.Fatal("expected error for -1")
		}
		if err := d.SetMaxWindowSize(MaxWindowSize + 1); err == nil {
			t.Fatal("expected error for too large")
		}
		if err := d.SetMaxWindowSize(MinWindowSize); err != nil {
			t.Fatalf("MinWindowSize should be valid: %v", err)
		}
		if err := d.SetMaxWindowSize(MaxWindowSize); err != nil {
			t.Fatalf("MaxWindowSize should be valid: %v", err)
		}
	})

	t.Run("one_shot", func(t *testing.T) {
		d := NewDecoder()
		if err := d.SetMaxWindowSize(MaxWindowSize); err != nil {
			t.Fatal(err)
		}
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
	e := NewEncoder()
	uno := bytes.Repeat([]byte("uno "), 300)
	dos := bytes.Repeat([]byte("dos "), 300)
	f1 := e.AppendCompress(nil, uno)
	f2 := e.AppendCompress(nil, dos)

	d := NewDecoder()
	for _, tc := range []struct{ frame, want []byte }{{f1, uno}, {f2, dos}} {
		r := NewReader(bytes.NewReader(tc.frame), d)
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

// TestDecoderReconfigureBetweenStreams tightens then loosens the max window on a
// bound Decoder between Reset cycles and verifies the new limit is honored.
func TestDecoderReconfigureBetweenStreams(t *testing.T) {
	src := bytes.Repeat([]byte("decoder reconfigure "), 300)
	compressed := NewEncoder().AppendCompress(nil, src)

	d := NewDecoder()
	r := NewReader(bytes.NewReader(compressed), d)

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}

	// Tighten the SAME decoder, reuse the SAME reader: must now fail.
	if err := d.SetMaxWindowSize(MinWindowSize); err != nil {
		t.Fatal(err)
	}
	if err := r.Reset(bytes.NewReader(compressed)); err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	var we *ErrWindowSizeExceeded
	if !errors.As(err, &we) {
		t.Fatalf("expected ErrWindowSizeExceeded after tightening, got %v", err)
	}

	// Loosen again: succeeds.
	if err := d.SetMaxWindowSize(MaxWindowSize); err != nil {
		t.Fatal(err)
	}
	if err := r.Reset(bytes.NewReader(compressed)); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}
