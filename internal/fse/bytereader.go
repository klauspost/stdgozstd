// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fse

type byteReader struct {
	b   []byte
	off int
}

func (b *byteReader) init(in []byte) {
	b.b = in
	b.off = 0
}

func (b *byteReader) advance(n uint) {
	b.off += int(n)
}

func (b byteReader) Uint32() uint32 {
	b2 := b.b[b.off:]
	b2 = b2[:4]
	return uint32(b2[0]) | uint32(b2[1])<<8 | uint32(b2[2])<<16 | uint32(b2[3])<<24
}

func (b byteReader) unread() []byte {
	return b.b[b.off:]
}

func (b byteReader) remain() int {
	return len(b.b) - b.off
}
