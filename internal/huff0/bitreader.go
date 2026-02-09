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

type bitReaderBytes struct {
	in       []byte
	off      uint
	value    uint64
	bitsRead uint8
}

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

func (b *bitReaderBytes) peekByteFast() uint8 {
	return uint8(b.value >> 56)
}

func (b *bitReaderBytes) advance(n uint8) {
	b.bitsRead += n
	b.value <<= n & 63
}

func (b *bitReaderBytes) fillFast() {
	if b.bitsRead < 32 {
		return
	}
	low := le.Load32(b.in, b.off-4)
	b.value |= uint64(low) << (b.bitsRead - 32)
	b.bitsRead -= 32
	b.off -= 4
}

func (b *bitReaderBytes) fillFastStart() {
	b.value = le.Load64(b.in, b.off-8)
	b.bitsRead = 0
	b.off -= 8
}

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

func (b *bitReaderBytes) finished() bool {
	return b.off == 0 && b.bitsRead >= 64
}

func (b *bitReaderBytes) remaining() uint {
	return b.off*8 + uint(64-b.bitsRead)
}

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

type bitReaderShifted struct {
	in       []byte
	off      uint
	value    uint64
	bitsRead uint8
}

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

func (b *bitReaderShifted) peekBitsFast(n uint8) uint16 {
	return uint16(b.value >> ((64 - n) & 63))
}

func (b *bitReaderShifted) advance(n uint8) {
	b.bitsRead += n
	b.value <<= n & 63
}

func (b *bitReaderShifted) fillFast() {
	if b.bitsRead < 32 {
		return
	}
	low := le.Load32(b.in, b.off-4)
	b.value |= uint64(low) << ((b.bitsRead - 32) & 63)
	b.bitsRead -= 32
	b.off -= 4
}

func (b *bitReaderShifted) fillFastStart() {
	b.value = le.Load64(b.in, b.off-8)
	b.bitsRead = 0
	b.off -= 8
}

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

func (b *bitReaderShifted) remaining() uint {
	return b.off*8 + uint(64-b.bitsRead)
}

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
