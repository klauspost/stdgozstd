// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package huff0

import (
	"errors"
	"fmt"
	"math/bits"
	"sync"

	"github.com/klauspost/stdgozstd/internal/fse"
)

const (
	maxSymbolValue = 255

	tableLogMax     = 11
	tableLogDefault = 11
	minTablelog     = 5
	huffNodesLen    = 512

	BlockSizeMax = 1<<18 - 1
)

var (
	ErrIncompressible         = errors.New("input is not compressible")
	ErrUseRLE                 = errors.New("input is single value repeated")
	ErrTooBig                 = errors.New("input too big")
	ErrMaxDecodedSizeExceeded = errors.New("maximum output size exceeded")
)

type ReusePolicy uint8

const (
	ReusePolicyAllow ReusePolicy = iota
	ReusePolicyPrefer
	ReusePolicyNone
	ReusePolicyMust
)

type Scratch struct {
	count [maxSymbolValue + 1]uint32

	Out            []byte
	OutTable       []byte
	OutData        []byte
	MaxDecodedSize int

	srcLen int

	MaxSymbolValue uint8
	TableLog       uint8
	Reuse          ReusePolicy
	WantLogLess    uint8

	symbolLen      uint16
	maxCount       int
	clearCount     bool
	actualTableLog uint8
	prevTableLog   uint8
	prevTable      cTable
	cTable         cTable
	dt             dTable
	nodes          []nodeElt
	fse            *fse.Scratch
	decPool        sync.Pool
	huffWeight     [maxSymbolValue + 1]byte
}

func (s *Scratch) TransferCTable(src *Scratch) {
	if cap(s.prevTable) < len(src.prevTable) {
		s.prevTable = make(cTable, 0, maxSymbolValue+1)
	}
	s.prevTable = s.prevTable[:len(src.prevTable)]
	copy(s.prevTable, src.prevTable)
	s.prevTableLog = src.prevTableLog
}

func (s *Scratch) prepare(in []byte) (*Scratch, error) {
	if len(in) > BlockSizeMax {
		return nil, ErrTooBig
	}
	if s == nil {
		s = &Scratch{}
	}
	if s.MaxSymbolValue == 0 {
		s.MaxSymbolValue = maxSymbolValue
	}
	if s.TableLog == 0 {
		s.TableLog = tableLogDefault
	}
	if s.TableLog > tableLogMax || s.TableLog < minTablelog {
		return nil, fmt.Errorf(" invalid tableLog %d (%d -> %d)", s.TableLog, minTablelog, tableLogMax)
	}
	if s.MaxDecodedSize <= 0 || s.MaxDecodedSize > BlockSizeMax {
		s.MaxDecodedSize = BlockSizeMax
	}
	if s.clearCount && s.maxCount == 0 {
		for i := range s.count {
			s.count[i] = 0
		}
		s.clearCount = false
	}
	if cap(s.Out) == 0 {
		s.Out = make([]byte, 0, len(in))
	}
	s.Out = s.Out[:0]

	s.OutTable = nil
	s.OutData = nil
	if cap(s.nodes) < huffNodesLen+1 {
		s.nodes = make([]nodeElt, 0, huffNodesLen+1)
	}
	s.nodes = s.nodes[:0]
	if s.fse == nil {
		s.fse = &fse.Scratch{}
	}
	s.srcLen = len(in)

	return s, nil
}

type cTable []cTableEntry

func (c cTable) write(s *Scratch) error {
	var (
		bitsToWeight   [tableLogMax + 1]byte
		huffLog        = s.actualTableLog
		maxSymbolValue = uint8(s.symbolLen - 1)
		huffWeight     = s.huffWeight[:256]
	)
	const maxFSETableLog = 6

	bitsToWeight[0] = 0
	for n := uint8(1); n < huffLog+1; n++ {
		bitsToWeight[n] = huffLog + 1 - n
	}

	hist := s.fse.Histogram()
	hist = hist[:256]
	for i := range hist[:16] {
		hist[i] = 0
	}
	for n := range maxSymbolValue {
		v := bitsToWeight[c[n].nBits] & 15
		huffWeight[n] = v
		hist[v]++
	}

	if maxSymbolValue >= 2 {
		huffMaxCnt := uint32(0)
		huffMax := uint8(0)
		for i, v := range hist[:16] {
			if v == 0 {
				continue
			}
			huffMax = byte(i)
			if v > huffMaxCnt {
				huffMaxCnt = v
			}
		}
		s.fse.HistogramFinished(huffMax, int(huffMaxCnt))
		s.fse.TableLog = maxFSETableLog
		b, err := fse.Compress(huffWeight[:maxSymbolValue], s.fse)
		if err == nil && len(b) < int(s.symbolLen>>1) {
			s.Out = append(s.Out, uint8(len(b)))
			s.Out = append(s.Out, b...)
			return nil
		}
	}
	if maxSymbolValue > (256 - 128) {
		return ErrIncompressible
	}
	op := s.Out
	op = append(op, 128|(maxSymbolValue-1))
	huffWeight[maxSymbolValue] = 0
	for n := uint16(0); n < uint16(maxSymbolValue); n += 2 {
		op = append(op, (huffWeight[n]<<4)|huffWeight[n+1])
	}
	s.Out = op
	return nil
}

func (c cTable) estTableSize(s *Scratch) (sz int, err error) {
	var (
		bitsToWeight   [tableLogMax + 1]byte
		huffLog        = s.actualTableLog
		maxSymbolValue = uint8(s.symbolLen - 1)
		huffWeight     = s.huffWeight[:256]
	)
	const maxFSETableLog = 6

	bitsToWeight[0] = 0
	for n := uint8(1); n < huffLog+1; n++ {
		bitsToWeight[n] = huffLog + 1 - n
	}

	hist := s.fse.Histogram()
	hist = hist[:256]
	for i := range hist[:16] {
		hist[i] = 0
	}
	for n := range maxSymbolValue {
		v := bitsToWeight[c[n].nBits] & 15
		huffWeight[n] = v
		hist[v]++
	}

	if maxSymbolValue >= 2 {
		huffMaxCnt := uint32(0)
		huffMax := uint8(0)
		for i, v := range hist[:16] {
			if v == 0 {
				continue
			}
			huffMax = byte(i)
			if v > huffMaxCnt {
				huffMaxCnt = v
			}
		}
		s.fse.HistogramFinished(huffMax, int(huffMaxCnt))
		s.fse.TableLog = maxFSETableLog
		b, err := fse.Compress(huffWeight[:maxSymbolValue], s.fse)
		if err == nil && len(b) < int(s.symbolLen>>1) {
			sz += 1 + len(b)
			return sz, nil
		}
	}
	if maxSymbolValue > (256 - 128) {
		return 0, ErrIncompressible
	}
	sz += 1 + int(maxSymbolValue/2)
	return sz, nil
}

func (c cTable) estimateSize(hist []uint32) int {
	nbBits := uint32(7)
	for i, v := range c[:len(hist)] {
		nbBits += uint32(v.nBits) * hist[i]
	}
	return int(nbBits >> 3)
}

func highBit32(val uint32) (n uint32) {
	return uint32(bits.Len32(val) - 1)
}

func EstimateSizes(in []byte, s *Scratch) (tableSz, dataSz, reuseSz int, err error) {
	s, err = s.prepare(in)
	if err != nil {
		return 0, 0, 0, err
	}

	reuseSz = -1
	maxCount := s.maxCount
	var canReuse bool
	if maxCount == 0 {
		maxCount, canReuse = s.countSimple(in)
	} else {
		canReuse = s.canUseTable(s.prevTable)
	}

	wantSize := len(in)
	if s.WantLogLess > 0 {
		wantSize -= wantSize >> s.WantLogLess
	}
	_ = wantSize

	s.clearCount = true
	s.maxCount = 0
	if maxCount >= len(in) {
		if maxCount > len(in) {
			return 0, 0, 0, fmt.Errorf("maxCount (%d) > length (%d)", maxCount, len(in))
		}
		if len(in) == 1 {
			return 0, 0, 0, ErrIncompressible
		}
		return 0, 0, 0, ErrUseRLE
	}
	if maxCount == 1 || maxCount < (len(in)>>7) {
		return 0, 0, 0, ErrIncompressible
	}

	err = s.buildCTable()
	if err != nil {
		return 0, 0, 0, err
	}

	tableSz, err = s.cTable.estTableSize(s)
	if err != nil {
		return 0, 0, 0, err
	}
	if canReuse {
		reuseSz = s.prevTable.estimateSize(s.count[:s.symbolLen])
	}
	dataSz = s.cTable.estimateSize(s.count[:s.symbolLen])

	return tableSz, dataSz, reuseSz, nil
}

func (s *Scratch) countSimple(in []byte) (max int, reuse bool) {
	reuse = true
	_ = s.count
	for _, v := range in {
		s.count[v]++
	}
	m := uint32(0)
	if len(s.prevTable) > 0 {
		for i, v := range s.count[:] {
			if v == 0 {
				continue
			}
			if v > m {
				m = v
			}
			s.symbolLen = uint16(i) + 1
			if i >= len(s.prevTable) {
				reuse = false
			} else if s.prevTable[i].nBits == 0 {
				reuse = false
			}
		}
		return int(m), reuse
	}
	for i, v := range s.count[:] {
		if v == 0 {
			continue
		}
		if v > m {
			m = v
		}
		s.symbolLen = uint16(i) + 1
	}
	return int(m), false
}

func (s *Scratch) canUseTable(c cTable) bool {
	if len(c) < int(s.symbolLen) {
		return false
	}
	for i, v := range s.count[:s.symbolLen] {
		if v != 0 && c[i].nBits == 0 {
			return false
		}
	}
	return true
}

func (s *Scratch) minTableLog() uint8 {
	minBitsSrc := highBit32(uint32(s.srcLen)) + 1
	minBitsSymbols := highBit32(uint32(s.symbolLen-1)) + 2
	if minBitsSrc < minBitsSymbols {
		return uint8(minBitsSrc)
	}
	return uint8(minBitsSymbols)
}

func (s *Scratch) optimalTableLog() {
	tableLog := s.TableLog
	minBits := s.minTableLog()
	maxBitsSrc := uint8(highBit32(uint32(s.srcLen-1))) - 1
	if maxBitsSrc < tableLog {
		tableLog = maxBitsSrc
	}
	if minBits > tableLog {
		tableLog = minBits
	}
	if tableLog < minTablelog {
		tableLog = minTablelog
	}
	if tableLog > tableLogMax {
		tableLog = tableLogMax
	}
	s.actualTableLog = tableLog
}

type cTableEntry struct {
	val   uint16
	nBits uint8
}

const huffNodesMask = huffNodesLen - 1

func (s *Scratch) buildCTable() error {
	s.optimalTableLog()
	s.huffSort()
	if cap(s.cTable) < maxSymbolValue+1 {
		s.cTable = make([]cTableEntry, s.symbolLen, maxSymbolValue+1)
	} else {
		s.cTable = s.cTable[:s.symbolLen]
		for i := range s.cTable {
			s.cTable[i] = cTableEntry{}
		}
	}

	var startNode = int16(s.symbolLen)
	nonNullRank := s.symbolLen - 1

	nodeNb := startNode
	huffNode := s.nodes[1 : huffNodesLen+1]
	huffNode0 := s.nodes[0 : huffNodesLen+1]

	for huffNode[nonNullRank].count() == 0 {
		nonNullRank--
	}

	lowS := int16(nonNullRank)
	nodeRoot := nodeNb + lowS - 1
	lowN := nodeNb
	huffNode[nodeNb].setCount(huffNode[lowS].count() + huffNode[lowS-1].count())
	huffNode[lowS].setParent(nodeNb)
	huffNode[lowS-1].setParent(nodeNb)
	nodeNb++
	lowS -= 2
	for n := nodeNb; n <= nodeRoot; n++ {
		huffNode[n].setCount(1 << 30)
	}
	huffNode0[0].setCount(1 << 31)

	for nodeNb <= nodeRoot {
		var n1, n2 int16
		if huffNode0[lowS+1].count() < huffNode0[lowN+1].count() {
			n1 = lowS
			lowS--
		} else {
			n1 = lowN
			lowN++
		}
		if huffNode0[lowS+1].count() < huffNode0[lowN+1].count() {
			n2 = lowS
			lowS--
		} else {
			n2 = lowN
			lowN++
		}

		huffNode[nodeNb].setCount(huffNode0[n1+1].count() + huffNode0[n2+1].count())
		huffNode0[n1+1].setParent(nodeNb)
		huffNode0[n2+1].setParent(nodeNb)
		nodeNb++
	}

	huffNode[nodeRoot].setNbBits(0)
	for n := nodeRoot - 1; n >= startNode; n-- {
		huffNode[n].setNbBits(huffNode[huffNode[n].parent()].nbBits() + 1)
	}
	for n := uint16(0); n <= nonNullRank; n++ {
		huffNode[n].setNbBits(huffNode[huffNode[n].parent()].nbBits() + 1)
	}
	s.actualTableLog = s.setMaxHeight(int(nonNullRank))
	maxNbBits := s.actualTableLog

	if maxNbBits > tableLogMax {
		return fmt.Errorf("internal error: maxNbBits (%d) > tableLogMax (%d)", maxNbBits, tableLogMax)
	}
	var nbPerRank [tableLogMax + 1]uint16
	var valPerRank [16]uint16
	for _, v := range huffNode[:nonNullRank+1] {
		nbPerRank[v.nbBits()]++
	}
	{
		min := uint16(0)
		for n := maxNbBits; n > 0; n-- {
			valPerRank[n] = min
			min += nbPerRank[n]
			min >>= 1
		}
	}

	for _, v := range huffNode[:nonNullRank+1] {
		s.cTable[v.symbol()].nBits = v.nbBits()
	}

	t := s.cTable[:s.symbolLen]
	for n, val := range t {
		nbits := val.nBits & 15
		v := valPerRank[nbits]
		t[n].val = v
		valPerRank[nbits] = v + 1
	}

	return nil
}

func (s *Scratch) huffSort() {
	type rankPos struct {
		base    uint32
		current uint32
	}

	nodes := s.nodes[:huffNodesLen+1]
	s.nodes = nodes
	nodes = nodes[1 : huffNodesLen+1]

	var rank [32]rankPos
	for _, v := range s.count[:s.symbolLen] {
		r := highBit32(v+1) & 31
		rank[r].base++
	}
	const maxBitLength = 18 + 1
	for n := maxBitLength; n > 0; n-- {
		rank[n-1].base += rank[n].base
	}
	for n := range rank[:maxBitLength] {
		rank[n].current = rank[n].base
	}
	for n, c := range s.count[:s.symbolLen] {
		r := (highBit32(c+1) + 1) & 31
		pos := rank[r].current
		rank[r].current++
		prev := nodes[(pos-1)&huffNodesMask]
		for pos > rank[r].base && c > prev.count() {
			nodes[pos&huffNodesMask] = prev
			pos--
			prev = nodes[(pos-1)&huffNodesMask]
		}
		nodes[pos&huffNodesMask] = makeNodeElt(c, byte(n))
	}
}

func (s *Scratch) setMaxHeight(lastNonNull int) uint8 {
	maxNbBits := s.actualTableLog
	huffNode := s.nodes[1 : huffNodesLen+1]

	largestBits := huffNode[lastNonNull].nbBits()

	if largestBits <= maxNbBits {
		return largestBits
	}
	totalCost := int(0)
	baseCost := int(1) << (largestBits - maxNbBits)
	n := uint32(lastNonNull)

	for huffNode[n].nbBits() > maxNbBits {
		totalCost += baseCost - (1 << (largestBits - huffNode[n].nbBits()))
		huffNode[n].setNbBits(maxNbBits)
		n--
	}

	for huffNode[n].nbBits() == maxNbBits {
		n--
	}

	totalCost >>= largestBits - maxNbBits

	{
		const noSymbol = 0xF0F0F0F0
		var rankLast [tableLogMax + 2]uint32

		for i := range rankLast[:] {
			rankLast[i] = noSymbol
		}

		{
			currentNbBits := maxNbBits
			for pos := int(n); pos >= 0; pos-- {
				if huffNode[pos].nbBits() >= currentNbBits {
					continue
				}
				currentNbBits = huffNode[pos].nbBits()
				rankLast[maxNbBits-currentNbBits] = uint32(pos)
			}
		}

		for totalCost > 0 {
			nBitsToDecrease := uint8(highBit32(uint32(totalCost))) + 1

			for ; nBitsToDecrease > 1; nBitsToDecrease-- {
				highPos := rankLast[nBitsToDecrease]
				lowPos := rankLast[nBitsToDecrease-1]
				if highPos == noSymbol {
					continue
				}
				if lowPos == noSymbol {
					break
				}
				highTotal := huffNode[highPos].count()
				lowTotal := 2 * huffNode[lowPos].count()
				if highTotal <= lowTotal {
					break
				}
			}
			for (nBitsToDecrease <= tableLogMax) && (rankLast[nBitsToDecrease] == noSymbol) {
				nBitsToDecrease++
			}
			totalCost -= 1 << (nBitsToDecrease - 1)
			if rankLast[nBitsToDecrease-1] == noSymbol {
				rankLast[nBitsToDecrease-1] = rankLast[nBitsToDecrease]
			}
			huffNode[rankLast[nBitsToDecrease]].setNbBits(1 +
				huffNode[rankLast[nBitsToDecrease]].nbBits())
			if rankLast[nBitsToDecrease] == 0 {
				rankLast[nBitsToDecrease] = noSymbol
			} else {
				rankLast[nBitsToDecrease]--
				if huffNode[rankLast[nBitsToDecrease]].nbBits() != maxNbBits-nBitsToDecrease {
					rankLast[nBitsToDecrease] = noSymbol
				}
			}
		}

		for totalCost < 0 {
			if rankLast[1] == noSymbol {
				for huffNode[n].nbBits() == maxNbBits {
					n--
				}
				huffNode[n+1].setNbBits(huffNode[n+1].nbBits() - 1)
				rankLast[1] = n + 1
				totalCost++
				continue
			}
			huffNode[rankLast[1]+1].setNbBits(huffNode[rankLast[1]+1].nbBits() - 1)
			rankLast[1]++
			totalCost++
		}
	}
	return maxNbBits
}

type nodeElt uint64

func makeNodeElt(count uint32, symbol byte) nodeElt {
	return nodeElt(count) | nodeElt(symbol)<<48
}

func (e *nodeElt) count() uint32  { return uint32(*e) }
func (e *nodeElt) parent() uint16 { return uint16(*e >> 32) }
func (e *nodeElt) symbol() byte   { return byte(*e >> 48) }
func (e *nodeElt) nbBits() uint8  { return uint8(*e >> 56) }

func (e *nodeElt) setCount(c uint32) { *e = (*e)&0xffffffff00000000 | nodeElt(c) }
func (e *nodeElt) setParent(p int16) { *e = (*e)&0xffff0000ffffffff | nodeElt(uint16(p))<<32 }
func (e *nodeElt) setNbBits(n uint8) { *e = (*e)&0x00ffffffffffffff | nodeElt(n)<<56 }
