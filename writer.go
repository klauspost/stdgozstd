// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"fmt"
	"io"
)

var _ io.ReaderFrom = (*Writer)(nil)

// Writer compresses data written to it as a zstd stream.
// Writes are buffered internally; callers must call [Writer.Close] to
// flush any remaining data and write the frame trailer.
type Writer struct {
	cfg *Encoder
	w   io.Writer
	enc encoder

	filling []byte

	headerWritten    bool
	eofWritten       bool
	fullFrameWritten bool
	err              error
	nWritten         int64
	nInput           int64
	contentSize      int64
}

// ensureInit lazily initializes the Writer on first use.
func (w *Writer) ensureInit() {
	if w.cfg == nil {
		w.cfg = &Encoder{}
	}
	w.cfg.ensureInit()
}

// NewWriter returns a new Writer that compresses data written to w using the
// configuration built from opts. With no options, default configuration is used.
// If w is nil the Writer must be pointed at a destination with [Writer.Reset]
// before streaming.
func NewWriter(w io.Writer, opts ...EncoderOption) (*Writer, error) {
	e, err := NewEncoder(opts...)
	if err != nil {
		return nil, err
	}
	wr := &Writer{cfg: e}
	if w != nil {
		if err := wr.Reset(w); err != nil {
			return nil, err
		}
	}
	return wr, nil
}

// Reset discards the Writer's state and prepares it to write a new frame to wr.
// Options in opts are applied to the configuration first; any option may be
// changed. With no options the current configuration is preserved.
func (w *Writer) Reset(wr io.Writer, opts ...EncoderOption) error {
	w.ensureInit()
	for _, o := range opts {
		if err := o(w.cfg); err != nil {
			return err
		}
	}
	if cap(w.filling) == 0 {
		w.filling = make([]byte, 0, w.cfg.blockSize)
	}
	w.filling = w.filling[:0]

	if w.enc != nil {
		putEncoder(w.enc)
	}
	w.enc = w.cfg.newEncoder()
	w.enc.reset(w.cfg.dict, false)

	w.w = wr
	w.headerWritten = false
	w.eofWritten = false
	w.fullFrameWritten = false
	w.err = nil
	w.nWritten = 0
	w.nInput = 0
	w.contentSize = w.cfg.contentSize
	return nil
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
		if len(p)+len(w.filling) < w.cfg.blockSize {
			if w.cfg.crc {
				_, _ = w.enc.checksum().Write(p)
			}
			w.filling = append(w.filling, p...)
			return n + len(p), nil
		}
		add := p
		if len(p)+len(w.filling) > w.cfg.blockSize {
			add = add[:w.cfg.blockSize-len(w.filling)]
		}
		if w.cfg.crc {
			_, _ = w.enc.checksum().Write(add)
		}
		w.filling = append(w.filling, add...)
		p = p[len(add):]
		n += len(add)
		if len(w.filling) < w.cfg.blockSize {
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
	w.filling = w.filling[:w.cfg.blockSize]
	src := w.filling
	for {
		n2, err := r.Read(src)
		if w.cfg.crc {
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
		w.filling = w.filling[:w.cfg.blockSize]
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
// be reset with [Writer.Reset] before it can be used again.
func (w *Writer) Close() error {
	w.ensureInit()
	if w.enc == nil {
		return nil
	}
	defer func() {
		putEncoder(w.enc)
		w.enc = nil
	}()
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

	if w.cfg.crc {
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

// nextBlock encodes the current filling buffer as a block.
func (w *Writer) nextBlock(final bool) error {
	if w.err != nil {
		return w.err
	}
	if len(w.filling) > w.cfg.blockSize {
		return fmt.Errorf("block > maxStoreBlockSize")
	}
	if !w.headerWritten {
		if final {
			var current []byte
			current = w.cfg.encodeAll(w.enc, w.filling, current)
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
			Checksum:      w.cfg.crc,
			DictID:        w.cfg.dict.ID(),
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

	if raw, ok := w.enc.(rawBlockWriter); ok {
		src := w.filling
		w.nInput += int64(len(src))
		if len(src) == 0 && !final {
			return w.err
		}
		var n int
		n, w.err = raw.writeRaw(w.w, src, final)
		w.nWritten += int64(n)
		if final {
			w.eofWritten = true
		}
		w.filling = w.filling[:0]
		return w.err
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
