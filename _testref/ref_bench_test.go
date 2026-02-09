// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstdref

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	ref "github.com/klauspost/compress/zstd"
	zstd "github.com/klauspost/stdgozstd"
)

func benchDatasets(b *testing.B) map[string][]byte {
	b.Helper()
	small := []byte(strings.Repeat("the quick brown fox jumps over the lazy dog. ", 23))[:1024]
	medium := loadTestFile(b, "z000028")
	large := testData(1 << 20)
	return map[string][]byte{"small": small, "medium": medium, "large": large}
}

func BenchmarkAppendTo(b *testing.B) {
	datasets := benchDatasets(b)
	levels := []int{1, 3, 5, 8}

	for _, level := range levels {
		for name, data := range datasets {
			b.Run(fmt.Sprintf("lite/level-%d/%s", level, name), func(b *testing.B) {
				w := zstd.NewWriter(nil)
				w.SetLevel(level)
				buf := make([]byte, 0, len(data))
				b.SetBytes(int64(len(data)))
				b.ResetTimer()
				for range b.N {
					w.AppendCompress(buf[:0], data)
				}
			})
			b.Run(fmt.Sprintf("ref/level-%d/%s", level, name), func(b *testing.B) {
				enc, err := ref.NewWriter(nil, ref.WithEncoderLevel(refLevel(level)))
				if err != nil {
					b.Fatal(err)
				}
				buf := make([]byte, 0, len(data))
				b.SetBytes(int64(len(data)))
				b.ResetTimer()
				for range b.N {
					enc.EncodeAll(data, buf[:0])
				}
			})
		}
	}
}

func BenchmarkStreamEncode(b *testing.B) {
	medium := loadTestFile(b, "z000028")
	levels := []int{1, 3, 5, 8}

	for _, level := range levels {
		b.Run(fmt.Sprintf("lite/level-%d/medium", level), func(b *testing.B) {
			w := zstd.NewWriter(nil)
			w.SetLevel(level)
			var buf bytes.Buffer
			b.SetBytes(int64(len(medium)))
			b.ResetTimer()
			for range b.N {
				buf.Reset()
				w.Reset(&buf)
				w.Write(medium)
				w.Close()
			}
		})
		b.Run(fmt.Sprintf("ref/level-%d/medium", level), func(b *testing.B) {
			enc, err := ref.NewWriter(nil, ref.WithEncoderLevel(refLevel(level)), ref.WithEncoderConcurrency(1))
			if err != nil {
				b.Fatal(err)
			}
			var buf bytes.Buffer
			b.SetBytes(int64(len(medium)))
			b.ResetTimer()
			for range b.N {
				buf.Reset()
				enc.Reset(&buf)
				enc.Write(medium)
				enc.Close()
			}
		})
	}
}

func BenchmarkAppendDecompress(b *testing.B) {
	datasets := benchDatasets(b)
	levels := []int{1, 3, 5, 8}

	for _, level := range levels {
		for name, data := range datasets {
			compressed := liteEncode(b, data, level)

			b.Run(fmt.Sprintf("lite/level-%d/%s", level, name), func(b *testing.B) {
				r, err := zstd.NewReader(bytes.NewReader([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x04, 0x00, 0x01, 0x00, 0x00}))
				if err != nil {
					b.Fatal(err)
				}
				defer r.Close()
				buf := make([]byte, 0, len(data))
				b.SetBytes(int64(len(data)))
				b.ResetTimer()
				for range b.N {
					r.AppendDecompress(buf[:0], compressed)
				}
			})
			b.Run(fmt.Sprintf("ref/level-%d/%s", level, name), func(b *testing.B) {
				dec, err := ref.NewReader(nil)
				if err != nil {
					b.Fatal(err)
				}
				defer dec.Close()
				buf := make([]byte, 0, len(data))
				b.SetBytes(int64(len(data)))
				b.ResetTimer()
				for range b.N {
					dec.DecodeAll(compressed, buf[:0])
				}
			})
		}
	}
}

func BenchmarkStreamDecode(b *testing.B) {
	medium := loadTestFile(b, "z000028")
	levels := []int{1, 3, 5, 8}

	for _, level := range levels {
		compressed := liteEncode(b, medium, level)

		b.Run(fmt.Sprintf("lite/level-%d/medium", level), func(b *testing.B) {
			b.SetBytes(int64(len(medium)))
			b.ResetTimer()
			for range b.N {
				r, err := zstd.NewReader(bytes.NewReader(compressed))
				if err != nil {
					b.Fatal(err)
				}
				io.ReadAll(r)
				r.Close()
			}
		})
		b.Run(fmt.Sprintf("ref/level-%d/medium", level), func(b *testing.B) {
			dec, err := ref.NewReader(nil)
			if err != nil {
				b.Fatal(err)
			}
			defer dec.Close()
			b.SetBytes(int64(len(medium)))
			b.ResetTimer()
			for range b.N {
				dec.Reset(bytes.NewReader(compressed))
				io.ReadAll(dec)
			}
		})
	}
}

func BenchmarkCompressionRatio(b *testing.B) {
	datasets := benchDatasets(b)
	levels := []int{1, 3, 5, 8}

	for _, level := range levels {
		for name, data := range datasets {
			b.Run(fmt.Sprintf("lite/level-%d/%s", level, name), func(b *testing.B) {
				compressed := liteEncode(b, data, level)
				ratio := float64(len(compressed)) / float64(len(data))
				b.ReportMetric(ratio, "ratio")
				b.ReportMetric(float64(len(compressed)), "compressed-bytes")
				for range b.N {
				}
			})
			b.Run(fmt.Sprintf("ref/level-%d/%s", level, name), func(b *testing.B) {
				compressed := refEncode(b, data, ref.WithEncoderLevel(refLevel(level)))
				ratio := float64(len(compressed)) / float64(len(data))
				b.ReportMetric(ratio, "ratio")
				b.ReportMetric(float64(len(compressed)), "compressed-bytes")
				for range b.N {
				}
			})
		}
	}
}
