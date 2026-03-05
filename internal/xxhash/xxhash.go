// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xxhash

import (
	"encoding/binary"
	"math/bits"
)

const (
	xxhPrime64c1 = 0x9e3779b185ebca87
	xxhPrime64c2 = 0xc2b2ae3d27d4eb4f
	xxhPrime64c3 = 0x165667b19e3779f9
	xxhPrime64c4 = 0x85ebca77c2b2ae63
	xxhPrime64c5 = 0x27d4eb2f165667c5
)

// Digest implements hash.Hash64.
type Digest struct {
	len uint64    // total length hashed
	v   [4]uint64 // accumulators
	buf [32]byte  // buffer
	cnt int       // number of bytes in buffer
}

func New() *Digest {
	var d Digest
	d.Reset()
	return &d
}

// Reset discards the current state and prepares to compute a new hash.
// We assume a seed of 0 since that is what zstd uses.
func (xh *Digest) Reset() {
	xh.len = 0

	// Separate addition for awkward constant overflow.
	xh.v[0] = xxhPrime64c1
	xh.v[0] += xxhPrime64c2

	xh.v[1] = xxhPrime64c2
	xh.v[2] = 0

	// Separate negation for awkward constant overflow.
	xh.v[3] = xxhPrime64c1
	xh.v[3] = -xh.v[3]

	clear(xh.buf[:])
	xh.cnt = 0
}

func (d *Digest) Size() int      { return 8 }
func (d *Digest) BlockSize() int { return 32 }

// update adds a buffer to the has.
func (xh *Digest) update(b []byte) {
	xh.len += uint64(len(b))

	if xh.cnt+len(b) < len(xh.buf) {
		copy(xh.buf[xh.cnt:], b)
		xh.cnt += len(b)
		return
	}

	if xh.cnt > 0 {
		n := copy(xh.buf[xh.cnt:], b)
		b = b[n:]
		xh.v[0] = round(xh.v[0], binary.LittleEndian.Uint64(xh.buf[:]))
		xh.v[1] = round(xh.v[1], binary.LittleEndian.Uint64(xh.buf[8:]))
		xh.v[2] = round(xh.v[2], binary.LittleEndian.Uint64(xh.buf[16:]))
		xh.v[3] = round(xh.v[3], binary.LittleEndian.Uint64(xh.buf[24:]))
		xh.cnt = 0
	}

	for len(b) >= 32 {
		xh.v[0] = round(xh.v[0], binary.LittleEndian.Uint64(b))
		xh.v[1] = round(xh.v[1], binary.LittleEndian.Uint64(b[8:]))
		xh.v[2] = round(xh.v[2], binary.LittleEndian.Uint64(b[16:]))
		xh.v[3] = round(xh.v[3], binary.LittleEndian.Uint64(b[24:]))
		b = b[32:]
	}

	if len(b) > 0 {
		copy(xh.buf[:], b)
		xh.cnt = len(b)
	}
}

func (d *Digest) Write(b []byte) (n int, err error) {
	d.update(b)
	return len(b), nil
}

func (d *Digest) Sum(b []byte) []byte {
	s := d.Sum64()
	return append(b, byte(s>>56), byte(s>>48), byte(s>>40), byte(s>>32),
		byte(s>>24), byte(s>>16), byte(s>>8), byte(s))
}

func (xh *Digest) Sum64() uint64 {
	var h64 uint64
	if xh.len < 32 {
		h64 = xh.v[2] + xxhPrime64c5
	} else {
		h64 = bits.RotateLeft64(xh.v[0], 1) +
			bits.RotateLeft64(xh.v[1], 7) +
			bits.RotateLeft64(xh.v[2], 12) +
			bits.RotateLeft64(xh.v[3], 18)
		h64 = mergeRound(h64, xh.v[0])
		h64 = mergeRound(h64, xh.v[1])
		h64 = mergeRound(h64, xh.v[2])
		h64 = mergeRound(h64, xh.v[3])
	}

	h64 += xh.len

	len := xh.len
	len &= 31
	buf := xh.buf[:]
	for len >= 8 {
		k1 := round(0, binary.LittleEndian.Uint64(buf))
		buf = buf[8:]
		h64 ^= k1
		h64 = bits.RotateLeft64(h64, 27)*xxhPrime64c1 + xxhPrime64c4
		len -= 8
	}
	if len >= 4 {
		h64 ^= uint64(binary.LittleEndian.Uint32(buf)) * xxhPrime64c1
		buf = buf[4:]
		h64 = bits.RotateLeft64(h64, 23)*xxhPrime64c2 + xxhPrime64c3
		len -= 4
	}
	for len > 0 {
		h64 ^= uint64(buf[0]) * xxhPrime64c5
		buf = buf[1:]
		h64 = bits.RotateLeft64(h64, 11) * xxhPrime64c1
		len--
	}

	h64 ^= h64 >> 33
	h64 *= xxhPrime64c2
	h64 ^= h64 >> 29
	h64 *= xxhPrime64c3
	h64 ^= h64 >> 32

	return h64
}

func round(v, n uint64) uint64 {
	v += n * xxhPrime64c2
	v = bits.RotateLeft64(v, 31)
	v *= xxhPrime64c1
	return v
}

func mergeRound(v, n uint64) uint64 {
	n = round(0, n)
	v ^= n
	v = v*xxhPrime64c1 + xxhPrime64c4
	return v
}

// Sum64 computes the xxHash-64 digest of b with seed 0.
func Sum64(b []byte) uint64 {
	n := uint64(len(b))
	var h64 uint64

	if n >= 32 {
		v1 := uint64(xxhPrime64c1)
		v1 += xxhPrime64c2
		v2 := uint64(xxhPrime64c2)
		v3 := uint64(0)
		v4 := uint64(xxhPrime64c1)
		v4 = -v4
		for len(b) >= 32 {
			v1 = round(v1, binary.LittleEndian.Uint64(b))
			v2 = round(v2, binary.LittleEndian.Uint64(b[8:]))
			v3 = round(v3, binary.LittleEndian.Uint64(b[16:]))
			v4 = round(v4, binary.LittleEndian.Uint64(b[24:]))
			b = b[32:]
		}
		h64 = bits.RotateLeft64(v1, 1) +
			bits.RotateLeft64(v2, 7) +
			bits.RotateLeft64(v3, 12) +
			bits.RotateLeft64(v4, 18)
		h64 = mergeRound(h64, v1)
		h64 = mergeRound(h64, v2)
		h64 = mergeRound(h64, v3)
		h64 = mergeRound(h64, v4)
	} else {
		h64 = xxhPrime64c5
	}

	h64 += n

	for len(b) >= 8 {
		k1 := round(0, binary.LittleEndian.Uint64(b))
		b = b[8:]
		h64 ^= k1
		h64 = bits.RotateLeft64(h64, 27)*xxhPrime64c1 + xxhPrime64c4
	}
	if len(b) >= 4 {
		h64 ^= uint64(binary.LittleEndian.Uint32(b)) * xxhPrime64c1
		b = b[4:]
		h64 = bits.RotateLeft64(h64, 23)*xxhPrime64c2 + xxhPrime64c3
	}
	for len(b) > 0 {
		h64 ^= uint64(b[0]) * xxhPrime64c5
		b = b[1:]
		h64 = bits.RotateLeft64(h64, 11) * xxhPrime64c1
	}

	h64 ^= h64 >> 33
	h64 *= xxhPrime64c2
	h64 ^= h64 >> 29
	h64 *= xxhPrime64c3
	h64 ^= h64 >> 32

	return h64
}
