// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"fmt"
	"io"
	"math/bits"

	"github.com/klauspost/stdgozstd/internal/le"
	"github.com/klauspost/stdgozstd/internal/xxhash"
)

// encoder is the internal interface shared by all compression encoders.
type encoder interface {
	encode(blk *blockEnc, src []byte)
	encodeNoHist(blk *blockEnc, src []byte)
	block() *blockEnc
	checksum() *xxhash.Digest
	appendCRC([]byte) []byte
	windowSize(size int64) int32
	reset(d *dict, singleBlock bool)
	configure(maxMatchOff, bufferReset int32, lowMem bool)
}

// rawBlockWriter is implemented by encoders that emit uncompressed blocks directly.
type rawBlockWriter interface {
	writeRaw(w io.Writer, src []byte, last bool) (int, error)
	appendRaw(dst, src []byte, last bool) []byte
}

// encBase contains shared functionality for encoders.
type encBase struct {
	cur         int32
	maxMatchOff int32
	bufferReset int32
	hist        []byte
	crc         *xxhash.Digest
	tmp         [8]byte
	blk         *blockEnc
	lastDictID  uint32
	lowMem      bool
}

// configure the encoder
func (e *encBase) configure(maxMatchOff, bufferReset int32, lowMem bool) {
	e.maxMatchOff = maxMatchOff
	e.bufferReset = bufferReset
	e.lowMem = lowMem
}

// checksum returns the running xxhash digest for the stream.
func (e *encBase) checksum() *xxhash.Digest {
	return e.crc
}

// appendCRC appends the lower 4 bytes of the xxhash checksum to dst.
func (e *encBase) appendCRC(dst []byte) []byte {
	crc := e.crc.Sum(e.tmp[:0])
	dst = append(dst, crc[7], crc[6], crc[5], crc[4])
	return dst
}

// windowSize returns the effective window size, clamped to the maximum match offset.
func (e *encBase) windowSize(size int64) int32 {
	if size > 0 && size < int64(e.maxMatchOff) {
		b := max(int32(1)<<uint(bits.Len(uint(size))), 1024)
		return b
	}
	return e.maxMatchOff
}

// block returns the encoder's reusable block buffer.
func (e *encBase) block() *blockEnc {
	return e.blk
}

// addBlock appends src to history and returns the start offset.
func (e *encBase) addBlock(src []byte) int32 {
	if len(e.hist)+len(src) > cap(e.hist) {
		if cap(e.hist) == 0 {
			e.ensureHist(len(src))
		} else {
			if cap(e.hist) < int(e.maxMatchOff+maxCompressedBlockSize) {
				panic(fmt.Errorf("unexpected buffer cap %d, want at least %d with window %d", cap(e.hist), e.maxMatchOff+maxCompressedBlockSize, e.maxMatchOff))
			}
			offset := int32(len(e.hist)) - e.maxMatchOff
			copy(e.hist[0:e.maxMatchOff], e.hist[offset:])
			e.cur += offset
			e.hist = e.hist[:e.maxMatchOff]
		}
	}
	s := int32(len(e.hist))
	e.hist = append(e.hist, src...)
	return s
}

// ensureHist ensures history buffer has at least n bytes capacity.
func (e *encBase) ensureHist(n int) {
	if cap(e.hist) >= n {
		return
	}
	l := e.maxMatchOff
	if (e.lowMem && e.maxMatchOff > maxCompressedBlockSize) || e.maxMatchOff <= maxCompressedBlockSize {
		l += maxCompressedBlockSize
	} else {
		l += e.maxMatchOff
	}
	if l < 1<<20 && !e.lowMem {
		l = 1 << 20
	}
	if l < int32(n) {
		l = int32(n)
	}
	e.hist = make([]byte, 0, l)
}

// matchlen returns the match length between positions s and t in src.
func (e *encBase) matchlen(s, t int32, src []byte) int32 {
	return int32(matchLen(src[s:], src[t:]))
}

// resetBase resets the encoder state, optionally loading a dictionary.
func (e *encBase) resetBase(d *dict, singleBlock bool) {
	if e.blk == nil {
		e.blk = &blockEnc{lowMem: e.lowMem}
		e.blk.init()
	} else {
		e.blk.reset(nil)
	}
	e.blk.initNewEncode()
	if e.crc == nil {
		e.crc = xxhash.New()
	} else {
		e.crc.Reset()
	}
	e.blk.dictLitEnc = nil

	// Offset current position so everything will be out of reach.
	// If above reset line, history will be purged.
	if e.cur < e.bufferReset {
		e.cur += e.maxMatchOff + int32(len(e.hist))
	}
	e.hist = e.hist[:0]

	// Ensure buffer fits the current window. Must be after e.cur bump
	// so len(e.hist) still reflects old history for proper invalidation.
	e.ensureHist(int(e.maxMatchOff + maxCompressedBlockSize))
	if d != nil {
		low := e.lowMem
		if singleBlock {
			e.lowMem = true
		}
		e.ensureHist(len(d.content) + maxCompressedBlockSize)
		e.lowMem = low
		for i, off := range d.offsets {
			e.blk.recentOffsets[i] = uint32(off)
			e.blk.prevRecentOffsets[i] = e.blk.recentOffsets[i]
		}
		e.blk.dictLitEnc = d.litEnc
		e.hist = append(e.hist, d.content...)
	}
}

// Hash multiplier primes for various match lengths.
const (
	prime3bytes = 506832829
	prime4bytes = 2654435761
	prime5bytes = 889523592379
	prime6bytes = 227718039650203
	prime7bytes = 58295818150454627
	prime8bytes = 0xcf1bbcdcb7a56463
)

// hashLen hashes the first mls bytes of u into a length-bit hash.
func hashLen(u uint64, length, mls uint8) uint32 {
	switch mls {
	case 3:
		return (uint32(u<<8) * prime3bytes) >> (32 - length)
	case 5:
		return uint32(((u << (64 - 40)) * prime5bytes) >> (64 - length))
	case 6:
		return uint32(((u << (64 - 48)) * prime6bytes) >> (64 - length))
	case 7:
		return uint32(((u << (64 - 56)) * prime7bytes) >> (64 - length))
	case 8:
		return uint32((u * prime8bytes) >> (64 - length))
	default:
		return (uint32(u) * prime4bytes) >> (32 - length)
	}
}

// matchLen returns the number of matching bytes at the start of a and b.
func matchLen(a, b []byte) (n int) {
	left := len(a)
	for left >= 8 {
		diff := le.Load64(a, n) ^ le.Load64(b, n)
		if diff != 0 {
			return n + bits.TrailingZeros64(diff)>>3
		}
		n += 8
		left -= 8
	}
	a = a[n:]
	b = b[n:]

	for i := range a {
		if a[i] != b[i] {
			break
		}
		n++
	}
	return n
}

// rawEncoder implements the encoder interface for level 0 (no compression).
// It writes raw blocks directly, without history buffers or hash tables.
type rawEncoder struct {
	crc         *xxhash.Digest
	tmp         [8]byte
	maxMatchOff int32
}

// encode is a no-op; raw blocks are written directly.
func (e *rawEncoder) encode(*blockEnc, []byte) {}

// encodeNoHist is a no-op; raw blocks are written directly.
func (e *rawEncoder) encodeNoHist(*blockEnc, []byte) {}

// block is unused; rawEncoder bypasses blockEnc.
func (e *rawEncoder) block() *blockEnc { return nil }

// checksum returns the running xxhash digest.
func (e *rawEncoder) checksum() *xxhash.Digest { return e.crc }

// configure stores the maximum match offset for the frame header window size.
func (e *rawEncoder) configure(maxMatchOff, _ int32, _ bool) { e.maxMatchOff = maxMatchOff }

// windowSize returns the window size for the frame header.
func (e *rawEncoder) windowSize(size int64) int32 {
	if size > 0 && size < int64(e.maxMatchOff) {
		return max(int32(1)<<uint(bits.Len(uint(size))), 1024)
	}
	return e.maxMatchOff
}

// appendCRC appends the lower 4 bytes of the xxhash checksum to dst.
func (e *rawEncoder) appendCRC(dst []byte) []byte {
	crc := e.crc.Sum(e.tmp[:0])
	dst = append(dst, crc[7], crc[6], crc[5], crc[4])
	return dst
}

// reset initializes the checksum for a new frame.
// The dict argument is ignored since raw blocks do not use dictionaries.
func (e *rawEncoder) reset(_ *dict, _ bool) {
	if e.crc == nil {
		e.crc = xxhash.New()
	} else {
		e.crc.Reset()
	}
}

// appendRaw appends a raw block (header + data) to dst.
func (e *rawEncoder) appendRaw(dst, src []byte, last bool) []byte {
	var bh blockHeader
	bh.setLast(last)
	bh.setSize(uint32(len(src)))
	bh.setType(blockTypeRaw)
	dst = bh.appendTo(dst)
	return append(dst, src...)
}

// writeRaw writes a raw block (header + data) to w.
func (e *rawEncoder) writeRaw(w io.Writer, src []byte, last bool) (int, error) {
	var bh blockHeader
	bh.setLast(last)
	bh.setSize(uint32(len(src)))
	bh.setType(blockTypeRaw)
	hdr := bh.appendTo(e.tmp[:0])
	n, err := w.Write(hdr)
	if err != nil {
		return n, err
	}
	n2, err := w.Write(src)
	return n + n2, err
}
