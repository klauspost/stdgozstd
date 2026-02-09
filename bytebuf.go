// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"fmt"
	"io"
)

type byteBuffer interface {
	readSmall(n int) ([]byte, error)
	readBig(n int, dst []byte) ([]byte, error)
	readByte() (byte, error)
	skipN(n int64) error
}

type byteBuf []byte

// readSmall reads n bytes from the buffer.
func (b *byteBuf) readSmall(n int) ([]byte, error) {
	bb := *b
	if len(bb) < n {
		return nil, io.ErrUnexpectedEOF
	}
	r := bb[:n]
	*b = bb[n:]
	return r, nil
}

// readBig reads n bytes, ignoring dst (data is already in memory).
func (b *byteBuf) readBig(n int, dst []byte) ([]byte, error) {
	bb := *b
	if len(bb) < n {
		return nil, io.ErrUnexpectedEOF
	}
	r := bb[:n]
	*b = bb[n:]
	return r, nil
}

// readByte reads a single byte.
func (b *byteBuf) readByte() (byte, error) {
	bb := *b
	if len(bb) < 1 {
		return 0, io.ErrUnexpectedEOF
	}
	r := bb[0]
	*b = bb[1:]
	return r, nil
}

// skipN discards n bytes.
func (b *byteBuf) skipN(n int64) error {
	bb := *b
	if n < 0 {
		return fmt.Errorf("negative skip (%d) requested", n)
	}
	if int64(len(bb)) < n {
		return io.ErrUnexpectedEOF
	}
	*b = bb[n:]
	return nil
}

type readerWrapper struct {
	r   io.Reader
	tmp [8]byte
}

// readSmall reads n bytes (max 8) into the internal temp buffer.
func (r *readerWrapper) readSmall(n int) ([]byte, error) {
	n2, err := io.ReadFull(r.r, r.tmp[:n])
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	_ = n2
	return r.tmp[:n], nil
}

// readBig reads n bytes into dst, allocating if needed.
func (r *readerWrapper) readBig(n int, dst []byte) ([]byte, error) {
	if cap(dst) < n {
		dst = make([]byte, n)
	}
	n2, err := io.ReadFull(r.r, dst[:n])
	if err == io.EOF && n > 0 {
		err = io.ErrUnexpectedEOF
	}
	return dst[:n2], err
}

// readByte reads a single byte from the underlying reader.
func (r *readerWrapper) readByte() (byte, error) {
	n2, err := io.ReadFull(r.r, r.tmp[:1])
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	if n2 != 1 {
		return 0, io.ErrUnexpectedEOF
	}
	return r.tmp[0], nil
}

// skipN discards n bytes from the underlying reader.
func (r *readerWrapper) skipN(n int64) error {
	n2, err := io.CopyN(io.Discard, r.r, n)
	if n2 != n {
		err = io.ErrUnexpectedEOF
	}
	return err
}
