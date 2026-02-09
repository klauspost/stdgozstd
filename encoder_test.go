// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"math/rand/v2"
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
