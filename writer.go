// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"fmt"
	"io"
	"math"
	"sync"
)

// Compression level constants. These map to the levels accepted by
// [Writer.SetLevel].
const (
	DefaultCompression = -1 // level 3
	NoCompression      = 0  // store blocks without compression
	BestSpeed          = 1  // lowest compression, fastest speed
	BestCompression    = 9  // highest compression, slowest speed
)

var encoderPools [4]sync.Pool

func putEncoder(enc encoder) {
	switch enc.(type) {
	case *fastEncoder:
		encoderPools[0].Put(enc)
	case *doubleFastEncoder:
		encoderPools[1].Put(enc)
	case *betterFastEncoder:
		encoderPools[2].Put(enc)
	case *bestFastEncoder:
		encoderPools[3].Put(enc)
	}
}

func encoderCategory(level int) int {
	switch {
	case level <= 2:
		return 0
	case level <= 4:
		return 1
	case level <= 7:
		return 2
	default:
		return 3
	}
}

// Writer compresses data written to it as a zstd stream.
// Writes are buffered internally; callers must call [Writer.Close] to
// flush any remaining data and write the frame trailer.
type Writer struct {
	w   io.Writer
	enc encoder

	filling []byte

	blockSize int
	wndSize   int
	level     int
	crc       bool

	headerWritten    bool
	eofWritten       bool
	fullFrameWritten bool
	err              error
	nWritten         int64
	nInput           int64
	contentSize      int64
	lowMem           bool

	dict        *dict
	initialized bool
}

func (w *Writer) ensureInit() {
	if w.initialized {
		return
	}
	w.initialized = true
	initPredefined()
	w.level = 3
	w.blockSize = maxCompressedBlockSize
	w.wndSize = 4 << 20
	w.crc = true
}

// NewWriter returns a new Writer compressing data at the default level.
// If w is nil the Writer may only be used with [Writer.AppendTo];
// call [Writer.Reset] before streaming.
func NewWriter(w io.Writer) *Writer {
	wr := &Writer{}
	wr.ensureInit()
	if w != nil {
		wr.Reset(w)
	}
	return wr
}

// SetLevel sets the compression level. Valid values range from
// [NoCompression] (0) to [BestCompression] (9); [DefaultCompression] (-1)
// selects level 3. SetLevel must be called before [Writer.Reset] or
// [Writer.Write] to take effect.
func (w *Writer) SetLevel(level int) error {
	w.ensureInit()
	if level == DefaultCompression {
		level = 3
	}
	if level < NoCompression || level > BestCompression {
		return fmt.Errorf("zstd: invalid level %d", level)
	}
	w.level = level
	switch level {
	case 0:
		w.wndSize = 0
		w.blockSize = maxCompressedBlockSize
	case 1:
		w.wndSize = 4 << 20
		w.blockSize = 1 << 16
	case 2:
		w.wndSize = 8 << 20
		w.blockSize = 1 << 16
	case 3:
		w.wndSize = 4 << 20
		w.blockSize = maxCompressedBlockSize
	case 4:
		w.wndSize = 8 << 20
		w.blockSize = maxCompressedBlockSize
	case 5, 6:
		w.wndSize = 8 << 20
		w.blockSize = maxCompressedBlockSize
	case 7:
		w.wndSize = 8 << 20
		w.blockSize = maxCompressedBlockSize
	case 8:
		w.wndSize = 8 << 20
		w.blockSize = maxCompressedBlockSize
	case 9:
		w.wndSize = 8 << 20
		w.blockSize = maxCompressedBlockSize
	}
	return nil
}

// SetWindowSize overrides the window size for compression.
// n must be in the range [MinWindowSize, MaxWindowSize].
func (w *Writer) SetWindowSize(n int) error {
	w.ensureInit()
	if n < MinWindowSize || n > MaxWindowSize {
		return fmt.Errorf("zstd: window size %d out of range [%d, %d]", n, MinWindowSize, MaxWindowSize)
	}
	w.wndSize = n
	return nil
}

// SetLowMemory controls whether the encoder should trade speed for
// lower memory usage.
func (w *Writer) SetLowMemory(b bool) {
	w.ensureInit()
	w.lowMem = b
}

// SetCRC controls whether the writer appends a xxHash-64 checksum to each
// frame. The default is true.
func (w *Writer) SetCRC(b bool) {
	w.ensureInit()
	w.crc = b
}

// AddDict registers a parsed dictionary for compression.
func (w *Writer) AddDict(d *Dict) {
	w.ensureInit()
	if d == nil {
		w.dict = nil
		return
	}
	w.dict = d.d
}

// SetRawDict registers raw bytes as a dictionary prefix.
func (w *Writer) SetRawDict(b []byte) {
	w.ensureInit()
	if len(b) == 0 {
		w.dict = nil
		return
	}
	w.dict = &dict{content: b}
}

// ResetContentSize resets the Writer for a new stream to wr and records
// the uncompressed content size in the frame header. If size is negative
// the content size is omitted.
func (w *Writer) ResetContentSize(wr io.Writer, size int64) {
	w.ensureInit()
	w.Reset(wr)
	if size >= 0 {
		w.contentSize = size
	}
}

// Reset discards the Writer's state and prepares it to write a new frame
// to wr. Configuration (level, window size, CRC, dictionary) is preserved.
func (w *Writer) Reset(wr io.Writer) {
	w.ensureInit()
	if cap(w.filling) == 0 {
		w.filling = make([]byte, 0, w.blockSize)
	}
	w.filling = w.filling[:0]

	if w.enc != nil {
		putEncoder(w.enc)
	}
	w.enc = w.newEncoder()
	w.enc.reset(w.dict, false)

	w.w = wr
	w.headerWritten = false
	w.eofWritten = false
	w.fullFrameWritten = false
	w.err = nil
	w.nWritten = 0
	w.nInput = 0
	w.contentSize = 0
}

// newEncoder creates the appropriate encoder for the current level,
// pulling from a pool when possible to avoid hash table allocation.
func (w *Writer) newEncoder() encoder {
	maxOff := int32(w.wndSize)
	if maxOff == 0 {
		maxOff = 4 << 20
	}
	bufReset := math.MaxInt32 - maxOff*2
	cat := encoderCategory(w.level)

	if v := encoderPools[cat].Get(); v != nil {
		enc := v.(encoder)
		enc.configure(maxOff, bufReset, w.lowMem)
		return enc
	}

	switch {
	case w.level <= 2:
		e := &fastEncoder{}
		e.configure(maxOff, bufReset, w.lowMem)
		return e
	case w.level <= 4:
		e := &doubleFastEncoder{}
		e.configure(maxOff, bufReset, w.lowMem)
		return e
	case w.level <= 7:
		e := &betterFastEncoder{}
		e.configure(maxOff, bufReset, w.lowMem)
		return e
	default:
		e := &bestFastEncoder{}
		e.configure(maxOff, bufReset, w.lowMem)
		return e
	}
}

// Write compresses p and writes it to the underlying writer.
// The compressed bytes are not necessarily flushed until [Writer.Close]
// or [Writer.Flush] is called.
func (w *Writer) Write(p []byte) (n int, err error) {
	w.ensureInit()
	if w.eofWritten {
		return 0, ErrEncoderClosed
	}
	for len(p) > 0 {
		if len(p)+len(w.filling) < w.blockSize {
			if w.crc {
				_, _ = w.enc.checksum().Write(p)
			}
			w.filling = append(w.filling, p...)
			return n + len(p), nil
		}
		add := p
		if len(p)+len(w.filling) > w.blockSize {
			add = add[:w.blockSize-len(w.filling)]
		}
		if w.crc {
			_, _ = w.enc.checksum().Write(add)
		}
		w.filling = append(w.filling, add...)
		p = p[len(add):]
		n += len(add)
		if len(w.filling) < w.blockSize {
			return n, nil
		}
		if err := w.nextBlock(false); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ReadFrom reads data from r until io.EOF and compresses it to the
// underlying writer. It implements [io.ReaderFrom].
func (w *Writer) ReadFrom(r io.Reader) (n int64, err error) {
	w.ensureInit()
	if len(w.filling) > 0 {
		if err := w.nextBlock(false); err != nil {
			return 0, err
		}
	}
	w.filling = w.filling[:w.blockSize]
	src := w.filling
	for {
		n2, err := r.Read(src)
		if w.crc {
			_, _ = w.enc.checksum().Write(src[:n2])
		}
		src = src[n2:]
		n += int64(n2)
		switch err {
		case io.EOF:
			w.filling = w.filling[:len(w.filling)-len(src)]
			return n, nil
		case nil:
		default:
			w.err = err
			return n, err
		}
		if len(src) > 0 {
			continue
		}
		if err = w.nextBlock(false); err != nil {
			return n, err
		}
		w.filling = w.filling[:w.blockSize]
		src = w.filling
	}
}

// Flush writes any buffered data to the underlying writer as a compressed
// block. It does not write the frame trailer; use [Writer.Close] to
// finalize the frame.
func (w *Writer) Flush() error {
	w.ensureInit()
	if len(w.filling) > 0 {
		if err := w.nextBlock(false); err != nil {
			return err
		}
	}
	return w.err
}

// Close flushes any remaining data, writes the frame trailer (and optional
// checksum), and releases encoder resources. After Close, the Writer must
// be [Writer.Reset] before it can be used again.
func (w *Writer) Close() error {
	w.ensureInit()
	if w.enc == nil {
		return nil
	}
	err := w.nextBlock(true)
	if err != nil {
		return err
	}
	if w.contentSize > 0 && w.nInput != w.contentSize {
		return fmt.Errorf("frame content size %d given, but %d bytes was written", w.contentSize, w.nInput)
	}
	if w.fullFrameWritten {
		return w.err
	}

	if w.err != nil {
		return w.err
	}

	if w.crc {
		var tmp [4]byte
		_, w.err = w.w.Write(w.enc.appendCRC(tmp[:0]))
		w.nWritten += 4
	}
	if w.err == nil {
		w.err = ErrEncoderClosed
		return nil
	}
	return w.err
}

// AppendCompress compresses src as a single zstd frame and appends the result to dst,
// returning the extended buffer. It is a one-shot alternative to the streaming
// Write/Close interface.
//
// The Writer's current level, dictionary, CRC, and window-size settings apply.
// The returned frame is self-contained: it includes a frame header, one or more
// blocks, and an optional checksum.
//
// Passing nil for dst allocates a new slice. Passing a non-nil dst (e.g. buf[:0])
// lets the caller reuse memory. Multiple calls may be made to concatenate frames:
//
//	var frames []byte
//	frames = w.AppendCompress(part1, frames)
//	frames = w.AppendCompress(part2, frames)
//
// If src is nil or empty, AppendTo returns dst unchanged.
//
// AppendTo must not be called concurrently with other Writer methods, but
// successive calls on the same Writer are safe without Reset.
func (w *Writer) AppendCompress(dst, src []byte) []byte {
	w.ensureInit()
	cat := encoderCategory(w.level)
	if w.enc != nil {
		var encCat int
		switch w.enc.(type) {
		case *fastEncoder:
			encCat = 0
		case *doubleFastEncoder:
			encCat = 1
		case *betterFastEncoder:
			encCat = 2
		case *bestFastEncoder:
			encCat = 3
		}
		if encCat != cat {
			putEncoder(w.enc)
			w.enc = nil
		}
	}
	if w.enc == nil {
		w.enc = w.newEncoder()
	}
	return w.encodeAll(w.enc, src, dst)
}

// nextBlock encodes the current filling buffer as a block.
func (w *Writer) nextBlock(final bool) error {
	if w.err != nil {
		return w.err
	}
	if len(w.filling) > w.blockSize {
		return fmt.Errorf("block > maxStoreBlockSize")
	}
	if !w.headerWritten {
		if final && len(w.filling) == 0 {
			w.headerWritten = true
			w.fullFrameWritten = true
			w.eofWritten = true
			return nil
		}
		if final && len(w.filling) > 0 {
			var current []byte
			current = w.encodeAll(w.enc, w.filling, current)
			var n2 int
			n2, w.err = w.w.Write(current)
			if w.err != nil {
				return w.err
			}
			w.nWritten += int64(n2)
			w.nInput += int64(len(w.filling))
			w.filling = w.filling[:0]
			w.headerWritten = true
			w.fullFrameWritten = true
			w.eofWritten = true
			return nil
		}

		var tmp [maxHeaderSize]byte
		fh := frameHeader{
			ContentSize:   uint64(w.contentSize),
			WindowSize:    uint32(w.enc.windowSize(w.contentSize)),
			SingleSegment: false,
			Checksum:      w.crc,
			DictID:        w.dict.ID(),
		}
		dst := fh.appendTo(tmp[:0])
		w.headerWritten = true
		var n2 int
		n2, w.err = w.w.Write(dst)
		if w.err != nil {
			return w.err
		}
		w.nWritten += int64(n2)
	}

	if w.eofWritten {
		final = false
	}

	if len(w.filling) == 0 {
		if final {
			enc := w.enc
			blk := enc.block()
			blk.reset(nil)
			blk.last = true
			blk.encodeRaw(nil)
			_, w.err = w.w.Write(blk.output)
			w.nWritten += int64(len(blk.output))
			w.eofWritten = true
		}
		return w.err
	}

	src := w.filling
	w.nInput += int64(len(src))

	enc := w.enc
	blk := enc.block()
	blk.reset(nil)
	enc.encode(blk, src)
	blk.last = final
	if final {
		w.eofWritten = true
	}

	w.err = blk.encode(src, false, true)
	if w.err != nil {
		return w.err
	}
	_, w.err = w.w.Write(blk.output)
	w.nWritten += int64(len(blk.output))
	w.filling = w.filling[:0]
	return w.err
}

// encodeAll compresses src into a single frame appended to dst.
func (w *Writer) encodeAll(enc encoder, src, dst []byte) []byte {
	if len(src) == 0 {
		return dst
	}

	single := len(src) <= w.wndSize && len(src) > MinWindowSize
	fh := frameHeader{
		ContentSize:   uint64(len(src)),
		WindowSize:    uint32(enc.windowSize(int64(len(src)))),
		SingleSegment: single,
		Checksum:      w.crc,
		DictID:        w.dict.ID(),
	}

	if len(dst) == 0 && cap(dst) == 0 && len(src) < 1<<20 {
		dst = make([]byte, 0, len(src))
	}
	dst = fh.appendTo(dst)

	if len(src) <= w.blockSize {
		enc.reset(w.dict, true)
		if w.crc {
			_, _ = enc.checksum().Write(src)
		}
		blk := enc.block()
		blk.last = true
		if w.dict == nil {
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
		enc.reset(w.dict, false)
		blk := enc.block()
		for len(src) > 0 {
			todo := src
			if len(todo) > w.blockSize {
				todo = todo[:w.blockSize]
			}
			src = src[len(todo):]
			if w.crc {
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
	if w.crc {
		dst = enc.appendCRC(dst)
	}
	return dst
}
