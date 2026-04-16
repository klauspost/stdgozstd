// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package huff0

// bitWriter writes Huffman-coded bits to a byte stream in little-endian order.
type bitWriter struct {
	bitContainer uint64
	nBits        uint8
	out          []byte
}

// addBits16Clean adds up to 16 bits. Value must be clean (no excess high bits).
func (b *bitWriter) addBits16Clean(value uint16, bits uint8) {
	b.bitContainer |= uint64(value) << (b.nBits & 63)
	b.nBits += bits
}

// encSymbol encodes a single symbol using the compression table.
func (b *bitWriter) encSymbol(ct cTable, symbol byte) {
	enc := ct[symbol]
	b.bitContainer |= uint64(enc.val) << (b.nBits & 63)
	b.nBits += enc.nBits
}

// encTwoSymbols encodes two symbols in a single combined operation.
func (b *bitWriter) encTwoSymbols(ct cTable, av, bv byte) {
	encA := ct[av]
	encB := ct[bv]
	sh := b.nBits & 63
	combined := uint64(encA.val) | (uint64(encB.val) << (encA.nBits & 63))
	b.bitContainer |= combined << sh
	b.nBits += encA.nBits + encB.nBits
}

// encFourSymbols encodes four pre-looked-up symbols in a single combined operation.
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

// flush32 writes the lower 32 bits to output if the buffer has accumulated >= 32 bits.
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

// flushAlign flushes all remaining bits to output, byte-aligned.
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
