// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "testing"

func TestLLCode(t *testing.T) {
	tests := []struct {
		val  uint32
		want uint8
	}{
		{0, 0}, {15, 15}, {16, 16}, {17, 16}, {63, 24}, {64, 25}, {128, 26},
	}
	for _, tt := range tests {
		if got := llCode(tt.val); got != tt.want {
			t.Fatalf("llCode(%d) = %d, want %d", tt.val, got, tt.want)
		}
	}
}

func TestMLCode(t *testing.T) {
	tests := []struct {
		val  uint32
		want uint8
	}{
		{0, 0}, {31, 31}, {32, 32}, {33, 32}, {127, 42}, {128, 43},
	}
	for _, tt := range tests {
		if got := mlCode(tt.val); got != tt.want {
			t.Fatalf("mlCode(%d) = %d, want %d", tt.val, got, tt.want)
		}
	}
}

func TestOfCode(t *testing.T) {
	tests := []struct {
		val  uint32
		want uint8
	}{
		{1, 0}, {2, 1}, {4, 2}, {8, 3}, {256, 8}, {1024, 10},
	}
	for _, tt := range tests {
		if got := ofCode(tt.val); got != tt.want {
			t.Fatalf("ofCode(%d) = %d, want %d", tt.val, got, tt.want)
		}
	}
}
