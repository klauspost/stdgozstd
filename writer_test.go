// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"
)

func roundTrip(t *testing.T, src []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := mustWriter(t, &buf)
	_, err := w.Write(src)
	if err != nil {
		t.Fatal("write:", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r := mustReader(t, bytes.NewReader(buf.Bytes()))
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal("readall:", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(src))
	}
}

func TestWriter_Write(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if buf.Len() == 0 {
			t.Fatal("expected non-empty frame for empty input")
		}
		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty decode, got %d bytes", len(got))
		}
	})

	t.Run("one_byte", func(t *testing.T) {
		roundTrip(t, []byte{42})
	})

	t.Run("small", func(t *testing.T) {
		roundTrip(t, []byte("hello, zstd world!"))
	})

	t.Run("medium", func(t *testing.T) {
		src := make([]byte, 1000)
		for i := range src {
			src[i] = byte(i % 251)
		}
		roundTrip(t, src)
	})

	t.Run("block_boundary", func(t *testing.T) {
		// Exactly one block.
		src := make([]byte, maxCompressedBlockSize)
		for i := range src {
			src[i] = byte(i * 7)
		}
		roundTrip(t, src)
	})

	t.Run("multi_block", func(t *testing.T) {
		// More than one block.
		src := make([]byte, maxCompressedBlockSize+1000)
		for i := range src {
			src[i] = byte(i * 3)
		}
		roundTrip(t, src)
	})

	t.Run("large", func(t *testing.T) {
		src := make([]byte, 1<<20) // 1MB
		rng := rand.New(rand.NewPCG(12345, 67890))
		for i := range src {
			src[i] = byte(rng.IntN(256))
		}
		roundTrip(t, src)
	})

	t.Run("compressible", func(t *testing.T) {
		// Highly compressible data.
		src := bytes.Repeat([]byte("ABCDEFGH"), 10000)
		roundTrip(t, src)
	})

	t.Run("all_zeros", func(t *testing.T) {
		roundTrip(t, make([]byte, 50000))
	})

	t.Run("small_chunks", func(t *testing.T) {
		src := []byte("the quick brown fox jumps over the lazy dog, repeatedly. ")
		src = bytes.Repeat(src, 100)

		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		for i := 0; i < len(src); i += 7 {
			end := min(i+7, len(src))
			_, err := w.Write(src[i:end])
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("single_byte", func(t *testing.T) {
		src := []byte("byte by byte writing test, long enough to have content")

		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		for _, b := range src {
			_, err := w.Write([]byte{b})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("crc_enabled", func(t *testing.T) {
		src := []byte("checksum test data")
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		_, _ = w.Write(src)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		// Verify readable (CRC checked by reader).
		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("no_crc", func(t *testing.T) {
		src := []byte("no checksum test data, slightly longer to be interesting")
		var buf bytes.Buffer
		w := mustWriter(t, &buf, WithEncoderCRC(false))
		w.Reset(&buf)
		_, _ = w.Write(src)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("small_read_buffer", func(t *testing.T) {
		src := bytes.Repeat([]byte("small buffer read test "), 200)

		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer func() { _ = r.Close() }()

		// Read in small chunks.
		var got []byte
		tmp := make([]byte, 13)
		for {
			n, err := r.Read(tmp)
			got = append(got, tmp[:n]...)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})
}

func TestWriter_Write_Count(t *testing.T) {
	src := []byte("verify write count returns correct values")
	var buf bytes.Buffer
	w := mustWriter(t, &buf)

	n, err := w.Write(src[:10])
	if err != nil || n != 10 {
		t.Fatalf("Write(10) = %d, %v", n, err)
	}
	n, err = w.Write(src[10:])
	if err != nil || n != len(src)-10 {
		t.Fatalf("Write(rest) = %d, %v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriter_Write_AfterClose(t *testing.T) {
	var buf bytes.Buffer
	w := mustWriter(t, &buf)
	_, _ = w.Write([]byte("data"))
	_ = w.Close()

	_, err := w.Write([]byte("more"))
	if err != ErrEncoderClosed {
		t.Fatalf("expected ErrEncoderClosed, got %v", err)
	}
}

func TestWriter_Close(t *testing.T) {
	var buf bytes.Buffer
	w := mustWriter(t, &buf)
	_, _ = w.Write([]byte("data"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Second close should not panic or error.
	if err := w.Close(); err != nil {
		t.Fatal("second close:", err)
	}
}

func TestWriter_Reset(t *testing.T) {
	t.Run("preserves_config_across_streams", func(t *testing.T) {
		src1 := []byte("first stream content")
		src2 := []byte("second stream content after reset")

		var buf1, buf2 bytes.Buffer
		w := mustWriter(t, &buf1)
		_, _ = w.Write(src1)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		if err := w.Reset(&buf2); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(src2)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		for i, tc := range []struct {
			compressed []byte
			want       []byte
		}{
			{buf1.Bytes(), src1},
			{buf2.Bytes(), src2},
		} {
			r := mustReader(t, bytes.NewReader(tc.compressed))
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				t.Fatalf("stream %d: %v", i, err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("stream %d: mismatch", i)
			}
		}
	})

	t.Run("change_options", func(t *testing.T) {
		src := bytes.Repeat([]byte("reconfigure me across resets. "), 400)
		w := mustWriter(t, nil)
		for _, opts := range [][]EncoderOption{
			{WithEncoderLevel(1)},
			{WithEncoderLevel(9), WithEncoderCRC(false)},
			{WithEncoderLevel(3), WithWindowSize(1 << 20), WithLowMemory(true)},
		} {
			var buf bytes.Buffer
			if err := w.Reset(&buf, opts...); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(src); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			r := mustReader(t, bytes.NewReader(buf.Bytes()))
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, src) {
				t.Fatal("mismatch")
			}
		}
	})

	t.Run("change_level_changes_output", func(t *testing.T) {
		src := bytes.Repeat([]byte("compressible data pattern. "), 500)
		w := mustWriter(t, nil)

		var raw bytes.Buffer
		if err := w.Reset(&raw, WithEncoderLevel(0)); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(src)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		var best bytes.Buffer
		if err := w.Reset(&best, WithEncoderLevel(9)); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(src)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		if raw.Len() <= best.Len() {
			t.Fatalf("level 0 output (%d) not larger than level 9 (%d)", raw.Len(), best.Len())
		}
		for _, b := range [][]byte{raw.Bytes(), best.Bytes()} {
			r := mustReader(t, bytes.NewReader(b))
			got, _ := io.ReadAll(r)
			_ = r.Close()
			if !bytes.Equal(got, src) {
				t.Fatal("mismatch")
			}
		}
	})

	t.Run("invalid_option", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, nil)
		if err := w.Reset(&buf, WithWindowSize(1)); err == nil {
			t.Fatal("expected error for out-of-range window size")
		}
	})

	t.Run("content_size_known", func(t *testing.T) {
		src := []byte("content size test data, exactly this long")
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		if err := w.Reset(&buf, WithContentSize(int64(len(src)))); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("content_size_negative_is_error", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, nil)
		if err := w.Reset(&buf, WithContentSize(-1)); err == nil {
			t.Fatal("expected error for negative content size")
		}
	})

	t.Run("content_size_mismatch", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		if err := w.Reset(&buf, WithContentSize(100)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("short")); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err == nil {
			t.Fatal("expected error for content size mismatch")
		}
	})
}

func TestWriter_Flush(t *testing.T) {
	t.Run("interleaved", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		part1 := []byte("first part of data. ")
		part2 := []byte("second part of data.")

		_, err := w.Write(part1)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		// After flush, some bytes should have been written.
		if buf.Len() == 0 {
			t.Fatal("expected output after flush")
		}

		_, err = w.Write(part2)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		want := append(part1, part2...)
		if !bytes.Equal(got, want) {
			t.Fatal("mismatch after flush")
		}
	})

	t.Run("empty", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
	})

	t.Run("multiple", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, &buf)

		parts := []string{"alpha.", "beta.", "gamma."}
		for _, p := range parts {
			if _, err := w.Write([]byte(p)); err != nil {
				t.Fatal(err)
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "alpha.beta.gamma." {
			t.Fatalf("got %q", got)
		}
	})
}

type limitedErrReader struct {
	n   int
	err error
}

func (r *limitedErrReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, r.err
	}
	n := min(len(p), r.n)
	for i := range n {
		p[i] = 'x'
	}
	r.n -= n
	return n, nil
}

func TestWriter_ReadFrom(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		src := bytes.Repeat([]byte("ReadFrom test data! "), 1000)

		var buf bytes.Buffer
		w := mustWriter(t, &buf)
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

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("ReadFrom round-trip mismatch")
		}
	})

	t.Run("empty", func(t *testing.T) {
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		n, err := w.ReadFrom(bytes.NewReader(nil))
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("ReadFrom empty returned %d", n)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty, got %d bytes", len(got))
		}
	})

	t.Run("error", func(t *testing.T) {
		readErr := errors.New("source error")
		var buf bytes.Buffer
		w := mustWriter(t, &buf)
		_, err := w.ReadFrom(&limitedErrReader{n: 100, err: readErr})
		if err != readErr {
			t.Fatalf("expected readErr, got %v", err)
		}
	})

	t.Run("large", func(t *testing.T) {
		src := make([]byte, maxCompressedBlockSize*2+1000)
		for i := range src {
			src[i] = byte(i % 199)
		}

		var buf bytes.Buffer
		w := mustWriter(t, &buf)
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

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})
}

func roundTripLevel(t *testing.T, src []byte, level int) {
	t.Helper()
	var buf bytes.Buffer
	w := mustWriter(t, &buf, WithEncoderLevel(level))
	w.Reset(&buf)
	if _, err := w.Write(src); err != nil {
		t.Fatal("write:", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close:", err)
	}

	r := mustReader(t, bytes.NewReader(buf.Bytes()))
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("readall (level %d): %v", level, err)
	}
	if !bytes.Equal(got, src) {
		t.Fatalf("round-trip mismatch at level %d: got %d bytes, want %d", level, len(got), len(src))
	}
}

func TestWriter_AllLevels(t *testing.T) {
	t.Run("small", func(t *testing.T) {
		src := []byte("the quick brown fox jumps over the lazy dog, repeatedly! ")
		src = bytes.Repeat(src, 50)
		for level := NoCompression; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				roundTripLevel(t, src, level)
			})
		}
		// DefaultCompression
		roundTripLevel(t, src, DefaultCompression)
	})

	t.Run("medium", func(t *testing.T) {
		src := make([]byte, 50000)
		rng := rand.New(rand.NewPCG(42, 99))
		for i := range src {
			src[i] = byte(rng.IntN(64))
		}
		for level := NoCompression; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				roundTripLevel(t, src, level)
			})
		}
	})

	t.Run("large", func(t *testing.T) {
		src := make([]byte, 1<<20)
		rng := rand.New(rand.NewPCG(7777, 8888))
		for i := range src {
			src[i] = byte(rng.IntN(256))
		}
		for level := NoCompression; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				roundTripLevel(t, src, level)
			})
		}
	})

	t.Run("compressible", func(t *testing.T) {
		src := bytes.Repeat([]byte("ABCDEFGHIJKLMNOP"), 5000)
		for level := NoCompression; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				roundTripLevel(t, src, level)
			})
		}
	})

	t.Run("all_zeros", func(t *testing.T) {
		src := make([]byte, 100000)
		for level := NoCompression; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				roundTripLevel(t, src, level)
			})
		}
	})

	t.Run("multi_block", func(t *testing.T) {
		src := make([]byte, maxCompressedBlockSize*2+5000)
		for i := range src {
			src[i] = byte(i % 251)
		}
		for level := 1; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				roundTripLevel(t, src, level)
			})
		}
	})

	t.Run("one_byte", func(t *testing.T) {
		src := []byte{42}
		for level := NoCompression; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				roundTripLevel(t, src, level)
			})
		}
	})
}

func TestWriter_ZeroValue(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		src := bytes.Repeat([]byte("zero stream "), 100)
		var buf bytes.Buffer
		var w Writer
		w.Reset(&buf)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("read_from", func(t *testing.T) {
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

		r := mustReader(t, bytes.NewReader(buf.Bytes()))
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})
}
