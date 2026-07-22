// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"io"
)

var _ io.WriterTo = (*Reader)(nil)

// Reader decompresses a zstd-compressed stream.
type Reader struct {
	cfg   *Decoder
	frame *frameDec
	block *blockDec
	rw    readerWrapper

	buf           []byte
	decodedCount  uint64
	nDecodedTotal int64
	inFrame       bool
	frameSeen     bool
	err           error
	initialized   bool
}

// ensureInit lazily initializes the Reader on first use.
func (z *Reader) ensureInit() {
	if z.initialized {
		return
	}
	if z.cfg == nil {
		z.cfg = &Decoder{}
	}
	z.cfg.ensureInit()
	z.initialized = true
	initPredefined()
	if frame, _ := frameDecPool.Get().(*frameDec); frame != nil {
		frame.o = z.cfg.o
		z.frame = frame
	} else {
		z.frame = newFrameDec(z.cfg.o)
	}
	if block, _ := blockDecPool.Get().(*blockDec); block != nil {
		block.lowMem = z.cfg.o.lowMem
		z.block = block
	} else {
		z.block = &blockDec{lowMem: z.cfg.o.lowMem}
	}
}

// NewReader creates a new Reader that decompresses from r using the
// configuration built from opts. With no options, default configuration is used.
// If r is nil, the Reader must be pointed at a source with [Reader.Reset]
// before streaming.
func NewReader(r io.Reader, opts ...DecoderOption) (*Reader, error) {
	d, err := NewDecoder(opts...)
	if err != nil {
		return nil, err
	}
	z := &Reader{cfg: d}
	z.ensureInit()
	if r != nil {
		z.rw.r = r
	}
	return z, nil
}

// Reset discards the Reader's state and makes it read from r.
// Options in opts are applied to the configuration first; any option may be
// changed and takes effect from the next frame. With no options the current
// configuration is preserved.
// If r is nil, Reset is equivalent to [Reader.Close].
func (z *Reader) Reset(r io.Reader, opts ...DecoderOption) error {
	z.ensureInit()
	if r == nil {
		return z.Close()
	}
	for _, o := range opts {
		if err := o(z.cfg); err != nil {
			return err
		}
	}
	z.rw.r = r
	z.buf = nil
	z.inFrame = false
	z.frameSeen = false
	z.err = nil
	z.decodedCount = 0
	z.nDecodedTotal = 0
	z.frame.history.reset()
	return nil
}

// Read decompresses data into p.
func (z *Reader) Read(p []byte) (int, error) {
	z.ensureInit()
	if z.err != nil {
		return 0, z.err
	}

	var written int
	for len(p) > 0 {
		// Drain buffered data first.
		if len(z.buf) > 0 {
			n := copy(p, z.buf)
			z.buf = z.buf[n:]
			written += n
			p = p[n:]
			continue
		}

		// Start a new frame if needed.
		if !z.inFrame {
			z.frame.o = z.cfg.o
			err := z.frame.reset(&z.rw)
			if err != nil {
				if err == io.EOF {
					if written > 0 {
						return written, nil
					}
					if !z.frameSeen {
						z.err = &ErrCorrupted{msg: "empty input", err: io.ErrUnexpectedEOF}
						return 0, z.err
					}
					z.err = io.EOF
					return 0, io.EOF
				}
				z.err = err
				return written, err
			}
			z.frameSeen = true
			z.inFrame = true
			z.decodedCount = 0
			z.frame.history.reset()

			if err := applyFrameDict(z.frame, z.cfg.dicts); err != nil {
				z.err = err
				return written, z.err
			}
		}

		// Decode next block.
		z.frame.history.ensureBlock()
		prevLen := len(z.frame.history.b)

		if err := z.frame.next(z.block); err != nil {
			z.err = err
			return written, err
		}

		if err := z.block.decodeBuf(&z.frame.history); err != nil {
			z.err = err
			return written, err
		}

		decoded := z.frame.history.b[prevLen:]
		z.decodedCount += uint64(len(decoded))
		z.nDecodedTotal += int64(len(decoded))
		if m := z.frame.o.maxDecodedSize; m > 0 && z.nDecodedTotal > m {
			z.err = &ErrDecodedSizeExceeded{Allowed: m, Produced: z.nDecodedTotal}
			return written, z.err
		}

		if z.frame.HasCheckSum {
			_, _ = z.frame.crc.Write(decoded)
		}

		z.buf = decoded

		if z.block.Last {
			if z.frame.FrameContentSize != fcsUnknown && z.decodedCount != z.frame.FrameContentSize {
				z.err = errFrameSizeMismatch
				return written, z.err
			}
			if z.frame.HasCheckSum {
				if err := z.frame.checkCRC(); err != nil {
					z.err = err
					return written, err
				}
			}
			z.inFrame = false
		}
	}

	return written, nil
}

// Close releases resources but retains configuration.
// After Close, the Reader may be reused by calling
// [Reader.Reset].
func (z *Reader) Close() error {
	z.ensureInit()
	z.err = ErrDecoderClosed
	z.buf = nil
	z.frame.history.reset()
	frameDecPool.Put(z.frame)
	z.frame = nil
	blockDecPool.Put(z.block)
	z.block = nil
	z.initialized = false
	return nil
}

// WriteTo decompresses data and writes it to w until all frames are consumed
// or an error occurs. It implements [io.WriterTo].
//
// WriteTo writes decoded blocks directly to w without an intermediate copy,
// making it more efficient than reading into a buffer and writing separately.
//
// If the Reader has buffered data from a previous Read call, that data is
// flushed to w first.
func (z *Reader) WriteTo(w io.Writer) (int64, error) {
	z.ensureInit()
	if z.err != nil {
		return 0, z.err
	}

	var written int64

	// Drain any buffered data from a previous Read call.
	if len(z.buf) > 0 {
		n, err := w.Write(z.buf)
		written += int64(n)
		z.buf = nil
		if err != nil {
			z.err = err
			return written, err
		}
	}

	for {
		if !z.inFrame {
			z.frame.o = z.cfg.o
			err := z.frame.reset(&z.rw)
			if err != nil {
				if err == io.EOF {
					if !z.frameSeen {
						z.err = &ErrCorrupted{msg: "empty input", err: io.ErrUnexpectedEOF}
						return written, z.err
					}
					return written, nil
				}
				z.err = err
				return written, err
			}
			z.frameSeen = true
			z.inFrame = true
			z.decodedCount = 0
			z.frame.history.reset()

			if err := applyFrameDict(z.frame, z.cfg.dicts); err != nil {
				z.err = err
				return written, z.err
			}
		}

		z.frame.history.ensureBlock()
		prevLen := len(z.frame.history.b)

		if err := z.frame.next(z.block); err != nil {
			z.err = err
			return written, err
		}
		if err := z.block.decodeBuf(&z.frame.history); err != nil {
			z.err = err
			return written, err
		}

		decoded := z.frame.history.b[prevLen:]
		z.decodedCount += uint64(len(decoded))
		z.nDecodedTotal += int64(len(decoded))
		if m := z.frame.o.maxDecodedSize; m > 0 && z.nDecodedTotal > m {
			z.err = &ErrDecodedSizeExceeded{Allowed: m, Produced: z.nDecodedTotal}
			return written, z.err
		}

		if z.frame.HasCheckSum {
			_, _ = z.frame.crc.Write(decoded)
		}

		if len(decoded) > 0 {
			n, err := w.Write(decoded)
			written += int64(n)
			if err != nil {
				z.err = err
				return written, err
			}
		}

		if z.block.Last {
			if z.frame.FrameContentSize != fcsUnknown && z.decodedCount != z.frame.FrameContentSize {
				z.err = errFrameSizeMismatch
				return written, z.err
			}
			if z.frame.HasCheckSum {
				if err := z.frame.checkCRC(); err != nil {
					z.err = err
					return written, err
				}
			}
			z.inFrame = false
		}
	}
}
