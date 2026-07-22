// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"fmt"
	"io"
	"sync"
)

var defaultDecoderOptions = decoderOptions{
	maxWindowSize: 128 << 20,
	lowMem:        true,
}

// Decoder holds decompression configuration and provides one-shot decompression
// via [Decoder.AppendDecompress]. Configuration is fixed at construction via
// [DecoderOption] values and is immutable afterwards.
type Decoder struct {
	o     decoderOptions
	dicts map[uint32]*dict
	once  sync.Once
}

// ensureInit lazily applies default configuration on first use.
func (d *Decoder) ensureInit() {
	d.once.Do(func() {
		initPredefined()
		d.o = defaultDecoderOptions
	})
}

// NewDecoder returns a new Decoder configured by opts. Options are applied in
// order; a later option overrides an earlier one. With no options the Decoder
// uses default limits.
func NewDecoder(opts ...DecoderOption) (*Decoder, error) {
	d := &Decoder{}
	d.ensureInit()
	for _, o := range opts {
		if err := o(d); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// DecoderOption configures a [Decoder]. Options are passed to [NewDecoder],
// [NewReader], and [Reader.Reset]. Every option may be changed via
// [Reader.Reset]; the new configuration applies from the next frame.
type DecoderOption func(*Decoder) error

// WithDecoderMaxWindow sets the maximum allowed window size for decoding.
// The default is 128 MiB.
//
// n must be in the range [MinWindowSize, MaxWindowSize].
func WithDecoderMaxWindow(n int) DecoderOption {
	return func(d *Decoder) error {
		if n < MinWindowSize || n > MaxWindowSize {
			return fmt.Errorf("zstd: max window size %d out of range [%d, %d]", n, MinWindowSize, MaxWindowSize)
		}
		d.o.maxWindowSize = n
		return nil
	}
}

// WithDecoderMaxSize limits the total number of decompressed bytes produced.
// The default, 0, disables the limit; a negative n is an error.
//
// For streaming ([Reader.Read] / [Reader.WriteTo]) the limit applies to the
// total output produced since the last [Reader.Reset]. For
// [Decoder.AppendDecompress] it applies to the total output of a single call
// (across any concatenated frames), excluding bytes already present in dst.
//
// The limit is enforced per block, so output may exceed n by up to one block
// (128 KiB) before an [ErrDecodedSizeExceeded] is returned.
func WithDecoderMaxSize(n int64) DecoderOption {
	return func(d *Decoder) error {
		if n < 0 {
			return fmt.Errorf("zstd: max decoded size %d must be >= 0", n)
		}
		d.o.maxDecodedSize = n
		return nil
	}
}

// WithDecoderDict registers a dictionary for decompression.
// Passing nil removes all previously registered dictionaries.
// A non-nil Dict that was not created by [ParseDict] is ignored.
func WithDecoderDict(pd *Dict) DecoderOption {
	return func(d *Decoder) error {
		if pd == nil || pd.d == nil {
			if pd == nil {
				clear(d.dicts)
			}
			return nil
		}
		if d.dicts == nil {
			d.dicts = make(map[uint32]*dict)
		}
		d.dicts[pd.d.id] = pd.d
		return nil
	}
}

// WithDecoderRawDict registers raw bytes as a dictionary with ID 0.
// The dictionary must be at least 8 bytes; shorter values are ignored.
// Passing nil removes all previously registered dictionaries.
func WithDecoderRawDict(b []byte) DecoderOption {
	return func(d *Decoder) error {
		if b == nil {
			clear(d.dicts)
			return nil
		}
		if len(b) < 8 {
			return nil
		}
		if d.dicts == nil {
			d.dicts = make(map[uint32]*dict)
		}
		d.dicts[0] = &dict{id: 0, content: b, offsets: [3]int{1, 4, 8}}
		return nil
	}
}

// AppendDecompress decompresses src and appends the decompressed bytes to dst,
// returning the extended buffer. It is a one-shot alternative to the streaming
// [Reader] interface.
//
// src may contain one or more concatenated zstd frames.
//
// Passing nil for dst allocates a new slice. Passing a non-nil dst lets the
// caller reuse memory or prepend existing data:
//
//	result, err := d.AppendDecompress(existingPrefix, compressed)
//
// Any configured dictionaries (via WithDecoderDict or WithDecoderRawDict) apply.
//
// AppendDecompress is safe for concurrent use on the same Decoder.
func (d *Decoder) AppendDecompress(dst, src []byte) ([]byte, error) {
	d.ensureInit()

	frame, _ := frameDecPool.Get().(*frameDec)
	if frame == nil {
		frame = newFrameDec(d.o)
	} else {
		frame.o = d.o
	}
	block, _ := blockDecPool.Get().(*blockDec)
	if block == nil {
		block = &blockDec{lowMem: d.o.lowMem}
	} else {
		block.lowMem = d.o.lowMem
	}
	defer func() {
		// Drop references to the caller's src and dst so the pool does not pin
		// caller-owned buffers between decodes.
		frame.bBuf = nil
		frame.history.b = nil
		frameDecPool.Put(frame)
		blockDecPool.Put(block)
	}()

	dstStart := len(dst)
	frame.bBuf = byteBuf(src)
	var frameSeen bool
	for {
		err := frame.reset(&frame.bBuf)
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
		frame.history.reset()

		if err := applyFrameDict(frame, d.dicts); err != nil {
			return dst, err
		}

		var err2 error
		dst, err2 = frame.runDecoder(dst, block, d.o.maxDecodedSize, dstStart)
		if err2 != nil {
			return dst, err2
		}
	}
}
