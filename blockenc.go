// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"errors"
	"math"
	"slices"

	"github.com/klauspost/stdgozstd/internal/huff0"
)

type blockEnc struct {
	size       int
	literals   []byte
	sequences  []seq
	coders     seqCoders
	litEnc     *huff0.Scratch
	dictLitEnc *huff0.Scratch
	wr         bitWriter

	extraLits         int
	output            []byte
	recentOffsets     [3]uint32
	prevRecentOffsets [3]uint32

	last   bool
	lowMem bool
}

// init allocates buffers and encoders for the block.
func (b *blockEnc) init() {
	if b.lowMem {
		if cap(b.literals) < 1<<10 {
			b.literals = make([]byte, 0, 1<<10)
		}
		const defSeqs = 20
		if cap(b.sequences) < defSeqs {
			b.sequences = make([]seq, 0, defSeqs)
		}
		if cap(b.output) < 1<<10 {
			b.output = make([]byte, 0, 1<<10)
		}
	} else {
		if cap(b.literals) < maxCompressedBlockSize {
			b.literals = make([]byte, 0, maxCompressedBlockSize)
		}
		const defSeqs = 2000
		if cap(b.sequences) < defSeqs {
			b.sequences = make([]seq, 0, defSeqs)
		}
		if cap(b.output) < maxCompressedBlockSize {
			b.output = make([]byte, 0, maxCompressedBlockSize)
		}
	}

	if b.coders.mlEnc == nil {
		b.coders.mlEnc = &fseEncoder{}
		b.coders.mlPrev = &fseEncoder{}
		b.coders.ofEnc = &fseEncoder{}
		b.coders.ofPrev = &fseEncoder{}
		b.coders.llEnc = &fseEncoder{}
		b.coders.llPrev = &fseEncoder{}
	}
	b.litEnc = &huff0.Scratch{WantLogLess: 4}
	b.reset(nil)
}

// initNewEncode resets state for a new encoding session.
func (b *blockEnc) initNewEncode() {
	b.recentOffsets = [3]uint32{1, 4, 8}
	b.litEnc.Reuse = huff0.ReusePolicyNone
	b.coders.setPrev(nil, nil, nil)
}

// reset clears the block for reuse, carrying offsets from prev.
func (b *blockEnc) reset(prev *blockEnc) {
	b.extraLits = 0
	b.literals = b.literals[:0]
	b.size = 0
	b.sequences = b.sequences[:0]
	b.output = b.output[:0]
	b.last = false
	if prev != nil {
		b.recentOffsets = prev.prevRecentOffsets
	}
	b.dictLitEnc = nil
}

// pushOffsets saves current offsets as the previous offsets.
func (b *blockEnc) pushOffsets() {
	b.prevRecentOffsets = b.recentOffsets
}

// popOffsets restores offsets from the saved previous values.
func (b *blockEnc) popOffsets() {
	b.recentOffsets = b.prevRecentOffsets
}

// encodeRaw writes a raw (uncompressed) block.
func (b *blockEnc) encodeRaw(a []byte) {
	var bh blockHeader
	bh.setLast(b.last)
	bh.setSize(uint32(len(a)))
	bh.setType(blockTypeRaw)
	b.output = bh.appendTo(b.output[:0])
	b.output = append(b.output, a...)
}

// encodeRawTo writes a raw block to dst without modifying b.output.
func (b *blockEnc) encodeRawTo(dst, src []byte) []byte {
	var bh blockHeader
	bh.setLast(b.last)
	bh.setSize(uint32(len(src)))
	bh.setType(blockTypeRaw)
	dst = bh.appendTo(dst)
	dst = append(dst, src...)
	return dst
}

// encodeLits encodes a literals-only block (no sequences).
func (b *blockEnc) encodeLits(lits []byte, raw bool) error {
	var bh blockHeader
	bh.setLast(b.last)
	bh.setSize(uint32(len(lits)))

	if len(lits) < 8 || (len(lits) < 32 && b.dictLitEnc == nil) || raw {
		bh.setType(blockTypeRaw)
		b.output = bh.appendTo(b.output)
		b.output = append(b.output, lits...)
		return nil
	}

	var (
		out            []byte
		reUsed, single bool
		err            error
	)
	if b.dictLitEnc != nil {
		b.litEnc.TransferCTable(b.dictLitEnc)
		b.litEnc.Reuse = huff0.ReusePolicyAllow
		b.dictLitEnc = nil
	}
	if len(lits) >= 1024 {
		out, reUsed, err = huff0.Compress4X(lits, b.litEnc)
	} else if len(lits) > 16 {
		single = true
		out, reUsed, err = huff0.Compress1X(lits, b.litEnc)
	} else {
		err = huff0.ErrIncompressible
	}
	if err == nil && len(out)+5 > len(lits) {
		var lh literalsHeader
		lh.setSizes(len(out), len(lits), single)
		if len(out)+lh.size() >= len(lits) {
			err = huff0.ErrIncompressible
		}
	}
	switch err {
	case huff0.ErrIncompressible:
		bh.setType(blockTypeRaw)
		b.output = bh.appendTo(b.output)
		b.output = append(b.output, lits...)
		return nil
	case huff0.ErrUseRLE:
		bh.setType(blockTypeRLE)
		b.output = bh.appendTo(b.output)
		b.output = append(b.output, lits[0])
		return nil
	case nil:
	default:
		return err
	}
	b.litEnc.Reuse = huff0.ReusePolicyAllow
	bh.setType(blockTypeCompressed)
	var lh literalsHeader
	if reUsed {
		lh.setType(literalsBlockTreeless)
	} else {
		lh.setType(literalsBlockCompressed)
	}
	lh.setSizes(len(out), len(lits), single)
	bh.setSize(uint32(len(out) + lh.size() + 1))

	b.output = bh.appendTo(b.output)
	b.output = lh.appendTo(b.output)
	b.output = append(b.output, out...)
	b.output = append(b.output, 0)
	return nil
}

// encodeRLE writes an RLE block.
func (b *blockEnc) encodeRLE(val byte, length uint32) {
	var bh blockHeader
	bh.setLast(b.last)
	bh.setSize(length)
	bh.setType(blockTypeRLE)
	b.output = bh.appendTo(b.output)
	b.output = append(b.output, val)
}

// encode compresses a block with Huffman literals and FSE sequences.
func (b *blockEnc) encode(org []byte, raw, rawAllLits bool) error {
	if len(b.sequences) == 0 {
		return b.encodeLits(b.literals, rawAllLits)
	}
	if len(b.sequences) == 1 && len(org) > 0 && len(b.literals) <= 1 {
		seq := b.sequences[0]
		if seq.litLen == uint32(len(b.literals)) && seq.offset-3 == 1 {
			b.encodeRLE(org[0], b.sequences[0].matchLen+zstdMinMatch+seq.litLen)
			return nil
		}
	}

	// We want some difference to at least account for the headers.
	saved := b.size - len(b.literals) - (b.size >> 6)
	if saved < 16 {
		if org == nil {
			return errIncompressible
		}
		b.popOffsets()
		return b.encodeLits(org, rawAllLits)
	}

	var bh blockHeader
	var lh literalsHeader
	bh.setLast(b.last)
	bh.setType(blockTypeCompressed)
	bhOffset := len(b.output)
	b.output = bh.appendTo(b.output)

	var (
		out            []byte
		reUsed, single bool
		err            error
	)
	if b.dictLitEnc != nil {
		b.litEnc.TransferCTable(b.dictLitEnc)
		b.litEnc.Reuse = huff0.ReusePolicyAllow
		b.dictLitEnc = nil
	}
	if len(b.literals) >= 1024 && !raw {
		out, reUsed, err = huff0.Compress4X(b.literals, b.litEnc)
	} else if len(b.literals) > 16 && !raw {
		single = true
		out, reUsed, err = huff0.Compress1X(b.literals, b.litEnc)
	} else {
		err = huff0.ErrIncompressible
	}

	if err == nil && len(out)+5 > len(b.literals) {
		var lh literalsHeader
		lh.setSize(len(b.literals))
		szRaw := lh.size()
		lh.setSizes(len(out), len(b.literals), single)
		szComp := lh.size()
		if len(out)+szComp >= len(b.literals)+szRaw {
			err = huff0.ErrIncompressible
		}
	}
	switch err {
	case huff0.ErrIncompressible:
		lh.setType(literalsBlockRaw)
		lh.setSize(len(b.literals))
		b.output = lh.appendTo(b.output)
		b.output = append(b.output, b.literals...)
	case huff0.ErrUseRLE:
		lh.setType(literalsBlockRLE)
		lh.setSize(len(b.literals))
		b.output = lh.appendTo(b.output)
		b.output = append(b.output, b.literals[0])
	case nil:
		if reUsed {
			lh.setType(literalsBlockTreeless)
		} else {
			lh.setType(literalsBlockCompressed)
		}
		lh.setSizes(len(out), len(b.literals), single)
		b.output = lh.appendTo(b.output)
		b.output = append(b.output, out...)
		b.litEnc.Reuse = huff0.ReusePolicyAllow
	default:
		return err
	}

	switch {
	case len(b.sequences) < 128:
		b.output = append(b.output, uint8(len(b.sequences)))
	case len(b.sequences) < 0x7f00:
		n := len(b.sequences)
		b.output = append(b.output, 128+uint8(n>>8), uint8(n))
	default:
		n := len(b.sequences) - 0x7f00
		b.output = append(b.output, 255, uint8(n), uint8(n>>8))
	}

	b.genCodes()
	llEnc := b.coders.llEnc
	ofEnc := b.coders.ofEnc
	mlEnc := b.coders.mlEnc
	err = llEnc.normalizeCount(len(b.sequences))
	if err != nil {
		return err
	}
	err = ofEnc.normalizeCount(len(b.sequences))
	if err != nil {
		return err
	}
	err = mlEnc.normalizeCount(len(b.sequences))
	if err != nil {
		return err
	}

	chooseComp := func(cur, prev, preDef *fseEncoder) (*fseEncoder, seqCompMode) {
		hist := cur.count[:cur.symbolLen]
		nSize := cur.approxSize(hist) + cur.maxHeaderSize()
		predefSize := preDef.approxSize(hist)
		prevSize := prev.approxSize(hist)

		// Penalty for new encoders to avoid marginal gains.
		nSize = nSize + (nSize+2*8*16)>>4
		switch {
		case predefSize <= prevSize && predefSize <= nSize:
			return preDef, compModePredefined
		case prevSize <= nSize:
			return prev, compModeRepeat
		default:
			return cur, compModeFSE
		}
	}

	var mode uint8
	if llEnc.useRLE {
		mode |= uint8(compModeRLE) << 6
		llEnc.setRLE(b.sequences[0].llCode)
	} else {
		var m seqCompMode
		llEnc, m = chooseComp(llEnc, b.coders.llPrev, &fsePredefEnc[tableLiteralLengths])
		mode |= uint8(m) << 6
	}
	if ofEnc.useRLE {
		mode |= uint8(compModeRLE) << 4
		ofEnc.setRLE(b.sequences[0].ofCode)
	} else {
		var m seqCompMode
		ofEnc, m = chooseComp(ofEnc, b.coders.ofPrev, &fsePredefEnc[tableOffsets])
		mode |= uint8(m) << 4
	}
	if mlEnc.useRLE {
		mode |= uint8(compModeRLE) << 2
		mlEnc.setRLE(b.sequences[0].mlCode)
	} else {
		var m seqCompMode
		mlEnc, m = chooseComp(mlEnc, b.coders.mlPrev, &fsePredefEnc[tableMatchLengths])
		mode |= uint8(m) << 2
	}
	b.output = append(b.output, mode)

	b.output, err = llEnc.writeCount(b.output)
	if err != nil {
		return err
	}
	b.output, err = ofEnc.writeCount(b.output)
	if err != nil {
		return err
	}
	b.output, err = mlEnc.writeCount(b.output)
	if err != nil {
		return err
	}

	wr := &b.wr
	wr.reset(b.output)

	var ll, of, ml cState

	seq := len(b.sequences) - 1
	s := b.sequences[seq]
	llEnc.setBits(llBitsTable[:])
	mlEnc.setBits(mlBitsTable[:])
	ofEnc.setBits(nil)

	llTT, ofTT, mlTT := llEnc.ct.symbolTT[:256], ofEnc.ct.symbolTT[:256], mlEnc.ct.symbolTT[:256]

	llB, ofB, mlB := llTT[s.llCode], ofTT[s.ofCode], mlTT[s.mlCode]
	ll.init(wr, &llEnc.ct, llB)
	of.init(wr, &ofEnc.ct, ofB)
	wr.flush32()
	ml.init(wr, &mlEnc.ct, mlB)

	wr.addBits32NC(s.litLen, llB.outBits)
	wr.addBits32NC(s.matchLen, mlB.outBits)
	wr.flush32()
	wr.addBits32NC(s.offset, ofB.outBits)
	seq--

	for seq >= 0 {
		s = b.sequences[seq]

		ofB := ofTT[s.ofCode]
		wr.flush32()
		nbBitsOut := (uint32(of.state) + ofB.deltaNbBits) >> 16
		dstState := int32(of.state>>(nbBitsOut&15)) + int32(ofB.deltaFindState)
		wr.addBits16NC(of.state, uint8(nbBitsOut))
		of.state = of.stateTable[dstState]

		outBits := ofB.outBits & 31
		extraBits := uint64(s.offset & bitMask32[outBits])
		extraBitsN := outBits

		mlB := mlTT[s.mlCode]
		nbBitsOut = (uint32(ml.state) + mlB.deltaNbBits) >> 16
		dstState = int32(ml.state>>(nbBitsOut&15)) + int32(mlB.deltaFindState)
		wr.addBits16NC(ml.state, uint8(nbBitsOut))
		ml.state = ml.stateTable[dstState]

		outBits = mlB.outBits & 31
		extraBits = extraBits<<outBits | uint64(s.matchLen&bitMask32[outBits])
		extraBitsN += outBits

		llB := llTT[s.llCode]
		nbBitsOut = (uint32(ll.state) + llB.deltaNbBits) >> 16
		dstState = int32(ll.state>>(nbBitsOut&15)) + int32(llB.deltaFindState)
		wr.addBits16NC(ll.state, uint8(nbBitsOut))
		ll.state = ll.stateTable[dstState]

		outBits = llB.outBits & 31
		extraBits = extraBits<<outBits | uint64(s.litLen&bitMask32[outBits])
		extraBitsN += outBits

		wr.flush32()
		wr.addBits64NC(extraBits, extraBitsN)

		seq--
	}
	ml.flush(mlEnc.actualTableLog)
	of.flush(ofEnc.actualTableLog)
	ll.flush(llEnc.actualTableLog)
	wr.close()
	b.output = wr.out

	if len(b.output)-3-bhOffset >= b.size {
		b.output = b.encodeRawTo(b.output[:bhOffset], org)
		b.popOffsets()
		b.litEnc.Reuse = huff0.ReusePolicyNone
		return nil
	}

	bh.setSize(uint32(len(b.output)-bhOffset) - 3)
	_ = bh.appendTo(b.output[bhOffset:bhOffset])
	b.coders.setPrev(llEnc, mlEnc, ofEnc)
	return nil
}

var errIncompressible = errors.New("incompressible")

// genCodes assigns FSE codes to all sequences.
func (b *blockEnc) genCodes() {
	if len(b.sequences) == 0 {
		return
	}
	if len(b.sequences) > math.MaxUint16 {
		panic("can only encode up to 64K sequences")
	}
	llH := b.coders.llEnc.Histogram()
	ofH := b.coders.ofEnc.Histogram()
	mlH := b.coders.mlEnc.Histogram()
	for i := range llH {
		llH[i] = 0
	}
	for i := range ofH {
		ofH[i] = 0
	}
	for i := range mlH {
		mlH[i] = 0
	}

	var llMax, ofMax, mlMax uint8
	for i := range b.sequences {
		seq := &b.sequences[i]
		v := llCode(seq.litLen)
		seq.llCode = v
		llH[v]++
		if v > llMax {
			llMax = v
		}

		v = ofCode(seq.offset)
		seq.ofCode = v
		ofH[v]++
		if v > ofMax {
			ofMax = v
		}

		v = mlCode(seq.matchLen)
		seq.mlCode = v
		mlH[v]++
		if v > mlMax {
			mlMax = v
		}
	}

	b.coders.mlEnc.HistogramFinished(mlMax, int(slices.Max(mlH[:mlMax+1])))
	b.coders.ofEnc.HistogramFinished(ofMax, int(slices.Max(ofH[:ofMax+1])))
	b.coders.llEnc.HistogramFinished(llMax, int(slices.Max(llH[:llMax+1])))
}
