// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package huff0

type bitWriter struct {
	bitContainer uint64
	nBits        uint8
	out          []byte
}

func (b *bitWriter) addBits16Clean(value uint16, bits uint8) {
	b.bitContainer |= uint64(value) << (b.nBits & 63)
	b.nBits += bits
}

func (b *bitWriter) encSymbol(ct cTable, symbol byte) {
	enc := ct[symbol]
	b.bitContainer |= uint64(enc.val) << (b.nBits & 63)
	b.nBits += enc.nBits
}

func (b *bitWriter) encTwoSymbols(ct cTable, av, bv byte) {
	encA := ct[av]
	encB := ct[bv]
	sh := b.nBits & 63
	combined := uint64(encA.val) | (uint64(encB.val) << (encA.nBits & 63))
	b.bitContainer |= combined << sh
	b.nBits += encA.nBits + encB.nBits
}

func (b *bitWriter) encFourSymbols(encA, encB, encC, encD cTableEntry) {
	bitsA := encA.nBits
	bitsB := bitsA + encB.nBits
	bitsC := bitsB + encC.nBits
	bitsD := bitsC + encD.nBits
	combined := uint64(encA.val) |
		(uint64(encB.val) << (bitsA & 63)) |
		(uint64(encC.val) << (bitsB & 63)) |
		(uint64(encD.val) << (bitsC & 63))
	b.bitContainer |= combined << (b.nBits & 63)
	b.nBits += bitsD
}

func (b *bitWriter) flush32() {
	if b.nBits < 32 {
		return
	}
	b.out = append(b.out,
		byte(b.bitContainer),
		byte(b.bitContainer>>8),
		byte(b.bitContainer>>16),
		byte(b.bitContainer>>24))
	b.nBits -= 32
	b.bitContainer >>= 32
}

func (b *bitWriter) flushAlign() {
	nbBytes := (b.nBits + 7) >> 3
	for i := range nbBytes {
		b.out = append(b.out, byte(b.bitContainer>>(i*8)))
	}
	b.nBits = 0
	b.bitContainer = 0
}

func (b *bitWriter) close() {
	b.addBits16Clean(1, 1)
	b.flushAlign()
}
