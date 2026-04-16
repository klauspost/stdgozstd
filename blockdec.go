// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"io"
	"sync"

	"github.com/klauspost/stdgozstd/internal/huff0"
)

// blockType identifies the encoding type of a zstd block.
type blockType uint8

// Block type constants as defined in the zstd specification.
const (
	blockTypeRaw blockType = iota
	blockTypeRLE
	blockTypeCompressed
	blockTypeReserved
)

// literalsBlockType identifies the encoding type of the literals section.
type literalsBlockType uint8

// Literals block type constants as defined in the zstd specification.
const (
	literalsBlockRaw literalsBlockType = iota
	literalsBlockRLE
	literalsBlockCompressed
	literalsBlockTreeless
)

// Block and sequence size limits.
const (
	maxCompressedBlockSize      = 128 << 10
	compressedBlockOverAlloc    = 16
	maxCompressedBlockSizeAlloc = 128<<10 + compressedBlockOverAlloc
	maxBlockSize                = (1 << 21) - 1
	maxMatchLen                 = 131074
)

// Decoder object pools for reuse across blocks.
var (
	huffDecoderPool = sync.Pool{New: func() any { return &huff0.Scratch{} }}
	fseDecoderPool  = sync.Pool{New: func() any { return &fseDecoder{} }}
)

// blockDec decodes a single zstd block.
type blockDec struct {
	data        []byte
	dataStorage []byte
	dst         []byte
	literalBuf  []byte
	WindowSize  uint64
	RLESize     uint32
	Type        blockType
	Last        bool
	lowMem      bool
}

// reset reads the block header and data from br.
func (b *blockDec) reset(br byteBuffer, windowSize uint64) error {
	b.WindowSize = windowSize
	tmp, err := br.readSmall(3)
	if err != nil {
		return &ErrCorrupted{msg: "reading block header", err: err}
	}
	bh := uint32(tmp[0]) | (uint32(tmp[1]) << 8) | (uint32(tmp[2]) << 16)
	b.Last = bh&1 != 0
	b.Type = blockType((bh >> 1) & 3)
	cSize := int(bh >> 3)
	maxSize := maxCompressedBlockSizeAlloc

	switch b.Type {
	case blockTypeReserved:
		return errReservedBlockType
	case blockTypeRLE:
		if cSize > maxCompressedBlockSize || cSize > int(b.WindowSize) {
			return &ErrWindowSizeExceeded{Allowed: min(b.WindowSize, maxCompressedBlockSize), Requested: uint64(cSize)}
		}
		b.RLESize = uint32(cSize)
		if b.lowMem {
			maxSize = cSize
		}
		cSize = 1
	case blockTypeCompressed:
		b.RLESize = 0
		maxSize = maxCompressedBlockSizeAlloc
		if windowSize < maxCompressedBlockSize && b.lowMem {
			maxSize = int(windowSize) + compressedBlockOverAlloc
		}
		if cSize > maxCompressedBlockSize || uint64(cSize) > b.WindowSize {
			return errCompressedSizeTooBig
		}
		if cSize < 2 {
			return errBlockTooSmall
		}
	case blockTypeRaw:
		if cSize > maxCompressedBlockSize || cSize > int(b.WindowSize) {
			return &ErrWindowSizeExceeded{Allowed: min(b.WindowSize, maxCompressedBlockSize), Requested: uint64(cSize)}
		}
		b.RLESize = 0
		maxSize = -1
	default:
		panic("invalid block type")
	}

	if _, ok := br.(*byteBuf); !ok && cap(b.dataStorage) < cSize {
		if b.lowMem || cSize > maxCompressedBlockSize {
			b.dataStorage = make([]byte, 0, cSize+compressedBlockOverAlloc)
		} else {
			b.dataStorage = make([]byte, 0, maxCompressedBlockSizeAlloc)
		}
	}
	b.data, err = br.readBig(cSize, b.dataStorage)
	if err != nil {
		return &ErrCorrupted{msg: "reading block data", err: err}
	}
	if cap(b.dst) <= maxSize {
		b.dst = make([]byte, 0, maxSize+1)
	}
	return nil
}

// decodeBuf decodes the block content into the history buffer.
func (b *blockDec) decodeBuf(hist *history) error {
	switch b.Type {
	case blockTypeRLE:
		if cap(b.dst) < int(b.RLESize) {
			if b.lowMem {
				b.dst = make([]byte, b.RLESize)
			} else {
				b.dst = make([]byte, maxCompressedBlockSize)
			}
		}
		b.dst = b.dst[:b.RLESize]
		v := b.data[0]
		for i := range b.dst {
			b.dst[i] = v
		}
		hist.appendKeep(b.dst)
		return nil
	case blockTypeRaw:
		hist.appendKeep(b.data)
		return nil
	case blockTypeCompressed:
		saved := b.dst
		if hist.ignoreBuffer == 0 {
			b.dst = hist.b
			hist.b = nil
		} else {
			b.dst = b.dst[:0]
		}
		err := b.decodeCompressed(hist)
		if hist.ignoreBuffer == 0 {
			hist.b = b.dst
			b.dst = saved
		} else {
			hist.appendKeep(b.dst)
		}
		return err
	case blockTypeReserved:
		return errReservedBlockType
	default:
		return corruptedErrorf("invalid block type %d", b.Type)
	}
}

// decodeLiterals parses and decompresses the literals section.
func (b *blockDec) decodeLiterals(in []byte, hist *history) (remain []byte, err error) {
	if len(in) < 2 {
		return in, errBlockTooSmall
	}

	litType := literalsBlockType(in[0] & 3)
	var litRegenSize int
	var litCompSize int
	sizeFormat := (in[0] >> 2) & 3
	var fourStreams bool
	var literals []byte

	switch litType {
	case literalsBlockRaw, literalsBlockRLE:
		switch sizeFormat {
		case 0, 2:
			litRegenSize = int(in[0] >> 3)
			in = in[1:]
		case 1:
			litRegenSize = int(in[0]>>4) + (int(in[1]) << 4)
			in = in[2:]
		case 3:
			if len(in) < 3 {
				return in, errBlockTooSmall
			}
			litRegenSize = int(in[0]>>4) + (int(in[1]) << 4) + (int(in[2]) << 12)
			in = in[3:]
		}
	case literalsBlockCompressed, literalsBlockTreeless:
		switch sizeFormat {
		case 0, 1:
			if len(in) < 3 {
				return in, errBlockTooSmall
			}
			n := uint64(in[0]>>4) + (uint64(in[1]) << 4) + (uint64(in[2]) << 12)
			litRegenSize = int(n & 1023)
			litCompSize = int(n >> 10)
			fourStreams = sizeFormat == 1
			in = in[3:]
		case 2:
			fourStreams = true
			if len(in) < 4 {
				return in, errBlockTooSmall
			}
			n := uint64(in[0]>>4) + (uint64(in[1]) << 4) + (uint64(in[2]) << 12) + (uint64(in[3]) << 20)
			litRegenSize = int(n & 16383)
			litCompSize = int(n >> 14)
			in = in[4:]
		case 3:
			fourStreams = true
			if len(in) < 5 {
				return in, errBlockTooSmall
			}
			n := uint64(in[0]>>4) + (uint64(in[1]) << 4) + (uint64(in[2]) << 12) + (uint64(in[3]) << 20) + (uint64(in[4]) << 28)
			litRegenSize = int(n & 262143)
			litCompSize = int(n >> 18)
			in = in[5:]
		}
	}

	if litRegenSize > int(b.WindowSize) || litRegenSize > maxCompressedBlockSize {
		return in, &ErrWindowSizeExceeded{Allowed: min(b.WindowSize, maxCompressedBlockSize), Requested: uint64(litRegenSize)}
	}

	switch litType {
	case literalsBlockRaw:
		if len(in) < litRegenSize {
			return in, errBlockTooSmall
		}
		literals = in[:litRegenSize]
		in = in[litRegenSize:]
	case literalsBlockRLE:
		if len(in) < 1 {
			return in, errBlockTooSmall
		}
		if cap(b.literalBuf) < litRegenSize {
			if b.lowMem {
				b.literalBuf = make([]byte, litRegenSize, litRegenSize+compressedBlockOverAlloc)
			} else {
				b.literalBuf = make([]byte, litRegenSize, maxCompressedBlockSize+compressedBlockOverAlloc)
			}
		}
		literals = b.literalBuf[:litRegenSize]
		v := in[0]
		for i := range literals {
			literals[i] = v
		}
		in = in[1:]
	case literalsBlockTreeless:
		if len(in) < litCompSize {
			return in, errBlockTooSmall
		}
		literals = in[:litCompSize]
		in = in[litCompSize:]
		huff := hist.huffTree
		if huff == nil {
			return in, corruptedError("treeless literal block, but no history defined")
		}
		if cap(b.literalBuf) < litRegenSize {
			if b.lowMem {
				b.literalBuf = make([]byte, 0, litRegenSize+compressedBlockOverAlloc)
			} else {
				b.literalBuf = make([]byte, 0, maxCompressedBlockSize+compressedBlockOverAlloc)
			}
		}
		huff.MaxDecodedSize = litRegenSize
		if fourStreams {
			literals, err = huff.Decoder().Decompress4X(b.literalBuf[:0:litRegenSize], literals)
		} else {
			literals, err = huff.Decoder().Decompress1X(b.literalBuf[:0:litRegenSize], literals)
		}
		if err != nil {
			return in, &ErrCorrupted{msg: "decompressing literals", err: err}
		}
		if len(literals) != litRegenSize {
			return in, corruptedErrorf("literal output size mismatch: want %d, got %d", litRegenSize, len(literals))
		}
	case literalsBlockCompressed:
		if len(in) < litCompSize {
			return in, errBlockTooSmall
		}
		literals = in[:litCompSize]
		in = in[litCompSize:]
		if cap(b.literalBuf) < litRegenSize {
			if b.lowMem {
				b.literalBuf = make([]byte, 0, litRegenSize+compressedBlockOverAlloc)
			} else {
				b.literalBuf = make([]byte, 0, maxCompressedBlockSize+compressedBlockOverAlloc)
			}
		}
		huff := hist.huffTree
		if huff == nil || (hist.dict != nil && huff == hist.dict.litEnc) {
			huff = huffDecoderPool.Get().(*huff0.Scratch)
		}
		huff, literals, err = huff0.ReadTable(literals, huff)
		if err != nil {
			return in, &ErrCorrupted{msg: "reading huffman table", err: err}
		}
		hist.huffTree = huff
		huff.MaxDecodedSize = litRegenSize
		if fourStreams {
			literals, err = huff.Decoder().Decompress4X(b.literalBuf[:0:litRegenSize], literals)
		} else {
			literals, err = huff.Decoder().Decompress1X(b.literalBuf[:0:litRegenSize], literals)
		}
		if err != nil {
			return in, &ErrCorrupted{msg: "decompressing literals", err: err}
		}
		if len(literals) != litRegenSize {
			return in, corruptedErrorf("literal output size mismatch: want %d, got %d", litRegenSize, len(literals))
		}
		literals = b.literalBuf[:len(literals)]
	}

	hist.decoders.literals = literals
	return in, nil
}

// decodeCompressed decodes a compressed block's literals and sequences.
func (b *blockDec) decodeCompressed(hist *history) error {
	in := b.data
	in, err := b.decodeLiterals(in, hist)
	if err != nil {
		return err
	}
	err = b.prepareSequences(in, hist)
	if err != nil {
		return err
	}
	if hist.decoders.nSeqs == 0 {
		b.dst = append(b.dst, hist.decoders.literals...)
		return nil
	}
	err = hist.decoders.decodeSync(hist.b[hist.ignoreBuffer:])
	if err != nil {
		return err
	}
	b.dst = hist.decoders.out
	hist.recentOffsets = hist.decoders.prevOffset
	return nil
}

// prepareSequences parses sequence headers and FSE tables.
func (b *blockDec) prepareSequences(in []byte, hist *history) (err error) {
	if len(in) < 1 {
		return errBlockTooSmall
	}
	var nSeqs int
	seqHeader := in[0]
	switch {
	case seqHeader < 128:
		nSeqs = int(seqHeader)
		in = in[1:]
	case seqHeader < 255:
		if len(in) < 2 {
			return errBlockTooSmall
		}
		nSeqs = int(seqHeader-128)<<8 | int(in[1])
		in = in[2:]
	case seqHeader == 255:
		if len(in) < 3 {
			return errBlockTooSmall
		}
		nSeqs = 0x7f00 + int(in[1]) + (int(in[2]) << 8)
		in = in[3:]
	}

	if nSeqs == 0 && len(in) != 0 {
		return errUnexpectedBlockSize
	}

	seqs := &hist.decoders
	seqs.nSeqs = nSeqs

	if nSeqs > 0 {
		if len(in) < 1 {
			return errBlockTooSmall
		}
		br := byteReader{b: in, off: 0}
		compMode := br.Uint8()
		br.advance(1)
		if compMode&3 != 0 {
			return corruptedError("reserved sequence bits not zero")
		}
		for i := range uint(3) {
			mode := seqCompMode((compMode >> (6 - i*2)) & 3)
			var sd *sequenceDec
			switch tableIndex(i) {
			case tableLiteralLengths:
				sd = &seqs.litLengths
			case tableOffsets:
				sd = &seqs.offsets
			case tableMatchLengths:
				sd = &seqs.matchLengths
			default:
				panic("unknown table")
			}
			switch mode {
			case compModePredefined:
				if sd.fse != nil && !sd.fse.preDefined {
					fseDecoderPool.Put(sd.fse)
				}
				sd.fse = &fsePredef[i]
			case compModeRLE:
				if br.remain() < 1 {
					return errBlockTooSmall
				}
				v := br.Uint8()
				br.advance(1)
				if sd.fse == nil || sd.fse.preDefined {
					sd.fse = fseDecoderPool.Get().(*fseDecoder)
				}
				symb, err := decSymbolValue(v, symbolTableX[i])
				if err != nil {
					return err
				}
				sd.fse.setRLE(symb)
			case compModeFSE:
				if sd.fse == nil || sd.fse.preDefined {
					sd.fse = fseDecoderPool.Get().(*fseDecoder)
				}
				err := sd.fse.readNCount(&br, uint16(maxTableSymbol[i]))
				if err != nil {
					return err
				}
				err = sd.fse.transform(symbolTableX[i])
				if err != nil {
					return err
				}
			case compModeRepeat:
				sd.repeat = true
			}
			if br.overread() {
				return io.ErrUnexpectedEOF
			}
		}
		in = br.unread()
	}

	if nSeqs == 0 {
		return nil
	}

	bReader := seqs.br
	if bReader == nil {
		bReader = &bitReader{}
	}
	if err := bReader.init(in); err != nil {
		return err
	}

	return seqs.initialize(bReader, hist, b.dst)
}
