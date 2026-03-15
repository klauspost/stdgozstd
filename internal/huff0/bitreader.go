// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package huff0

import (
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/stdgozstd/internal/le"
)

// bitReaderBytes reads Huffman-coded bits from a byte stream in reverse order.
type bitReaderBytes struct {
	in       []byte
	off      uint
	value    uint64
	bitsRead uint8
}

// init prepares the reader from a bitstream, locating the end-of-stream marker.
func (b *bitReaderBytes) init(in []byte) error {
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
	b.advance(8 - uint8(highBit32(uint32(v))))
	return nil
}

// peekByteFast returns the top 8 bits of the buffer without bounds checking.
func (b *bitReaderBytes) peekByteFast() uint8 {
	return uint8(b.value >> 56)
}

// advance consumes n bits from the buffer.
func (b *bitReaderBytes) advance(n uint8) {
	b.bitsRead += n
	b.value <<= n & 63
}

// fillFast loads 32 bits if at least 32 have been consumed. Requires off >= 4.
func (b *bitReaderBytes) fillFast() {
	if b.bitsRead < 32 {
		return
	}
	low := le.Load32(b.in, b.off-4)
	b.value |= uint64(low) << (b.bitsRead - 32)
	b.bitsRead -= 32
	b.off -= 4
}

// fillFastStart loads the initial 64 bits. Requires off >= 8.
func (b *bitReaderBytes) fillFastStart() {
	b.value = le.Load64(b.in, b.off-8)
	b.bitsRead = 0
	b.off -= 8
}

// fill loads bits, handling short reads near the end of the stream.
func (b *bitReaderBytes) fill() {
	if b.bitsRead < 32 {
		return
	}
	if b.off >= 4 {
		low := le.Load32(b.in, b.off-4)
		b.value |= uint64(low) << (b.bitsRead - 32)
		b.bitsRead -= 32
		b.off -= 4
		return
	}
	for b.off > 0 {
		b.value |= uint64(b.in[b.off-1]) << (b.bitsRead - 8)
		b.bitsRead -= 8
		b.off--
	}
}

// finished reports whether all bits have been consumed.
func (b *bitReaderBytes) finished() bool {
	return b.off == 0 && b.bitsRead >= 64
}

// remaining returns the number of unread bits.
func (b *bitReaderBytes) remaining() uint {
	return b.off*8 + uint(64-b.bitsRead)
}

// close verifies the stream was fully consumed and releases resources.
func (b *bitReaderBytes) close() error {
	b.in = nil
	if b.remaining() > 0 {
		return fmt.Errorf("corrupt input: %d bits remain on stream", b.remaining())
	}
	if b.bitsRead > 64 {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// bitReaderShifted reads Huffman-coded bits, returning values pre-shifted to MSB.
type bitReaderShifted struct {
	in       []byte
	off      uint
	value    uint64
	bitsRead uint8
}

// init prepares the reader from a bitstream, locating the end-of-stream marker.
func (b *bitReaderShifted) init(in []byte) error {
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
	b.advance(8 - uint8(highBit32(uint32(v))))
	return nil
}

// peekBitsFast returns the top n bits of the buffer without bounds checking.
func (b *bitReaderShifted) peekBitsFast(n uint8) uint16 {
	return uint16(b.value >> ((64 - n) & 63))
}

// advance consumes n bits from the buffer.
func (b *bitReaderShifted) advance(n uint8) {
	b.bitsRead += n
	b.value <<= n & 63
}

// fillFast loads 32 bits if at least 32 have been consumed. Requires off >= 4.
func (b *bitReaderShifted) fillFast() {
	if b.bitsRead < 32 {
		return
	}
	low := le.Load32(b.in, b.off-4)
	b.value |= uint64(low) << ((b.bitsRead - 32) & 63)
	b.bitsRead -= 32
	b.off -= 4
}

// fillFastStart loads the initial 64 bits. Requires off >= 8.
func (b *bitReaderShifted) fillFastStart() {
	b.value = le.Load64(b.in, b.off-8)
	b.bitsRead = 0
	b.off -= 8
}

// fill loads bits, handling short reads near the end of the stream.
func (b *bitReaderShifted) fill() {
	if b.bitsRead < 32 {
		return
	}
	if b.off > 4 {
		low := le.Load32(b.in, b.off-4)
		b.value |= uint64(low) << ((b.bitsRead - 32) & 63)
		b.bitsRead -= 32
		b.off -= 4
		return
	}
	for b.off > 0 {
		b.value |= uint64(b.in[b.off-1]) << ((b.bitsRead - 8) & 63)
		b.bitsRead -= 8
		b.off--
	}
}

// remaining returns the number of unread bits.
func (b *bitReaderShifted) remaining() uint {
	return b.off*8 + uint(64-b.bitsRead)
}

// close verifies the stream was fully consumed and releases resources.
func (b *bitReaderShifted) close() error {
	b.in = nil
	if b.remaining() > 0 {
		return fmt.Errorf("corrupt input: %d bits remain on stream", b.remaining())
	}
	if b.bitsRead > 64 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
