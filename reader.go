// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "io"

var _ io.WriterTo = (*Reader)(nil)

// Reader decompresses a zstd-compressed stream.
type Reader struct {
	frame frameDec
	block blockDec
	rw    readerWrapper

	buf          []byte
	dicts        map[uint32]*dict
	decodedCount uint64
	inFrame      bool
	frameSeen    bool
	err          error
	initialized  bool
}

func (z *Reader) ensureInit() {
	if z.initialized {
		return
	}
	z.initialized = true
	initPredefined()
	z.frame = *newFrameDec(decoderOptions{
		maxWindowSize: 128 << 20,
		lowMem:        true,
	})
	z.block = blockDec{lowMem: true}
}

// NewReader creates a new Reader reading from r.
// If r is nil, the Reader may only be used with [Reader.DecodeBytes];
// call [Reader.Reset] before streaming.
func NewReader(r io.Reader) (*Reader, error) {
	z := &Reader{}
	z.ensureInit()
	if r != nil {
		z.rw.r = r
	}
	return z, nil
}

// Reset discards the Reader's state and makes it read from r.
// If r is nil, Reset is equivalent to [Reader.Close].
func (z *Reader) Reset(r io.Reader) error {
	z.ensureInit()
	if r == nil {
		return z.Close()
	}
	z.rw.r = r
	z.buf = nil
	z.inFrame = false
	z.frameSeen = false
	z.err = nil
	z.decodedCount = 0
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

			if z.frame.DictionaryID != 0 {
				d, ok := z.dicts[z.frame.DictionaryID]
				if !ok {
					z.err = ErrUnknownDictionary
					return written, z.err
				}
				z.frame.history.setDict(d)
			} else if d, ok := z.dicts[0]; ok {
				// Raw dict (ID 0): only use content for match references.
				z.frame.history.dict = d
				z.frame.history.decoders.dict = d.content
			}
		}

		// Decode next block.
		z.frame.history.ensureBlock()
		prevLen := len(z.frame.history.b)

		if err := z.frame.next(&z.block); err != nil {
			z.err = err
			return written, err
		}

		if err := z.block.decodeBuf(&z.frame.history); err != nil {
			z.err = err
			return written, err
		}

		decoded := z.frame.history.b[prevLen:]
		z.decodedCount += uint64(len(decoded))

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

// AppendDecompress decompresses src and appends the decompressed bytes to dst,
// returning the extended buffer. It is a one-shot alternative to the streaming
// Read interface.
//
// src may contain one or more concatenated zstd frames.
//
// The Reader must not be in the middle of a streaming Read; call Reset
// before switching from streaming to one-shot use.
//
// Passing nil for dst allocates a new slice. Passing a non-nil dst lets the
// caller reuse memory or prepend existing data:
//
//	result, err := r.AppendDecompress(existingPrefix, compressed)
//
// Any registered dictionaries (via AddDict or SetRawDict) apply.
func (z *Reader) AppendDecompress(dst, src []byte) ([]byte, error) {
	z.ensureInit()
	if z.err == ErrDecoderClosed {
		return nil, ErrDecoderClosed
	}
	z.frame.bBuf = byteBuf(src)
	var frameSeen bool
	for {
		err := z.frame.reset(&z.frame.bBuf)
		if err == io.EOF {
			if !frameSeen {
				return dst, &ErrCorrupted{msg: "empty input", err: io.ErrUnexpectedEOF}
			}
			return dst, nil
		}
		if err != nil {
			return dst, err
		}
		frameSeen = true
		z.frame.history.reset()

		if z.frame.DictionaryID != 0 {
			d, ok := z.dicts[z.frame.DictionaryID]
			if !ok {
				return dst, ErrUnknownDictionary
			}
			z.frame.history.setDict(d)
		} else if d, ok := z.dicts[0]; ok {
			z.frame.history.dict = d
			z.frame.history.decoders.dict = d.content
		}

		var err2 error
		dst, err2 = z.frame.runDecoder(dst, &z.block)
		if err2 != nil {
			return dst, err2
		}
	}
}

// Close releases resources. After Close, the Reader may be reused by
// calling [Reader.Reset].
func (z *Reader) Close() error {
	z.ensureInit()
	z.err = ErrDecoderClosed
	z.buf = nil
	z.frame.history.freeHuffDecoder()
	z.frame.history.decoders.freeDecoders()
	return nil
}

// SetMaxWindowSize sets the maximum allowed window size for decoding.
// The default is 128 MiB. The maximum is MaxWindowSize (512 MiB).
func (z *Reader) SetMaxWindowSize(n uint64) {
	z.ensureInit()
	z.frame.o.maxWindowSize = n
}

// AddDict registers a dictionary for decompression.
func (z *Reader) AddDict(d *Dict) {
	z.ensureInit()
	if d == nil || d.d == nil {
		return
	}
	if z.dicts == nil {
		z.dicts = make(map[uint32]*dict)
	}
	z.dicts[d.d.id] = d.d
}

// SetRawDict registers raw bytes as a dictionary with ID 0.
func (z *Reader) SetRawDict(b []byte) {
	z.ensureInit()
	if z.dicts == nil {
		z.dicts = make(map[uint32]*dict)
	}
	z.dicts[0] = &dict{id: 0, content: b, offsets: [3]int{1, 4, 8}}
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

			if z.frame.DictionaryID != 0 {
				d, ok := z.dicts[z.frame.DictionaryID]
				if !ok {
					z.err = ErrUnknownDictionary
					return written, z.err
				}
				z.frame.history.setDict(d)
			} else if d, ok := z.dicts[0]; ok {
				z.frame.history.dict = d
				z.frame.history.decoders.dict = d.content
			}
		}

		z.frame.history.ensureBlock()
		prevLen := len(z.frame.history.b)

		if err := z.frame.next(&z.block); err != nil {
			z.err = err
			return written, err
		}
		if err := z.block.decodeBuf(&z.frame.history); err != nil {
			z.err = err
			return written, err
		}

		decoded := z.frame.history.b[prevLen:]
		z.decodedCount += uint64(len(decoded))

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
