// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fse

import (
	"encoding/binary"
	"errors"
	"io"
)

// bitReader reads bits in LSB order from a byte stream consumed back-to-front.
type bitReader struct {
	in       []byte
	off      uint
	value    uint64
	bitsRead uint8
}

// init initializes the reader from a complete bit stream.
func (b *bitReader) init(in []byte) error {
	if len(in) < 1 {
		return errors.New("corrupt stream: too short")
	}
	b.in = in
	b.off = uint(len(in))
	v := in[len(in)-1]
	if v == 0 {
		return errors.New("corrupt stream, did not find end of stream")
	}
	b.bitsRead = 64
	b.value = 0
	if len(in) >= 8 {
		b.fillFastStart()
	} else {
		b.fill()
		b.fill()
	}
	b.bitsRead += 8 - uint8(highBits(uint32(v)))
	return nil
}

// getBits returns n bits, or 0 if no bits remain.
func (b *bitReader) getBits(n uint8) uint16 {
	if n == 0 || b.bitsRead >= 64 {
		return 0
	}
	return b.getBitsFast(n)
}

// getBitsFast returns n bits without bounds checking.
func (b *bitReader) getBitsFast(n uint8) uint16 {
	const regMask = 64 - 1
	v := uint16((b.value << (b.bitsRead & regMask)) >> ((regMask + 1 - n) & regMask))
	b.bitsRead += n
	return v
}

// fillFast refills 32 bits without checking for end of input.
func (b *bitReader) fillFast() {
	if b.bitsRead < 32 {
		return
	}
	v := b.in[b.off-4:]
	v = v[:4]
	low := uint32(v[0]) | uint32(v[1])<<8 | uint32(v[2])<<16 | uint32(v[3])<<24
	b.value = (b.value << 32) | uint64(low)
	b.bitsRead -= 32
	b.off -= 4
}

// fill refills up to 32 bits, handling end-of-input gracefully.
func (b *bitReader) fill() {
	if b.bitsRead < 32 {
		return
	}
	if b.off > 4 {
		v := b.in[b.off-4:]
		v = v[:4]
		low := uint32(v[0]) | uint32(v[1])<<8 | uint32(v[2])<<16 | uint32(v[3])<<24
		b.value = (b.value << 32) | uint64(low)
		b.bitsRead -= 32
		b.off -= 4
		return
	}
	for b.off > 0 {
		b.value = (b.value << 8) | uint64(b.in[b.off-1])
		b.bitsRead -= 8
		b.off--
	}
}

// fillFastStart loads the initial 64 bits when input is at least 8 bytes.
func (b *bitReader) fillFastStart() {
	b.value = binary.LittleEndian.Uint64(b.in[b.off-8:])
	b.bitsRead = 0
	b.off -= 8
}

// finished reports whether all bits have been consumed.
func (b *bitReader) finished() bool {
	return b.bitsRead >= 64 && b.off == 0
}

// close releases resources and returns an error if the stream was corrupted.
func (b *bitReader) close() error {
	b.in = nil
	if b.bitsRead > 64 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
