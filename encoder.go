// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"fmt"
	"io"
	"math"
	"math/bits"
	"sync"

	"github.com/klauspost/stdgozstd/internal/le"
	"github.com/klauspost/stdgozstd/internal/xxhash"
)

// Compression level constants. These map to the levels accepted by
// [WithEncoderLevel].
const (
	DefaultCompression = -1 // level 3
	NoCompression      = 0  // store blocks without compression
	BestSpeed          = 1  // lowest compression, fastest speed
	BestCompression    = 9  // highest compression, slowest speed
)

// encoderPools caches encoders by category to avoid repeated hash table allocation.
var encoderPools [5]sync.Pool

// putEncoder returns an encoder to its pool for reuse.
func putEncoder(enc encoder) {
	switch enc.(type) {
	case *rawEncoder:
		encoderPools[0].Put(enc)
	case *fastEncoder:
		encoderPools[1].Put(enc)
	case *doubleFastEncoder:
		encoderPools[2].Put(enc)
	case *betterFastEncoder:
		encoderPools[3].Put(enc)
	case *bestFastEncoder:
		encoderPools[4].Put(enc)
	}
}

// encoderCategory maps a compression level to a pool index (0-4).
func encoderCategory(level int) int {
	switch {
	case level == 0:
		return 0
	case level <= 2:
		return 1
	case level <= 4:
		return 2
	case level <= 7:
		return 3
	default:
		return 4
	}
}

// Encoder holds compression configuration and provides one-shot compression
// via [Encoder.AppendCompress]. Configuration is fixed at construction via
// [EncoderOption] values and is immutable afterwards.
type Encoder struct {
	level     int
	blockSize int
	wndSize   int
	crc       bool
	lowMem    bool

	dict *dict
	once sync.Once
}

// ensureInit lazily applies default configuration on first use.
func (e *Encoder) ensureInit() {
	e.once.Do(func() {
		initPredefined()
		e.level = 3
		e.blockSize = maxCompressedBlockSize
		e.wndSize = 4 << 20
		e.crc = true
	})
}

// NewEncoder returns a new Encoder configured by opts. Options are applied in
// order; a later option overrides an earlier one. With no options the Encoder
// uses the default compression level.
func NewEncoder(opts ...EncoderOption) (*Encoder, error) {
	e := &Encoder{}
	e.ensureInit()
	for _, o := range opts {
		if err := o(e); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// EncoderOption configures an [Encoder]. Options are passed to [NewEncoder] and
// [NewWriter].
type EncoderOption func(*Encoder) error

// WithEncoderLevel sets the compression level. Valid values range from
// [NoCompression] (0) to [BestCompression] (9); [DefaultCompression] (-1)
// selects level 3.
func WithEncoderLevel(level int) EncoderOption {
	return func(e *Encoder) error {
		if level == DefaultCompression {
			level = 3
		}
		if level < NoCompression || level > BestCompression {
			return fmt.Errorf("zstd: invalid level %d", level)
		}
		e.level = level
		e.blockSize = maxCompressedBlockSize
		// These mirror zstd default window sizes at levels 1, 3 and 9.
		switch level {
		case 0:
			e.wndSize = 0
		case 1:
			e.wndSize = 2 << 20
		case 2:
			e.wndSize = 3 << 20
		case 3:
			e.wndSize = 4 << 20
		case 4:
			e.wndSize = 4 << 20
		case 5:
			e.wndSize = 4 << 20
		case 6:
			e.wndSize = 5 << 20
		case 7:
			e.wndSize = 6 << 20
		case 8:
			e.wndSize = 7 << 20
		case 9:
			e.wndSize = 8 << 20
		}
		return nil
	}
}

// WithWindowSize overrides the window size for compression.
// This allows limiting memory usage both for compression and decompression.
// Apply it after [WithEncoderLevel] to override the level's default window.
//
// n must be in the range [MinWindowSize, MaxWindowSize].
func WithWindowSize(n int) EncoderOption {
	return func(e *Encoder) error {
		if n < MinWindowSize || n > MaxWindowSize {
			return fmt.Errorf("zstd: window size %d out of range [%d, %d]", n, MinWindowSize, MaxWindowSize)
		}
		e.wndSize = n
		return nil
	}
}

// WithLowMemory controls whether the encoder should trade speed for
// lower memory usage.
func WithLowMemory(b bool) EncoderOption {
	return func(e *Encoder) error {
		e.lowMem = b
		return nil
	}
}

// WithEncoderCRC controls whether a xxHash-64 checksum is appended to each
// frame. The default is true.
func WithEncoderCRC(b bool) EncoderOption {
	return func(e *Encoder) error {
		e.crc = b
		return nil
	}
}

// WithEncoderDict registers a parsed dictionary for compression.
// Passing nil removes any previously configured dictionary.
func WithEncoderDict(d *Dict) EncoderOption {
	return func(e *Encoder) error {
		if d == nil {
			e.dict = nil
			return nil
		}
		e.dict = d.d
		return nil
	}
}

// WithEncoderRawDict registers raw bytes as a dictionary prefix.
// The dictionary must be at least 8 bytes; shorter non-nil values are ignored.
// Passing nil removes any previously configured dictionary.
func WithEncoderRawDict(b []byte) EncoderOption {
	return func(e *Encoder) error {
		if b == nil {
			e.dict = nil
			return nil
		}
		if len(b) < 8 {
			return nil
		}
		e.dict = &dict{content: b}
		return nil
	}
}

// newEncoder creates the appropriate encoder for the current level,
// pulling from a pool when possible to avoid hash table allocation.
func (e *Encoder) newEncoder() encoder {
	maxOff := int32(e.wndSize)
	if maxOff == 0 {
		maxOff = 4 << 20
	}
	bufReset := math.MaxInt32 - maxOff*2
	cat := encoderCategory(e.level)

	if v := encoderPools[cat].Get(); v != nil {
		enc := v.(encoder)
		enc.configure(maxOff, bufReset, e.lowMem)
		return enc
	}

	switch {
	case e.level == 0:
		en := &rawEncoder{}
		en.configure(maxOff, bufReset, e.lowMem)
		return en
	case e.level <= 2:
		en := &fastEncoder{}
		en.configure(maxOff, bufReset, e.lowMem)
		return en
	case e.level <= 4:
		en := &doubleFastEncoder{}
		en.configure(maxOff, bufReset, e.lowMem)
		return en
	case e.level <= 7:
		en := &betterFastEncoder{}
		en.configure(maxOff, bufReset, e.lowMem)
		return en
	default:
		en := &bestFastEncoder{}
		en.configure(maxOff, bufReset, e.lowMem)
		return en
	}
}

// AppendCompress compresses src as a single zstd frame and appends the result to
// dst, returning the extended buffer. It is a one-shot alternative to the
// streaming [Writer] interface.
//
// The Encoder's configured level, dictionary, CRC, and window-size settings apply.
// The returned frame is self-contained: it includes a frame header, one or more
// blocks, and an optional checksum.
//
// Passing nil for dst allocates a new slice. Passing a non-nil dst (e.g. buf[:0])
// lets the caller reuse memory. Multiple calls may be made to concatenate frames:
//
//	var frames []byte
//	frames = e.AppendCompress(frames, part1)
//	frames = e.AppendCompress(frames, part2)
//
// If src is nil or empty, a minimal valid frame is appended to dst.
//
// AppendCompress is safe for concurrent use on the same Encoder.
func (e *Encoder) AppendCompress(dst, src []byte) []byte {
	e.ensureInit()
	enc := e.newEncoder()
	defer putEncoder(enc)
	return e.encodeAll(enc, src, dst)
}

// encodeAll compresses src into a single frame appended to dst.
func (e *Encoder) encodeAll(enc encoder, src, dst []byte) []byte {
	if len(src) == 0 {
		fh := frameHeader{
			ContentSize:   0,
			WindowSize:    MinWindowSize,
			SingleSegment: true,
			Checksum:      e.crc,
			DictID:        e.dict.ID(),
		}
		dst = fh.appendTo(dst)
		var blk blockHeader
		blk.setSize(0)
		blk.setType(blockTypeRaw)
		blk.setLast(true)
		dst = blk.appendTo(dst)
		if e.crc {
			enc.reset(nil, true)
			dst = enc.appendCRC(dst)
		}
		return dst
	}

	single := len(src) <= e.wndSize && len(src) > MinWindowSize
	fh := frameHeader{
		ContentSize:   uint64(len(src)),
		WindowSize:    uint32(enc.windowSize(int64(len(src)))),
		SingleSegment: single,
		Checksum:      e.crc,
		DictID:        e.dict.ID(),
	}

	if len(dst) == 0 && cap(dst) == 0 && len(src) < 1<<20 {
		dst = make([]byte, 0, len(src))
	}
	dst = fh.appendTo(dst)

	if raw, ok := enc.(rawBlockWriter); ok {
		enc.reset(nil, true)
		if e.crc {
			_, _ = enc.checksum().Write(src)
		}
		for len(src) > 0 {
			todo := src
			if len(todo) > maxCompressedBlockSize {
				todo = todo[:maxCompressedBlockSize]
			}
			src = src[len(todo):]
			dst = raw.appendRaw(dst, todo, len(src) == 0)
		}
	} else if len(src) <= e.blockSize {
		enc.reset(e.dict, true)
		if e.crc {
			_, _ = enc.checksum().Write(src)
		}
		blk := enc.block()
		blk.last = true
		if e.dict == nil {
			enc.encodeNoHist(blk, src)
		} else {
			enc.encode(blk, src)
		}

		oldout := blk.output
		blk.output = dst

		err := blk.encode(src, false, true)
		if err != nil {
			panic(err)
		}
		dst = blk.output
		blk.output = oldout
	} else {
		enc.reset(e.dict, false)
		blk := enc.block()
		for len(src) > 0 {
			todo := src
			if len(todo) > e.blockSize {
				todo = todo[:e.blockSize]
			}
			src = src[len(todo):]
			if e.crc {
				_, _ = enc.checksum().Write(todo)
			}
			blk.pushOffsets()
			enc.encode(blk, todo)
			if len(src) == 0 {
				blk.last = true
			}
			err := blk.encode(todo, false, true)
			if err != nil {
				panic(err)
			}
			dst = append(dst, blk.output...)
			blk.reset(nil)
		}
	}
	if e.crc {
		dst = enc.appendCRC(dst)
	}
	return dst
}

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
