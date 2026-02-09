// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

type byteReader struct {
	b   []byte
	off int
}

// advance moves the read position forward by n bytes.
func (b *byteReader) advance(n uint) {
	b.off += int(n)
}

// overread reports whether the reader advanced past the end.
func (b *byteReader) overread() bool {
	return b.off > len(b.b)
}

// Int32 reads a little-endian int32 at the current position.
func (b byteReader) Int32() int32 {
	b2 := b.b[b.off:]
	b2 = b2[:4]
	v3 := int32(b2[3])
	v2 := int32(b2[2])
	v1 := int32(b2[1])
	v0 := int32(b2[0])
	return v0 | (v1 << 8) | (v2 << 16) | (v3 << 24)
}

// Uint8 reads a single byte at the current position.
func (b *byteReader) Uint8() uint8 {
	return b.b[b.off]
}

// Uint32 reads a little-endian uint32, handling short buffers.
func (b byteReader) Uint32() uint32 {
	if r := b.remain(); r < 4 {
		v := uint32(0)
		for i := 1; i <= r; i++ {
			v = (v << 8) | uint32(b.b[len(b.b)-i])
		}
		return v
	}
	b2 := b.b[b.off:]
	b2 = b2[:4]
	v3 := uint32(b2[3])
	v2 := uint32(b2[2])
	v1 := uint32(b2[1])
	v0 := uint32(b2[0])
	return v0 | (v1 << 8) | (v2 << 16) | (v3 << 24)
}

// Uint32NC reads a little-endian uint32 without bounds checking.
func (b byteReader) Uint32NC() uint32 {
	b2 := b.b[b.off:]
	b2 = b2[:4]
	v3 := uint32(b2[3])
	v2 := uint32(b2[2])
	v1 := uint32(b2[1])
	v0 := uint32(b2[0])
	return v0 | (v1 << 8) | (v2 << 16) | (v3 << 24)
}

// unread returns the remaining unread bytes.
func (b byteReader) unread() []byte {
	return b.b[b.off:]
}

// remain returns the number of unread bytes.
func (b byteReader) remain() int {
	return len(b.b) - b.off
}
