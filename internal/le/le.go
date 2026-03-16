// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package le provides little endian loading and storing.
package le

import "encoding/binary"

// Indexer is a constraint for integer types usable as byte slice indices.
type Indexer interface {
	int | int8 | int16 | int32 | int64 | uint | uint8 | uint16 | uint32 | uint64
}

// Load8 loads a single byte from b at index i.
func Load8[I Indexer](b []byte, i I) byte {
	return b[i]
}

// Load16 loads a little-endian uint16 from b at index i.
func Load16[I Indexer](b []byte, i I) uint16 {
	return binary.LittleEndian.Uint16(b[i:])
}

// Load32 loads a little-endian uint32 from b at index i.
func Load32[I Indexer](b []byte, i I) uint32 {
	return binary.LittleEndian.Uint32(b[i:])
}

// Load64 loads a little-endian uint64 from b at index i.
func Load64[I Indexer](b []byte, i I) uint64 {
	return binary.LittleEndian.Uint64(b[i:])
}

// Store16 stores a uint16 as little-endian into b.
func Store16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
}

// Store32 stores a uint32 as little-endian into b.
func Store32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b, v)
}

// Store64 stores a uint64 as little-endian into b at index i.
func Store64[I Indexer](b []byte, i I, v uint64) {
	binary.LittleEndian.PutUint64(b[i:], v)
}
