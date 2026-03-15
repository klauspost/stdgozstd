// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fse

import "fmt"

// bitWriter writes bits in LSB order to an output byte slice.
type bitWriter struct {
	bitContainer uint64
	nBits        uint8
	out          []byte
}

// bitMask16 maps bit count to its corresponding mask value.
var bitMask16 = [32]uint16{
	0, 1, 3, 7, 0xF, 0x1F,
	0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF,
	0xFFF, 0x1FFF, 0x3FFF, 0x7FFF, 0xFFFF, 0xFFFF,
	0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF, 0xFFFF,
	0xFFFF, 0xFFFF,
}

// addBits16NC adds bits with masking but no capacity check.
func (b *bitWriter) addBits16NC(value uint16, bits uint8) {
	b.bitContainer |= uint64(value&bitMask16[bits&31]) << (b.nBits & 63)
	b.nBits += bits
}

// addBits16Clean adds bits assuming value has no excess high bits.
func (b *bitWriter) addBits16Clean(value uint16, bits uint8) {
	b.bitContainer |= uint64(value) << (b.nBits & 63)
	b.nBits += bits
}

// addBits16ZeroNC adds bits, handling zero-bit case, no capacity check.
func (b *bitWriter) addBits16ZeroNC(value uint16, bits uint8) {
	if bits == 0 {
		return
	}
	value <<= (16 - bits) & 15
	value >>= (16 - bits) & 15
	b.bitContainer |= uint64(value) << (b.nBits & 63)
	b.nBits += bits
}

// flush writes all complete bytes from the bit container.
func (b *bitWriter) flush() {
	v := b.nBits >> 3
	switch v {
	case 0:
	case 1:
		b.out = append(b.out, byte(b.bitContainer))
	case 2:
		b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8))
	case 3:
		b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8), byte(b.bitContainer>>16))
	case 4:
		b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8), byte(b.bitContainer>>16), byte(b.bitContainer>>24))
	case 5:
		b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8), byte(b.bitContainer>>16), byte(b.bitContainer>>24), byte(b.bitContainer>>32))
	case 6:
		b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8), byte(b.bitContainer>>16), byte(b.bitContainer>>24), byte(b.bitContainer>>32), byte(b.bitContainer>>40))
	case 7:
		b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8), byte(b.bitContainer>>16), byte(b.bitContainer>>24), byte(b.bitContainer>>32), byte(b.bitContainer>>40), byte(b.bitContainer>>48))
	case 8:
		b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8), byte(b.bitContainer>>16), byte(b.bitContainer>>24), byte(b.bitContainer>>32), byte(b.bitContainer>>40), byte(b.bitContainer>>48), byte(b.bitContainer>>56))
	default:
		panic(fmt.Errorf("bits (%d) > 64", b.nBits))
	}
	b.bitContainer >>= v << 3
	b.nBits &= 7
}

// flush32 writes 4 bytes if at least 32 bits are pending.
func (b *bitWriter) flush32() {
	if b.nBits < 32 {
		return
	}
	b.out = append(b.out, byte(b.bitContainer), byte(b.bitContainer>>8), byte(b.bitContainer>>16), byte(b.bitContainer>>24))
	b.nBits -= 32
	b.bitContainer >>= 32
}

// flushAlign writes all remaining bits, padding to byte boundary.
func (b *bitWriter) flushAlign() {
	nbBytes := (b.nBits + 7) >> 3
	for i := range nbBytes {
		b.out = append(b.out, byte(b.bitContainer>>(i*8)))
	}
	b.nBits = 0
	b.bitContainer = 0
}

// close writes the end-of-stream marker and flushes remaining bits.
func (b *bitWriter) close() {
	b.addBits16Clean(1, 1)
	b.flushAlign()
}

// reset clears the writer state and sets the output buffer.
func (b *bitWriter) reset(out []byte) {
	b.bitContainer = 0
	b.nBits = 0
	b.out = out
}
