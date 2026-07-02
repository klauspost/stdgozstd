// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
)

func TestHashLen(t *testing.T) {
	for _, mls := range []uint8{3, 4, 5, 6, 7, 8} {
		h := hashLen(0x123456789ABCDEF0, 14, mls)
		if h == 0 {
			t.Fatalf("hashLen with mls=%d returned 0 for non-zero input", mls)
		}
	}
}

func TestHashLenDistribution(t *testing.T) {
	const (
		length  = uint8(14)
		mls     = uint8(6)
		n       = 1000
		buckets = 1 << length
	)
	counts := make([]int, buckets)
	rng := rand.New(rand.NewPCG(42, 99))
	for range n {
		v := rng.Uint64()
		h := hashLen(v, length, mls)
		if h >= uint32(buckets) {
			t.Fatalf("hash %d out of range [0, %d)", h, buckets)
		}
		counts[h]++
	}
	maxAllowed := n / 5
	for i, c := range counts {
		if c > maxAllowed {
			t.Fatalf("bucket %d has %d entries (max %d), poor distribution", i, c, maxAllowed)
		}
	}
}

func TestMatchLen(t *testing.T) {
	tests := []struct {
		name string
		a, b []byte
		want int
	}{
		{"identical", []byte("abcdefghij"), []byte("abcdefghij"), 10},
		{"first_differs", []byte("xbcdef"), []byte("abcdef"), 0},
		{"differ_at_3", []byte("abcXef"), []byte("abcdef"), 3},
		{"differ_past_8", []byte("abcdefghXX"), []byte("abcdefghij"), 8},
		{"empty", []byte{}, []byte{}, 0},
		{"one_match", []byte{42, 1}, []byte{42, 2}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchLen(tt.a, tt.b)
			if got != tt.want {
				t.Fatalf("matchLen: got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEncoder_AppendCompress(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		src := bytes.Repeat([]byte("encode all test data! "), 500)
		e := NewEncoder()
		compressed := e.AppendCompress(nil, src)

		r := NewReader(bytes.NewReader(compressed), nil)
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("AppendCompress round-trip mismatch")
		}
	})

	t.Run("empty", func(t *testing.T) {
		e := NewEncoder()
		c1 := e.AppendCompress(nil, nil)
		c2 := e.AppendCompress(nil, []byte{})
		if len(c1) == 0 {
			t.Fatal("expected non-empty frame for nil input")
		}
		if !bytes.Equal(c1, c2) {
			t.Fatal("nil and empty should produce identical frames")
		}
		r := NewReader(bytes.NewReader(c1), nil)
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty decode, got %d bytes", len(got))
		}
	})

	t.Run("empty_crc", func(t *testing.T) {
		e := NewEncoder()
		e.SetCRC(true)
		frame := e.AppendCompress(nil, nil)

		var d Decoder
		got, err := d.AppendDecompress(nil, frame)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty, got %d bytes", len(got))
		}

		// Verify CRC is present: frame with CRC should be longer than without.
		e.SetCRC(false)
		noCRC := e.AppendCompress(nil, nil)
		if len(frame) <= len(noCRC) {
			t.Fatalf("empty CRC frame (%d) should be longer than no-CRC (%d)", len(frame), len(noCRC))
		}
	})

	t.Run("empty_no_crc", func(t *testing.T) {
		e := NewEncoder()
		e.SetCRC(false)
		frame := e.AppendCompress(nil, nil)

		var d Decoder
		got, err := d.AppendDecompress(nil, frame)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty, got %d bytes", len(got))
		}
	})

	t.Run("multi_block", func(t *testing.T) {
		src := make([]byte, maxCompressedBlockSize*2+1000)
		for i := range src {
			src[i] = byte(i % 200)
		}
		e := NewEncoder()
		compressed := e.AppendCompress(nil, src)

		r := NewReader(bytes.NewReader(compressed), nil)
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("AppendCompress multi-block round-trip mismatch")
		}
	})

	t.Run("pre_existing_dst", func(t *testing.T) {
		src := []byte("data to compress")
		e := NewEncoder()
		prefix := []byte("HEADER:")
		got := e.AppendCompress(prefix, src)
		if !bytes.HasPrefix(got, []byte("HEADER:")) {
			t.Fatalf("prefix not preserved: %x", got[:7])
		}
		// Decompress the frame after the prefix.
		var d Decoder
		dec, err := d.AppendDecompress(nil, got[7:])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(dec, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("reuse", func(t *testing.T) {
		e := NewEncoder()
		src1 := []byte("first payload")
		src2 := []byte("second payload, different content")

		c1 := e.AppendCompress(nil, src1)
		c2 := e.AppendCompress(nil, src2)

		var d Decoder
		got1, err := d.AppendDecompress(nil, c1)
		if err != nil {
			t.Fatal(err)
		}
		got2, err := d.AppendDecompress(nil, c2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got1, src1) || !bytes.Equal(got2, src2) {
			t.Fatal("reuse mismatch")
		}
	})

	t.Run("all_levels", func(t *testing.T) {
		src := bytes.Repeat([]byte("AppendCompress test data across levels! "), 500)
		for level := 1; level <= BestCompression; level++ {
			t.Run("", func(t *testing.T) {
				e := NewEncoder()
				if err := e.SetLevel(level); err != nil {
					t.Fatal(err)
				}
				compressed := e.AppendCompress(nil, src)
				r := NewReader(bytes.NewReader(compressed), nil)
				defer func() { _ = r.Close() }()
				got, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, src) {
					t.Fatalf("AppendCompress mismatch at level %d", level)
				}
			})
		}
	})
}

func TestEncoder_AppendCompress_Concurrent(t *testing.T) {
	t.Run("same_encoder", func(t *testing.T) {
		src := bytes.Repeat([]byte("concurrent compress test data! "), 500)
		e := NewEncoder()

		const goroutines = 8
		var wg sync.WaitGroup
		wg.Add(goroutines)
		errs := make(chan error, goroutines)

		for range goroutines {
			go func() {
				defer wg.Done()
				compressed := e.AppendCompress(nil, src)
				r := NewReader(bytes.NewReader(compressed), nil)
				defer r.Close()
				got, err := io.ReadAll(r)
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

	t.Run("all_levels", func(t *testing.T) {
		src := bytes.Repeat([]byte("concurrent levels "), 500)

		const goroutines = 10
		var wg sync.WaitGroup
		wg.Add(goroutines)
		errs := make(chan error, goroutines)

		for i := range goroutines {
			level := i % (BestCompression + 1)
			go func() {
				defer wg.Done()
				le := NewEncoder()
				_ = le.SetLevel(level)
				compressed := le.AppendCompress(nil, src)
				var d Decoder
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

func TestEncoder_SetLevel(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		e := NewEncoder()
		if err := e.SetLevel(DefaultCompression); err != nil {
			t.Fatal(err)
		}
		if err := e.SetLevel(NoCompression); err != nil {
			t.Fatal(err)
		}
		if err := e.SetLevel(BestSpeed); err != nil {
			t.Fatal(err)
		}
		if err := e.SetLevel(BestCompression); err != nil {
			t.Fatal(err)
		}
		if err := e.SetLevel(10); err == nil {
			t.Fatal("expected error for invalid level")
		}
		if err := e.SetLevel(-2); err == nil {
			t.Fatal("expected error for invalid level")
		}
	})

	t.Run("switching", func(t *testing.T) {
		src := bytes.Repeat([]byte("level switching test "), 200)
		e := NewEncoder()
		w := NewWriter(nil, e)
		for level := 1; level <= BestCompression; level++ {
			var buf bytes.Buffer
			_ = e.SetLevel(level)
			w.Reset(&buf)
			_, _ = w.Write(src)
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}

			r := NewReader(bytes.NewReader(buf.Bytes()), nil)
			got, err := io.ReadAll(r)
			_ = r.Close()
			if err != nil {
				t.Fatalf("level %d: %v", level, err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("level %d: mismatch", level)
			}
		}
	})

	t.Run("no_compression_raw", func(t *testing.T) {
		src := bytes.Repeat([]byte("hello world! this is highly compressible data. "), 1000)
		var buf bytes.Buffer
		e := NewEncoder()
		if err := e.SetLevel(NoCompression); err != nil {
			t.Fatal(err)
		}
		w := NewWriter(&buf, e)
		w.Reset(&buf)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if buf.Len() < len(src) {
			t.Errorf("NoCompression reduced size: src=%d compressed=%d", len(src), buf.Len())
		}
		r := NewReader(bytes.NewReader(buf.Bytes()), nil)
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("roundtrip mismatch")
		}

		// Also test AppendCompress path.
		buf.Reset()
		e2 := NewEncoder()
		if err := e2.SetLevel(NoCompression); err != nil {
			t.Fatal(err)
		}
		compressed := e2.AppendCompress(nil, src)
		if len(compressed) < len(src) {
			t.Errorf("AppendCompress NoCompression reduced size: src=%d compressed=%d", len(src), len(compressed))
		}
		r2 := NewReader(bytes.NewReader(compressed), nil)
		defer r2.Close()
		got2, err := io.ReadAll(r2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got2, src) {
			t.Fatal("AppendCompress roundtrip mismatch")
		}
	})
}

func TestEncoder_SetWindowSize(t *testing.T) {
	t.Run("validation", func(t *testing.T) {
		e := NewEncoder()
		if err := e.SetWindowSize(MinWindowSize); err != nil {
			t.Fatal(err)
		}
		if err := e.SetWindowSize(MaxWindowSize); err != nil {
			t.Fatal(err)
		}
		if err := e.SetWindowSize(MinWindowSize - 1); err == nil {
			t.Fatal("expected error for below min")
		}
		if err := e.SetWindowSize(MaxWindowSize + 1); err == nil {
			t.Fatal("expected error for above max")
		}
		if err := e.SetWindowSize(0); err == nil {
			t.Fatal("expected error for 0")
		}
		if err := e.SetWindowSize(-1); err == nil {
			t.Fatal("expected error for negative")
		}
	})

	t.Run("round_trip", func(t *testing.T) {
		for _, wnd := range []int{1 << 16, 1 << 20, 8 << 20} {
			// Keep data well under window size.
			src := bytes.Repeat([]byte("wnd "), wnd/8)
			e := NewEncoder()
			_ = e.SetWindowSize(wnd)
			frame := e.AppendCompress(nil, src)
			var d Decoder
			got, err := d.AppendDecompress(nil, frame)
			if err != nil {
				t.Fatalf("window %d: %v", wnd, err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("window %d: mismatch", wnd)
			}
		}
	})
}

func TestEncoder_SetCRC(t *testing.T) {
	src := bytes.Repeat([]byte("crc test "), 200)

	withCRC := NewEncoder()
	withCRC.SetCRC(true)
	crcFrame := withCRC.AppendCompress(nil, src)

	noCRC := NewEncoder()
	noCRC.SetCRC(false)
	noCRCFrame := noCRC.AppendCompress(nil, src)

	// CRC adds 4 bytes (checksum) + 1 bit in FHD.
	if len(crcFrame) <= len(noCRCFrame) {
		t.Fatalf("CRC frame (%d) should be larger than no-CRC frame (%d)", len(crcFrame), len(noCRCFrame))
	}

	// Both must decompress correctly.
	var d Decoder
	for _, frame := range [][]byte{crcFrame, noCRCFrame} {
		got, err := d.AppendDecompress(nil, frame)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	}
}

func TestEncoder_ZeroValue(t *testing.T) {
	t.Run("append_compress", func(t *testing.T) {
		src := bytes.Repeat([]byte("zero value writer "), 100)
		var e Encoder
		compressed := e.AppendCompress(nil, src)

		r := NewReader(bytes.NewReader(compressed), nil)
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	})

	t.Run("config", func(t *testing.T) {
		src := bytes.Repeat([]byte("zero config "), 100)
		var e Encoder
		e.SetCRC(false)
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
}

// These tests exercise the split configuration model: one Encoder/Decoder holds
// configuration and can be reused across, or reconfigured between, streams.

func TestEncoderReuseAcrossWriters(t *testing.T) {
	e := NewEncoder()
	if err := e.SetLevel(5); err != nil {
		t.Fatal(err)
	}
	for _, src := range [][]byte{
		bytes.Repeat([]byte("alpha "), 300),
		bytes.Repeat([]byte("beta "), 300),
	} {
		var buf bytes.Buffer
		w := NewWriter(&buf, e)
		if _, err := w.Write(src); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := NewDecoder().AppendDecompress(nil, buf.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	}
}

// TestEncoderSharedInterleavedWriters proves two Writers bound to one Encoder
// keep independent streaming state.
func TestEncoderSharedInterleavedWriters(t *testing.T) {
	e := NewEncoder()
	var b1, b2 bytes.Buffer
	w1 := NewWriter(&b1, e)
	w2 := NewWriter(&b2, e)
	src1 := bytes.Repeat([]byte("first stream "), 500)
	src2 := bytes.Repeat([]byte("second stream "), 500)

	if _, err := w1.Write(src1[:len(src1)/2]); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Write(src2[:len(src2)/2]); err != nil {
		t.Fatal(err)
	}
	if _, err := w1.Write(src1[len(src1)/2:]); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Write(src2[len(src2)/2:]); err != nil {
		t.Fatal(err)
	}
	if err := w1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w2.Close(); err != nil {
		t.Fatal(err)
	}

	if got, err := NewDecoder().AppendDecompress(nil, b1.Bytes()); err != nil || !bytes.Equal(got, src1) {
		t.Fatalf("w1: err=%v match=%v", err, bytes.Equal(got, src1))
	}
	if got, err := NewDecoder().AppendDecompress(nil, b2.Bytes()); err != nil || !bytes.Equal(got, src2) {
		t.Fatalf("w2: err=%v match=%v", err, bytes.Equal(got, src2))
	}
}

// TestEncoderReconfigureBetweenStreams changes settings on a bound Encoder
// between streams and verifies the change takes effect on the next Reset.
func TestEncoderReconfigureBetweenStreams(t *testing.T) {
	src := bytes.Repeat([]byte("reconfigure "), 200)
	e := NewEncoder()
	w := NewWriter(nil, e)

	e.SetCRC(true)
	var withCRC bytes.Buffer
	w.Reset(&withCRC)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Reconfigure the SAME encoder, then reuse the SAME writer.
	e.SetCRC(false)
	var noCRC bytes.Buffer
	w.Reset(&noCRC)
	if _, err := w.Write(src); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// The change must be observable: the checksummed frame is 4 bytes longer.
	if withCRC.Len() != noCRC.Len()+4 {
		t.Fatalf("CRC toggle not applied: withCRC=%d noCRC=%d", withCRC.Len(), noCRC.Len())
	}
	for _, b := range []*bytes.Buffer{&withCRC, &noCRC} {
		got, err := NewDecoder().AppendDecompress(nil, b.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatal("mismatch")
		}
	}
}

// TestEncoderReconfigureMidStreamDoesNotCorrupt verifies that reconfiguring the
// bound Encoder mid-stream leaves the in-flight stream valid; the configuration
// captured at Reset governs the current frame and the change applies at the next
// Reset. (Toggling CRC mid-stream is unsupported; changing the level is safe.)
func TestEncoderReconfigureMidStreamDoesNotCorrupt(t *testing.T) {
	// Larger than one block so blocks are encoded during Write, not just at Close.
	src := bytes.Repeat([]byte("mid stream reconfiguration test "), 10000)
	e := NewEncoder()
	if err := e.SetLevel(1); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := NewWriter(&buf, e)

	if _, err := w.Write(src[:len(src)/2]); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLevel(9); err != nil { // mid-stream change
		t.Fatal(err)
	}
	if _, err := w.Write(src[len(src)/2:]); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := NewDecoder().AppendDecompress(nil, buf.Bytes())
	if err != nil {
		t.Fatalf("mid-stream reconfig corrupted the frame: %v", err)
	}
	if !bytes.Equal(got, src) {
		t.Fatal("mismatch")
	}
}
