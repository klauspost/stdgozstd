// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package le provides little endian loading and storing.
package le

import "encoding/binary"

type Indexer interface {
	int | int8 | int16 | int32 | int64 | uint | uint8 | uint16 | uint32 | uint64
}

func Load8[I Indexer](b []byte, i I) byte {
	return b[i]
}

func Load16[I Indexer](b []byte, i I) uint16 {
	return binary.LittleEndian.Uint16(b[i:])
}

func Load32[I Indexer](b []byte, i I) uint32 {
	return binary.LittleEndian.Uint32(b[i:])
}

func Load64[I Indexer](b []byte, i I) uint64 {
	return binary.LittleEndian.Uint64(b[i:])
}

func Store16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
}

func Store32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b, v)
}

func Store64[I Indexer](b []byte, i I, v uint64) {
	binary.LittleEndian.PutUint64(b[i:], v)
}
