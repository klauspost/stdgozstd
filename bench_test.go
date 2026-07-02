// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"
)

type benchInput struct {
	name string
	data []byte
}

var benchInputs = []benchInput{
	{"json-1k", genJSON(1024, 45)},
	{"json-64k", genJSON(64*1024, 46)},
	{"json-1m", genJSON(1<<20, 47)},
	{"zeros-1k", make([]byte, 1024)},
	{"zeros-64k", make([]byte, 64*1024)},
	{"zeros-1m", make([]byte, 1<<20)},
	{"random-1k", randBytes(1024, 42)},
	{"random-64k", randBytes(64*1024, 43)},
	{"random-1m", randBytes(1<<20, 44)},
}

func randBytes(n int, seed uint64) []byte {
	b := make([]byte, n)
	rng := rand.New(rand.NewPCG(seed, seed+1))
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return b
}

func genJSON(size int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	var buf []byte
	buf = append(buf, '[')
	id := 0
	for len(buf) < size {
		if id > 0 {
			buf = append(buf, ',')
		}
		rec := genRecord(rng, id)
		b, _ := json.Marshal(rec)
		buf = append(buf, b...)
		id++
	}
	buf = append(buf, ']')
	if len(buf) > size {
		buf = buf[:size]
	}
	return buf
}

var (
	firstNames = []string{"Alice", "Bob", "Carol", "David", "Eve", "Frank", "Grace", "Henry", "Iris", "Jack"}
	lastNames  = []string{"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis", "Wilson", "Taylor"}
	cities     = []string{"New York", "San Francisco", "London", "Tokyo", "Berlin", "Sydney", "Toronto", "Paris", "Seoul", "Mumbai"}
	roles      = []string{"engineer", "manager", "analyst", "designer", "director", "intern", "lead", "architect"}
	tags       = []string{"active", "premium", "trial", "enterprise", "beta", "legacy", "internal", "external"}
)

func pick(rng *rand.Rand, s []string) string { return s[rng.IntN(len(s))] }

func genRecord(rng *rand.Rand, id int) map[string]any {
	nTags := 1 + rng.IntN(4)
	tagList := make([]string, nTags)
	for i := range tagList {
		tagList[i] = pick(rng, tags)
	}
	rec := map[string]any{
		"id":        id,
		"firstName": pick(rng, firstNames),
		"lastName":  pick(rng, lastNames),
		"email":     fmt.Sprintf("user%d@example.com", id),
		"age":       20 + rng.IntN(50),
		"score":     rng.Float64() * 100,
		"active":    rng.IntN(2) == 1,
		"role":      pick(rng, roles),
		"tags":      tagList,
		"address": map[string]any{
			"city":    pick(rng, cities),
			"zip":     fmt.Sprintf("%05d", rng.IntN(100000)),
			"floor":   rng.IntN(30),
			"primary": rng.IntN(2) == 1,
		},
	}
	if rng.IntN(3) == 0 {
		rec["notes"] = fmt.Sprintf("Note for user %d: %s %s is based in %s.",
			id, rec["firstName"], rec["lastName"], rec["address"].(map[string]any)["city"])
	}
	return rec
}

var benchLevels = []struct {
	name  string
	level int
}{
	{"speed", BestSpeed},
	{"default", 3},
	{"5", 5},
	{"best", BestCompression},
}

func BenchmarkAppendCompress(b *testing.B) {
	for _, input := range benchInputs {
		for _, lvl := range benchLevels {
			b.Run(input.name+"/"+lvl.name, func(b *testing.B) {
				e := NewEncoder()
				_ = e.SetLevel(lvl.level)
				var dst []byte
				b.SetBytes(int64(len(input.data)))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					dst = e.AppendCompress(dst[:0], input.data)
				}
				b.StopTimer()
				b.ReportMetric(100-100*float64(len(dst))/float64(len(input.data)), "reduction%")
			})
		}
	}
}

type countWriter int64

func (c *countWriter) Write(p []byte) (int, error) {
	*c += countWriter(len(p))
	return len(p), nil
}

func BenchmarkWrite(b *testing.B) {
	for _, input := range benchInputs {
		for _, lvl := range benchLevels {
			b.Run(input.name+"/"+lvl.name, func(b *testing.B) {
				var cw countWriter
				e := NewEncoder()
				_ = e.SetLevel(lvl.level)
				w := NewWriter(&cw, e)
				b.SetBytes(int64(len(input.data)))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					cw = 0
					w.Reset(&cw)
					_, _ = w.Write(input.data)
					_ = w.Close()
				}
				b.StopTimer()
				b.ReportMetric(100-100*float64(cw)/float64(len(input.data)), "reduction%")
			})
		}
	}
}

func BenchmarkReadFrom(b *testing.B) {
	for _, input := range benchInputs {
		for _, lvl := range benchLevels {
			b.Run(input.name+"/"+lvl.name, func(b *testing.B) {
				var cw countWriter
				e := NewEncoder()
				_ = e.SetLevel(lvl.level)
				w := NewWriter(&cw, e)
				b.SetBytes(int64(len(input.data)))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					cw = 0
					w.Reset(&cw)
					_, _ = w.ReadFrom(bytes.NewReader(input.data))
					_ = w.Close()
				}
				b.StopTimer()
				b.ReportMetric(100-100*float64(cw)/float64(len(input.data)), "reduction%")
			})
		}
	}
}

type preCompressed struct {
	name       string
	src        []byte
	compressed []byte
}

func preCompress(inputs []benchInput, level int) []preCompressed {
	e := NewEncoder()
	_ = e.SetLevel(level)
	out := make([]preCompressed, len(inputs))
	for i, in := range inputs {
		out[i] = preCompressed{
			name:       in.name,
			src:        in.data,
			compressed: e.AppendCompress(nil, in.data),
		}
	}
	return out
}

func BenchmarkAppendDecompress(b *testing.B) {
	for _, lvl := range benchLevels {
		pcs := preCompress(benchInputs, lvl.level)
		for _, pc := range pcs {
			b.Run(pc.name+"/"+lvl.name, func(b *testing.B) {
				d := NewDecoder()
				var dst []byte
				b.SetBytes(int64(len(pc.src)))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					dst, _ = d.AppendDecompress(dst[:0], pc.compressed)
				}
			})
		}
	}
}

func BenchmarkRead(b *testing.B) {
	for _, lvl := range benchLevels {
		pcs := preCompress(benchInputs, lvl.level)
		for _, pc := range pcs {
			b.Run(pc.name+"/"+lvl.name, func(b *testing.B) {
				r := NewReader(bytes.NewReader(pc.compressed), nil)
				b.SetBytes(int64(len(pc.src)))
				b.ResetTimer()
				for range b.N {
					_ = r.Reset(bytes.NewReader(pc.compressed))
					_, _ = io.ReadAll(r)
				}
			})
		}
	}
}

func BenchmarkWriteTo(b *testing.B) {
	for _, lvl := range benchLevels {
		pcs := preCompress(benchInputs, lvl.level)
		for _, pc := range pcs {
			b.Run(pc.name+"/"+lvl.name, func(b *testing.B) {
				r := NewReader(bytes.NewReader(pc.compressed), nil)
				b.SetBytes(int64(len(pc.src)))
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					_ = r.Reset(bytes.NewReader(pc.compressed))
					_, _ = r.WriteTo(io.Discard)
				}
			})
		}
	}
}

func BenchmarkAppendCompress_Dict(b *testing.B) {
	dict := genJSON(4*1024, 90)
	src := genJSON(64*1024, 91)

	for _, lvl := range benchLevels {
		b.Run("no-dict/"+lvl.name, func(b *testing.B) {
			e := NewEncoder()
			_ = e.SetLevel(lvl.level)
			var dst []byte
			b.SetBytes(int64(len(src)))
			b.ResetTimer()
			for range b.N {
				dst = e.AppendCompress(dst[:0], src)
			}
			b.StopTimer()
			b.ReportMetric(100-100*float64(len(dst))/float64(len(src)), "reduction%")
		})
		b.Run("with-dict/"+lvl.name, func(b *testing.B) {
			e := NewEncoder()
			_ = e.SetLevel(lvl.level)
			e.SetRawDict(dict)
			var dst []byte
			b.SetBytes(int64(len(src)))
			b.ResetTimer()
			for range b.N {
				dst = e.AppendCompress(dst[:0], src)
			}
			b.StopTimer()
			b.ReportMetric(100-100*float64(len(dst))/float64(len(src)), "reduction%")
		})
	}
}

func BenchmarkAppendDecompress_Dict(b *testing.B) {
	dict := genJSON(4*1024, 90)
	src := genJSON(64*1024, 91)

	for _, lvl := range benchLevels {
		e := NewEncoder()
		_ = e.SetLevel(lvl.level)
		e.SetRawDict(dict)
		compressed := e.AppendCompress(nil, src)

		b.Run(lvl.name, func(b *testing.B) {
			d := NewDecoder()
			d.SetRawDict(dict)
			var dst []byte
			b.SetBytes(int64(len(src)))
			b.ResetTimer()
			for range b.N {
				dst, _ = d.AppendDecompress(dst[:0], compressed)
			}
			b.StopTimer()
			b.ReportMetric(100-100*float64(len(compressed))/float64(len(src)), "reduction%")
		})
	}
}

func BenchmarkNewWriter(b *testing.B) {
	for range b.N {
		_ = NewWriter(io.Discard, nil)
	}
}

func BenchmarkNewReader(b *testing.B) {
	for range b.N {
		_ = NewReader(nil, nil)
	}
}

func BenchmarkWriterReuse(b *testing.B) {
	src := bytes.Repeat([]byte("reuse bench "), 500)
	w := NewWriter(io.Discard, nil)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		w.Reset(io.Discard)
		_, _ = w.Write(src)
		_ = w.Close()
	}
}

func BenchmarkReaderReuse(b *testing.B) {
	src := bytes.Repeat([]byte("reuse bench "), 500)
	e := NewEncoder()
	compressed := e.AppendCompress(nil, src)
	r := NewReader(bytes.NewReader(compressed), nil)
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = r.Reset(bytes.NewReader(compressed))
		_, _ = io.ReadAll(r)
	}
}
