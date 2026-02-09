// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"math"
	"math/bits"
)

type frameHeader struct {
	ContentSize   uint64
	WindowSize    uint32
	SingleSegment bool
	Checksum      bool
	DictID        uint32
}

const maxHeaderSize = 14

// appendTo serializes the frame header to dst.
func (f frameHeader) appendTo(dst []byte) []byte {
	dst = append(dst, frameMagic...)
	var fhd uint8
	if f.Checksum {
		fhd |= 1 << 2
	}
	if f.SingleSegment {
		fhd |= 1 << 5
	}

	var dictIDContent []byte
	if f.DictID > 0 {
		var tmp [4]byte
		if f.DictID < 256 {
			fhd |= 1
			tmp[0] = uint8(f.DictID)
			dictIDContent = tmp[:1]
		} else if f.DictID < 1<<16 {
			fhd |= 2
			tmp[0] = uint8(f.DictID)
			tmp[1] = uint8(f.DictID >> 8)
			dictIDContent = tmp[:2]
		} else {
			fhd |= 3
			tmp[0] = uint8(f.DictID)
			tmp[1] = uint8(f.DictID >> 8)
			tmp[2] = uint8(f.DictID >> 16)
			tmp[3] = uint8(f.DictID >> 24)
			dictIDContent = tmp[:4]
		}
	}
	var fcs uint8
	if f.ContentSize >= 256 {
		fcs++
	}
	if f.ContentSize >= 65536+256 {
		fcs++
	}
	if f.ContentSize >= 0xffffffff {
		fcs++
	}

	fhd |= fcs << 6

	dst = append(dst, fhd)
	if !f.SingleSegment {
		const winLogMin = 10
		windowLog := (bits.Len32(f.WindowSize-1) - winLogMin) << 3
		dst = append(dst, uint8(windowLog))
	}
	if f.DictID > 0 {
		dst = append(dst, dictIDContent...)
	}
	switch fcs {
	case 0:
		if f.SingleSegment {
			dst = append(dst, uint8(f.ContentSize))
		}
		// Unless SingleSegment is set, frame sizes < 256 are not stored.
	case 1:
		f.ContentSize -= 256
		dst = append(dst, uint8(f.ContentSize), uint8(f.ContentSize>>8))
	case 2:
		dst = append(dst, uint8(f.ContentSize), uint8(f.ContentSize>>8), uint8(f.ContentSize>>16), uint8(f.ContentSize>>24))
	case 3:
		dst = append(dst, uint8(f.ContentSize), uint8(f.ContentSize>>8), uint8(f.ContentSize>>16), uint8(f.ContentSize>>24),
			uint8(f.ContentSize>>32), uint8(f.ContentSize>>40), uint8(f.ContentSize>>48), uint8(f.ContentSize>>56))
	default:
		panic("invalid fcs")
	}
	return dst
}

type blockHeader uint32

// setLast marks this as the last block in the frame.
func (h *blockHeader) setLast(b bool) {
	if b {
		*h = *h | 1
	} else {
		const mask = (1 << 24) - 2
		*h = *h & mask
	}
}

// setSize sets the block content size.
func (h *blockHeader) setSize(v uint32) {
	const mask = 7
	*h = (*h)&mask | blockHeader(v<<3)
}

// setType sets the block type (raw, RLE, compressed).
func (h *blockHeader) setType(t blockType) {
	const mask = 1 | (((1 << 24) - 1) ^ 7)
	*h = (*h & mask) | blockHeader(t<<1)
}

// appendTo serializes the 3-byte block header to b.
func (h blockHeader) appendTo(b []byte) []byte {
	return append(b, uint8(h), uint8(h>>8), uint8(h>>16))
}

type literalsHeader uint64

// setType sets the literals block type.
func (h *literalsHeader) setType(t literalsBlockType) {
	const mask = math.MaxUint64 - 3
	*h = (*h & mask) | literalsHeader(t)
}

// setSize sets the regenerated (uncompressed) size for raw/RLE literals.
func (h *literalsHeader) setSize(regenLen int) {
	inBits := bits.Len32(uint32(regenLen))
	const mask = 3
	lh := uint64(*h & mask)
	switch {
	case inBits < 5:
		lh |= (uint64(regenLen) << 3) | (1 << 60)
	case inBits < 12:
		lh |= (1 << 2) | (uint64(regenLen) << 4) | (2 << 60)
	case inBits < 20:
		lh |= (3 << 2) | (uint64(regenLen) << 4) | (3 << 60)
	default:
		panic("block too big")
	}
	*h = literalsHeader(lh)
}

// setSizes sets both compressed and regenerated sizes for compressed literals.
func (h *literalsHeader) setSizes(compLen, inLen int, single bool) {
	compBits, inBits := bits.Len32(uint32(compLen)), bits.Len32(uint32(inLen))
	const mask = 3
	lh := uint64(*h & mask)
	switch {
	case compBits <= 10 && inBits <= 10:
		if !single {
			lh |= 1 << 2
		}
		lh |= (uint64(inLen) << 4) | (uint64(compLen) << (10 + 4)) | (3 << 60)
	case compBits <= 14 && inBits <= 14:
		lh |= (2 << 2) | (uint64(inLen) << 4) | (uint64(compLen) << (14 + 4)) | (4 << 60)
		if single {
			panic("single stream used with more than 10 bits length")
		}
	case compBits <= 18 && inBits <= 18:
		lh |= (3 << 2) | (uint64(inLen) << 4) | (uint64(compLen) << (18 + 4)) | (5 << 60)
		if single {
			panic("single stream used with more than 10 bits length")
		}
	default:
		panic("block too big")
	}
	*h = literalsHeader(lh)
}

// appendTo serializes the literals header to b.
func (h literalsHeader) appendTo(b []byte) []byte {
	size := uint8(h >> 60)
	switch size {
	case 1:
		b = append(b, uint8(h))
	case 2:
		b = append(b, uint8(h), uint8(h>>8))
	case 3:
		b = append(b, uint8(h), uint8(h>>8), uint8(h>>16))
	case 4:
		b = append(b, uint8(h), uint8(h>>8), uint8(h>>16), uint8(h>>24))
	case 5:
		b = append(b, uint8(h), uint8(h>>8), uint8(h>>16), uint8(h>>24), uint8(h>>32))
	default:
		panic("literalsHeader has invalid size")
	}
	return b
}

// size returns the serialized header size in bytes.
func (h literalsHeader) size() int {
	return int(h >> 60)
}
