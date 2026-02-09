// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "github.com/klauspost/stdgozstd/internal/huff0"

type history struct {
	huffTree         *huff0.Scratch
	decoders         sequenceDecs
	recentOffsets    [3]int
	b                []byte
	ignoreBuffer     int
	windowSize       int
	allocFrameBuffer int
	error            bool
	dict             *dict
}

// reset clears the history for a new frame.
func (h *history) reset() {
	h.b = h.b[:0]
	h.ignoreBuffer = 0
	h.error = false
	h.recentOffsets = [3]int{1, 4, 8}
	h.decoders.freeDecoders()
	h.decoders = sequenceDecs{br: h.decoders.br}
	h.freeHuffDecoder()
	h.huffTree = nil
	h.dict = nil
}

// freeHuffDecoder returns the Huffman decoder to the pool if owned.
func (h *history) freeHuffDecoder() {
	if h.huffTree != nil {
		if h.dict == nil || h.dict.litEnc != h.huffTree {
			huffDecoderPool.Put(h.huffTree)
			h.huffTree = nil
		}
	}
}

// setDict installs a dictionary's tables and content into history.
func (h *history) setDict(dict *dict) {
	if dict == nil {
		return
	}
	h.dict = dict
	h.decoders.litLengths = dict.llDec
	h.decoders.offsets = dict.ofDec
	h.decoders.matchLengths = dict.mlDec
	h.decoders.dict = dict.content
	h.recentOffsets = dict.offsets
	h.huffTree = dict.litEnc
}

// ensureBlock ensures there is room for a full block in the buffer.
func (h *history) ensureBlock() {
	avail := cap(h.b) - len(h.b)
	if avail >= maxCompressedBlockSize {
		// Enough room for a full block — trim if needed.
		if avail < h.windowSize && len(h.b) > h.windowSize {
			discard := len(h.b) - h.windowSize
			copy(h.b, h.b[discard:])
			h.b = h.b[:h.windowSize]
		}
		return
	}
	// Not enough room. Trim old data first if possible.
	if len(h.b) > h.windowSize {
		discard := len(h.b) - h.windowSize
		copy(h.b, h.b[discard:])
		h.b = h.b[:h.windowSize]
		if cap(h.b)-len(h.b) >= maxCompressedBlockSize {
			return
		}
	}
	// Grow lazily instead of allocating full allocFrameBuffer upfront.
	newCap := max(cap(h.b)*2, maxCompressedBlockSize*2)
	if newCap > h.allocFrameBuffer {
		newCap = h.allocFrameBuffer
	}
	if newCap < len(h.b)+maxCompressedBlockSize {
		newCap = len(h.b) + maxCompressedBlockSize
	}
	nb := make([]byte, len(h.b), newCap)
	copy(nb, h.b)
	h.b = nb
}

// appendKeep appends b to the history buffer without trimming.
func (h *history) appendKeep(b []byte) {
	h.b = append(h.b, b...)
}
