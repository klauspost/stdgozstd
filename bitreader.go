// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "github.com/klauspost/stdgozstd/internal/le"

// bitReader reads bits from a reverse bitstream (LSB-first, read from end).
type bitReader struct {
	in       []byte
	value    uint64
	cursor   int
	bitsRead uint8
}

// init initializes the reader from a reverse bitstream.
func (b *bitReader) init(in []byte) error {
	if len(in) < 1 {
		return corruptedError("bitstream too short")
	}
	b.in = in
	v := in[len(in)-1]
	if v == 0 {
		return corruptedError("bitstream missing end marker")
	}
	b.cursor = len(in)
	b.bitsRead = 64
	b.value = 0
	if len(in) >= 8 {
		b.fillFastStart()
	} else {
		b.fill()
		b.fill()
	}
	b.bitsRead += 8 - uint8(highBit(uint32(v)))
	return nil
}

// getBits returns n bits as int.
func (b *bitReader) getBits(n uint8) int {
	if n == 0 {
		return 0
	}
	return int(b.get32BitsFast(n))
}

// get32BitsFast returns n bits as uint32 without bounds checking.
func (b *bitReader) get32BitsFast(n uint8) uint32 {
	const regMask = 64 - 1
	v := uint32((b.value << (b.bitsRead & regMask)) >> ((regMask + 1 - n) & regMask))
	b.bitsRead += n
	return v
}

// fillFast refills at least 32 bits from input.
func (b *bitReader) fillFast() {
	if b.bitsRead < 32 {
		return
	}
	b.cursor -= 4
	b.value = (b.value << 32) | uint64(le.Load32(b.in, b.cursor))
	b.bitsRead -= 32
}

// fillFastStart loads the first 8 bytes into the bit buffer.
func (b *bitReader) fillFastStart() {
	b.cursor -= 8
	b.value = le.Load64(b.in, b.cursor)
	b.bitsRead = 0
}

// fill refills the bit buffer, handling short inputs.
func (b *bitReader) fill() {
	if b.bitsRead < 32 {
		return
	}
	if b.cursor >= 4 {
		b.cursor -= 4
		b.value = (b.value << 32) | uint64(le.Load32(b.in, b.cursor))
		b.bitsRead -= 32
		return
	}

	b.bitsRead -= uint8(8 * b.cursor)
	for b.cursor > 0 {
		b.cursor--
		b.value = (b.value << 8) | uint64(b.in[b.cursor])
	}
}

// finished reports whether all bits have been consumed.
func (b *bitReader) finished() bool {
	return b.cursor == 0 && b.bitsRead >= 64
}

// overread reports whether more bits were read than available.
func (b *bitReader) overread() bool {
	return b.bitsRead > 64
}

// remain returns the number of unread bits.
func (b *bitReader) remain() int {
	return 8*b.cursor + 64 - int(b.bitsRead)
}

// close verifies the stream was fully consumed.
func (b *bitReader) close() error {
	b.in = nil
	b.cursor = 0
	if !b.finished() {
		return corruptedErrorf("%d extra bits on block", b.remain())
	}
	return nil
}
