// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"encoding/binary"
	"io"
	"math"
	"sync"

	"github.com/klauspost/stdgozstd/internal/xxhash"
)

// frameDecPool holds frame decoders for reuse.
var frameDecPool sync.Pool

// decoderOptions holds configuration for the frame decoder.
type decoderOptions struct {
	maxWindowSize  int
	maxDecodedSize int64 // 0 = unlimited
	lowMem         bool
}

// Limits for the decoder window size, as defined by the zstd specification.
const (
	MinWindowSize = 1 << 10 // 1 KiB
	MaxWindowSize = 1 << 29 // 512 MiB
)

// Frame magic bytes as defined in the zstd specification.
const (
	frameMagic          = "\x28\xb5\x2f\xfd"
	skippableFrameMagic = "\x2a\x4d\x18"
)

// frameDec decodes a single zstd frame.
type frameDec struct {
	o   decoderOptions
	crc *xxhash.Digest

	WindowSize uint64

	history history

	rawInput byteBuffer

	bBuf byteBuf

	FrameContentSize uint64

	DictionaryID  uint32
	HasCheckSum   bool
	SingleSegment bool
}

// newFrameDec creates a frame decoder with the given options.
func newFrameDec(o decoderOptions) *frameDec {
	return &frameDec{o: o}
}

// reset parses the frame header from br, preparing for block decoding.
func (d *frameDec) reset(br byteBuffer) error {
	d.HasCheckSum = false
	d.WindowSize = 0
	var signature [4]byte
	for {
		b, err := br.readSmall(1)
		switch err {
		case io.EOF, io.ErrUnexpectedEOF:
			return io.EOF
		case nil:
			signature[0] = b[0]
		default:
			return err
		}
		b, err = br.readSmall(3)
		switch err {
		case io.EOF:
			return io.EOF
		case nil:
			copy(signature[1:], b)
		default:
			return &ErrCorrupted{msg: "reading frame header", err: err}
		}

		if string(signature[1:4]) != skippableFrameMagic || signature[0]&0xf0 != 0x50 {
			break
		}
		// Skippable frame — read size and skip
		b, err = br.readSmall(4)
		if err != nil {
			return &ErrCorrupted{msg: "reading frame header", err: err}
		}
		n := uint32(b[0]) | (uint32(b[1]) << 8) | (uint32(b[2]) << 16) | (uint32(b[3]) << 24)
		if err := br.skipN(int64(n)); err != nil {
			return &ErrCorrupted{msg: "reading frame header", err: err}
		}
	}

	if string(signature[:]) != frameMagic {
		return errMagicMismatch
	}

	fhd, err := br.readByte()
	if err != nil {
		return &ErrCorrupted{msg: "reading frame header", err: err}
	}
	d.SingleSegment = fhd&(1<<5) != 0

	if fhd&(1<<3) != 0 {
		return corruptedError("reserved bit set on frame header")
	}

	d.WindowSize = 0
	if !d.SingleSegment {
		wd, err := br.readByte()
		if err != nil {
			return &ErrCorrupted{msg: "reading frame header", err: err}
		}
		windowLog := 10 + (wd >> 3)
		windowBase := uint64(1) << windowLog
		windowAdd := (windowBase / 8) * uint64(wd&0x7)
		d.WindowSize = windowBase + windowAdd
	}

	d.DictionaryID = 0
	if size := fhd & 3; size != 0 {
		if size == 3 {
			size = 4
		}
		b, err := br.readSmall(int(size))
		if err != nil {
			return &ErrCorrupted{msg: "reading frame header", err: err}
		}
		switch len(b) {
		case 1:
			d.DictionaryID = uint32(b[0])
		case 2:
			d.DictionaryID = uint32(b[0]) | (uint32(b[1]) << 8)
		case 4:
			d.DictionaryID = uint32(b[0]) | (uint32(b[1]) << 8) | (uint32(b[2]) << 16) | (uint32(b[3]) << 24)
		}
	}

	var fcsSize int
	v := fhd >> 6
	switch v {
	case 0:
		if d.SingleSegment {
			fcsSize = 1
		}
	default:
		fcsSize = 1 << v
	}
	d.FrameContentSize = fcsUnknown
	if fcsSize > 0 {
		b, err := br.readSmall(fcsSize)
		if err != nil {
			return &ErrCorrupted{msg: "reading frame header", err: err}
		}
		switch len(b) {
		case 1:
			d.FrameContentSize = uint64(b[0])
		case 2:
			d.FrameContentSize = uint64(b[0]) | (uint64(b[1]) << 8) + 256
		case 4:
			d.FrameContentSize = uint64(b[0]) | (uint64(b[1]) << 8) | (uint64(b[2]) << 16) | (uint64(b[3]) << 24)
		case 8:
			d1 := uint32(b[0]) | (uint32(b[1]) << 8) | (uint32(b[2]) << 16) | (uint32(b[3]) << 24)
			d2 := uint32(b[4]) | (uint32(b[5]) << 8) | (uint32(b[6]) << 16) | (uint32(b[7]) << 24)
			d.FrameContentSize = uint64(d1) | (uint64(d2) << 32)
		}
	}

	// Reject a frame whose declared content size alone exceeds the limit,
	// before allocating buffers or decoding any block.
	if d.o.maxDecodedSize > 0 && d.FrameContentSize != fcsUnknown && d.FrameContentSize > uint64(d.o.maxDecodedSize) {
		// Clamp to avoid int64 overflow when FrameContentSize exceeds MaxInt64.
		return &ErrDecodedSizeExceeded{Allowed: d.o.maxDecodedSize, Produced: int64(min(d.FrameContentSize, math.MaxInt64))}
	}

	d.HasCheckSum = fhd&(1<<2) != 0
	if d.HasCheckSum {
		if d.crc == nil {
			d.crc = xxhash.New()
		}
		d.crc.Reset()
	}

	if d.WindowSize > uint64(d.o.maxWindowSize) {
		return &ErrWindowSizeExceeded{Allowed: uint64(d.o.maxWindowSize), Requested: d.WindowSize}
	}

	if d.WindowSize == 0 && d.SingleSegment {
		d.WindowSize = max(d.FrameContentSize, MinWindowSize)
		if d.WindowSize > uint64(d.o.maxWindowSize) {
			return &ErrWindowSizeExceeded{Allowed: uint64(d.o.maxWindowSize), Requested: d.WindowSize}
		}
	}

	if d.WindowSize < MinWindowSize {
		return errWindowSizeTooSmall
	}

	d.history.windowSize = int(d.WindowSize)
	if !d.o.lowMem || d.history.windowSize < maxBlockSize {
		d.history.allocFrameBuffer = d.history.windowSize * 2
	} else {
		d.history.allocFrameBuffer = d.history.windowSize + maxBlockSize/2
	}

	d.rawInput = br
	return nil
}

// next reads the next block header and data from the stream.
func (d *frameDec) next(block *blockDec) error {
	return block.reset(d.rawInput, d.WindowSize)
}

// checkCRC reads and verifies the frame checksum.
func (d *frameDec) checkCRC() error {
	buf, err := d.rawInput.readSmall(4)
	if err != nil {
		return &ErrCorrupted{msg: "reading checksum", err: err}
	}
	want := binary.LittleEndian.Uint32(buf[:4])
	got := uint32(d.crc.Sum64())
	if got != want {
		return errCRCMismatch
	}
	return nil
}

// maxDirectDecodeSize is the threshold above which runDecoder falls back to streaming.
const maxDirectDecodeSize = 1 << 20 // 1 MiB

// runDecoder decodes all blocks into dst (used for DecodeAll-style calls).
// Falls back to streaming if content exceeds maxDirectDecodeSize.
//
// maxTotal (when > 0) caps the cumulative output measured from dstStart, the
// length of dst at the start of the enclosing decode call; this bounds output
// across concatenated frames even when a frame's content size is absent or lies.
func (d *frameDec) runDecoder(dst []byte, dec *blockDec, maxTotal int64, dstStart int) ([]byte, error) {
	saved := d.history.b

	d.history.b = dst
	d.history.ignoreBuffer = len(dst)
	crcStart := len(dst)

	if d.FrameContentSize != fcsUnknown && d.FrameContentSize <= maxDirectDecodeSize {
		if uint64(cap(dst)) < d.FrameContentSize+uint64(len(dst)) {
			dst2 := make([]byte, len(dst), uint64(len(dst))+d.FrameContentSize+compressedBlockOverAlloc)
			copy(dst2, dst)
			dst = dst2
			d.history.b = dst
		}
	}

	var err error
	for {
		err = dec.reset(d.rawInput, d.WindowSize)
		if err != nil {
			break
		}
		err = dec.decodeBuf(&d.history)
		if err != nil {
			break
		}
		if maxTotal > 0 && int64(len(d.history.b)-dstStart) > maxTotal {
			err = &ErrDecodedSizeExceeded{Allowed: maxTotal, Produced: int64(len(d.history.b) - dstStart)}
			break
		}
		if dec.Last {
			break
		}
	}
	dst = d.history.b
	if err == nil {
		if d.FrameContentSize != fcsUnknown && uint64(len(dst)-crcStart) != d.FrameContentSize {
			err = errFrameSizeMismatch
		} else if d.HasCheckSum {
			_, _ = d.crc.Write(dst[crcStart:])
			err = d.checkCRC()
		}
	}
	d.history.b = saved
	return dst, err
}
