// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "math/bits"

type seqCoders struct {
	llEnc, ofEnc, mlEnc    *fseEncoder
	llPrev, ofPrev, mlPrev *fseEncoder
}

// setPrev updates previous encoders for table reuse detection.
func (s *seqCoders) setPrev(ll, ml, of *fseEncoder) {
	compareSwap := func(used *fseEncoder, current, prev **fseEncoder) {
		if *current == used {
			*prev, *current = *current, *prev
			c := *current
			p := *prev
			c.reUsed = false
			p.reUsed = true
			return
		}
		if used == *prev {
			return
		}
		prevEnc := *prev
		prevEnc.symbolLen = 0
	}
	compareSwap(ll, &s.llEnc, &s.llPrev)
	compareSwap(ml, &s.mlEnc, &s.mlPrev)
	compareSwap(of, &s.ofEnc, &s.ofPrev)
}

// highBit returns the position of the highest set bit (0-based).
func highBit(val uint32) (n uint32) {
	return uint32(bits.Len32(val) - 1)
}

var llCodeTable = [64]byte{0, 1, 2, 3, 4, 5, 6, 7,
	8, 9, 10, 11, 12, 13, 14, 15,
	16, 16, 17, 17, 18, 18, 19, 19,
	20, 20, 20, 20, 21, 21, 21, 21,
	22, 22, 22, 22, 22, 22, 22, 22,
	23, 23, 23, 23, 23, 23, 23, 23,
	24, 24, 24, 24, 24, 24, 24, 24,
	24, 24, 24, 24, 24, 24, 24, 24}

const maxLLCode = 35

var llBitsTable = [maxLLCode + 1]byte{
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	1, 1, 1, 1, 2, 2, 3, 3,
	4, 6, 7, 8, 9, 10, 11, 12,
	13, 14, 15, 16}

// llCode returns the literal length FSE code for the given length.
func llCode(litLength uint32) uint8 {
	const llDeltaCode = 19
	if litLength <= 63 {
		return llCodeTable[litLength&63]
	}
	return uint8(highBit(litLength)) + llDeltaCode
}

var mlCodeTable = [128]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
	32, 32, 33, 33, 34, 34, 35, 35, 36, 36, 36, 36, 37, 37, 37, 37,
	38, 38, 38, 38, 38, 38, 38, 38, 39, 39, 39, 39, 39, 39, 39, 39,
	40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40, 40,
	41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41,
	42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42,
	42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42}

const maxMLCode = 52

var mlBitsTable = [maxMLCode + 1]byte{
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	1, 1, 1, 1, 2, 2, 3, 3,
	4, 4, 5, 7, 8, 9, 10, 11,
	12, 13, 14, 15, 16}

// mlCode returns the match length FSE code for the given length.
func mlCode(mlBase uint32) uint8 {
	const mlDeltaCode = 36
	if mlBase <= 127 {
		return mlCodeTable[mlBase&127]
	}
	return uint8(highBit(mlBase)) + mlDeltaCode
}

// ofCode returns the offset FSE code for the given offset.
func ofCode(offset uint32) uint8 {
	return uint8(bits.Len32(offset) - 1)
}
