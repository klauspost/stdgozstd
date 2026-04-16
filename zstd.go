// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package zstd implements reading and writing of zstd compressed data,
// as specified in RFC 8878.
//
// A [Writer] compresses data written to it. A [Reader] decompresses data
// read from it.
//
// Both types support streaming and one-shot operation. For one-shot use,
// [Writer.AppendCompress] and [Reader.AppendDecompress] compress and
// decompress a complete byte slice without the overhead of a streaming
// interface.
package zstd

import (
	"errors"
	"fmt"
	"math"

	"github.com/klauspost/stdgozstd/internal/le"
)

// ErrCorrupted indicates that the input data is not valid zstd.
// Use errors.Is(err, &ErrCorrupted{}) to test for any corruption error.
type ErrCorrupted struct {
	msg string
	err error
}

// Error implements the error interface.
func (e *ErrCorrupted) Error() string {
	if e.err != nil {
		if e.msg != "" {
			return e.msg + ": " + e.err.Error()
		}
		return e.err.Error()
	}
	return e.msg
}

// Is reports whether target is an *ErrCorrupted.
func (e *ErrCorrupted) Is(target error) bool {
	_, ok := target.(*ErrCorrupted)
	return ok
}

// Unwrap returns the underlying error, if any.
func (e *ErrCorrupted) Unwrap() error { return e.err }

// corruptedError returns a new ErrCorrupted with the given message.
func corruptedError(msg string) *ErrCorrupted {
	return &ErrCorrupted{msg: msg}
}

// corruptedErrorf returns a new ErrCorrupted with a formatted message.
func corruptedErrorf(format string, args ...any) *ErrCorrupted {
	return &ErrCorrupted{msg: fmt.Sprintf(format, args...)}
}

const zstdMinMatch = 3            // minimum match length per the zstd specification
const fcsUnknown = math.MaxUint64 // sentinel for unknown frame content size

// Parent zstd uses 30; fillBase in fse_predefined.go expects this.
const maxOffsetBits = 30

// ErrWindowSizeExceeded is returned when a frame or block requests
// a window larger than the configured maximum.
type ErrWindowSizeExceeded struct {
	Allowed, Requested uint64
}

// Error implements the error interface.
func (e *ErrWindowSizeExceeded) Error() string {
	return fmt.Sprintf("window size exceeded (requested %d, allowed %d)", e.Requested, e.Allowed)
}

// Is reports whether target is an *ErrWindowSizeExceeded.
func (e *ErrWindowSizeExceeded) Is(target error) bool {
	_, ok := target.(*ErrWindowSizeExceeded)
	return ok
}

// Errors returned by the zstd encoder and decoder.
var (
	ErrUnknownDictionary = errors.New("unknown dictionary")
	ErrDecoderClosed     = errors.New("decoder used after Close")
	ErrEncoderClosed     = errors.New("encoder used after Close")
)

// Corruption sentinel errors returned during decoding.
var (
	errReservedBlockType    = corruptedError("reserved block type")
	errCompressedSizeTooBig = corruptedError("compressed size too big")
	errBlockTooSmall        = corruptedError("block too small")
	errUnexpectedBlockSize  = corruptedError("unexpected block size")
	errMagicMismatch        = corruptedError("magic number mismatch")
	errWindowSizeTooSmall   = corruptedError("window size too small")
	errFrameSizeMismatch    = corruptedError("frame size does not match")
	errCRCMismatch          = corruptedError("CRC check failed")
)

// load3232 loads a uint32 from b at int32 index.
func load3232(b []byte, i int32) uint32 {
	return le.Load32(b, i)
}

// load6432 loads a uint64 from b at int32 index.
func load6432(b []byte, i int32) uint64 {
	return le.Load64(b, i)
}
