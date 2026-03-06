// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"errors"
	"fmt"
	"math"
)

// FSE encoder size limits.
const (
	maxEncTableLog    = 8
	maxEncTablesize   = 1 << maxTableLog
	maxEncTableMask   = (1 << maxTableLog) - 1
	minEncTablelog    = 5
	maxEncSymbolValue = maxMatchLengthSymbol
)

// fseEncoder builds and holds an FSE compression table.
type fseEncoder struct {
	symbolLen      uint16
	actualTableLog uint8
	ct             cTable
	maxCount       int
	zeroBits       bool
	clearCount     bool
	useRLE         bool
	preDefined     bool
	reUsed         bool
	rleVal         uint8
	maxBits        uint8
	count          [256]uint32
	norm           [256]int16
}

// cTable holds the FSE compression table arrays.
type cTable struct {
	tableSymbol []byte
	stateTable  []uint16
	symbolTT    []symbolTransform
}

// symbolTransform holds the per-symbol encoding parameters for FSE.
type symbolTransform struct {
	deltaNbBits    uint32
	deltaFindState int16
	outBits        uint8
}

// Histogram returns the symbol frequency counts.
func (s *fseEncoder) Histogram() *[256]uint32 {
	return &s.count
}

// HistogramFinished finalizes histogram state after counting.
func (s *fseEncoder) HistogramFinished(maxSymbol uint8, maxCount int) {
	s.maxCount = maxCount
	s.symbolLen = uint16(maxSymbol) + 1
	s.clearCount = maxCount != 0
}

// allocCtable ensures the compression table slices are allocated.
func (s *fseEncoder) allocCtable() {
	tableSize := 1 << s.actualTableLog
	if cap(s.ct.tableSymbol) < tableSize {
		s.ct.tableSymbol = make([]byte, tableSize)
	}
	s.ct.tableSymbol = s.ct.tableSymbol[:tableSize]

	ctSize := tableSize
	if cap(s.ct.stateTable) < ctSize {
		s.ct.stateTable = make([]uint16, ctSize)
	}
	s.ct.stateTable = s.ct.stateTable[:ctSize]

	if cap(s.ct.symbolTT) < 256 {
		s.ct.symbolTT = make([]symbolTransform, 256)
	}
	s.ct.symbolTT = s.ct.symbolTT[:256]
}

// buildCTable builds the compression table from normalized counts.
func (s *fseEncoder) buildCTable() error {
	tableSize := uint32(1 << s.actualTableLog)
	highThreshold := tableSize - 1
	var cumul [256]int16

	s.allocCtable()
	tableSymbol := s.ct.tableSymbol[:tableSize]
	{
		cumul[0] = 0
		for ui, v := range s.norm[:s.symbolLen-1] {
			u := byte(ui)
			if v == -1 {
				cumul[u+1] = cumul[u] + 1
				tableSymbol[highThreshold] = u
				highThreshold--
			} else {
				cumul[u+1] = cumul[u] + v
			}
		}
		u := int(s.symbolLen - 1)
		v := s.norm[s.symbolLen-1]
		if v == -1 {
			cumul[u+1] = cumul[u] + 1
			tableSymbol[highThreshold] = byte(u)
			highThreshold--
		} else {
			cumul[u+1] = cumul[u] + v
		}
		if uint32(cumul[s.symbolLen]) != tableSize {
			return fmt.Errorf("internal error: expected cumul[s.symbolLen] (%d) == tableSize (%d)", cumul[s.symbolLen], tableSize)
		}
		cumul[s.symbolLen] = int16(tableSize) + 1
	}
	s.zeroBits = false
	{
		step := tableStep(tableSize)
		tableMask := tableSize - 1
		var position uint32
		largeLimit := int16(1 << (s.actualTableLog - 1))
		for ui, v := range s.norm[:s.symbolLen] {
			symbol := byte(ui)
			if v > largeLimit {
				s.zeroBits = true
			}
			for range v {
				tableSymbol[position] = symbol
				position = (position + step) & tableMask
				for position > highThreshold {
					position = (position + step) & tableMask
				}
			}
		}
		if position != 0 {
			return errors.New("position!=0")
		}
	}

	table := s.ct.stateTable
	{
		tsi := int(tableSize)
		for u, v := range tableSymbol {
			table[cumul[v]] = uint16(tsi + u)
			cumul[v]++
		}
	}

	{
		total := int16(0)
		symbolTT := s.ct.symbolTT[:s.symbolLen]
		tableLog := s.actualTableLog
		tl := (uint32(tableLog) << 16) - (1 << tableLog)
		for i, v := range s.norm[:s.symbolLen] {
			switch v {
			case 0:
			case -1, 1:
				symbolTT[i].deltaNbBits = tl
				symbolTT[i].deltaFindState = total - 1
				total++
			default:
				maxBitsOut := uint32(tableLog) - highBit(uint32(v-1))
				minStatePlus := uint32(v) << maxBitsOut
				symbolTT[i].deltaNbBits = (maxBitsOut << 16) - minStatePlus
				symbolTT[i].deltaFindState = total - v
				total += v
			}
		}
		if total != int16(tableSize) {
			return fmt.Errorf("total mismatch %d (got) != %d (want)", total, tableSize)
		}
	}
	return nil
}

// rtbTable contains thresholds for the "rest to beat" normalization heuristic.
var rtbTable = [...]uint32{0, 473195, 504333, 520860, 550000, 700000, 750000, 830000}

// setRLE configures the encoder for a single repeated symbol.
func (s *fseEncoder) setRLE(val byte) {
	s.allocCtable()
	s.actualTableLog = 0
	s.ct.stateTable = s.ct.stateTable[:1]
	s.ct.symbolTT[val] = symbolTransform{
		deltaFindState: 0,
		deltaNbBits:    0,
	}
	s.rleVal = val
	s.useRLE = true
}

// setBits assigns output bit counts per symbol from a transform table.
func (s *fseEncoder) setBits(transform []byte) {
	if s.reUsed || s.preDefined {
		return
	}
	if s.useRLE {
		if transform == nil {
			s.ct.symbolTT[s.rleVal].outBits = s.rleVal
			s.maxBits = s.rleVal
			return
		}
		s.maxBits = transform[s.rleVal]
		s.ct.symbolTT[s.rleVal].outBits = s.maxBits
		return
	}
	if transform == nil {
		for i := range s.ct.symbolTT[:s.symbolLen] {
			s.ct.symbolTT[i].outBits = uint8(i)
		}
		s.maxBits = uint8(s.symbolLen - 1)
		return
	}
	s.maxBits = 0
	for i, v := range transform[:s.symbolLen] {
		s.ct.symbolTT[i].outBits = v
		if v > s.maxBits {
			s.maxBits = v
		}
	}
}

// normalizeCount normalizes symbol counts to sum to 1<<tableLog.
func (s *fseEncoder) normalizeCount(length int) error {
	if s.reUsed {
		return nil
	}
	s.optimalTableLog(length)
	var (
		tableLog          = s.actualTableLog
		scale             = 62 - uint64(tableLog)
		step              = (1 << 62) / uint64(length)
		vStep             = uint64(1) << (scale - 20)
		stillToDistribute = int16(1 << tableLog)
		largest           int
		largestP          int16
		lowThreshold      = (uint32)(length >> tableLog)
	)
	if s.maxCount == length {
		s.useRLE = true
		return nil
	}
	s.useRLE = false
	for i, cnt := range s.count[:s.symbolLen] {
		if cnt == 0 {
			s.norm[i] = 0
			continue
		}
		if cnt <= lowThreshold {
			s.norm[i] = -1
			stillToDistribute--
		} else {
			proba := (int16)((uint64(cnt) * step) >> scale)
			if proba < 8 {
				restToBeat := vStep * uint64(rtbTable[proba])
				v := uint64(cnt)*step - (uint64(proba) << scale)
				if v > restToBeat {
					proba++
				}
			}
			if proba > largestP {
				largestP = proba
				largest = i
			}
			s.norm[i] = proba
			stillToDistribute -= proba
		}
	}

	if -stillToDistribute >= (s.norm[largest] >> 1) {
		err := s.normalizeCount2(length)
		if err != nil {
			return err
		}
		return s.buildCTable()
	}
	s.norm[largest] += stillToDistribute
	return s.buildCTable()
}

// normalizeCount2 uses a secondary normalization when primary is too skewed.
func (s *fseEncoder) normalizeCount2(length int) error {
	const notYetAssigned = -2
	var (
		distributed  uint32
		total        = uint32(length)
		tableLog     = s.actualTableLog
		lowThreshold = total >> tableLog
		lowOne       = (total * 3) >> (tableLog + 1)
	)
	for i, cnt := range s.count[:s.symbolLen] {
		if cnt == 0 {
			s.norm[i] = 0
			continue
		}
		if cnt <= lowThreshold {
			s.norm[i] = -1
			distributed++
			total -= cnt
			continue
		}
		if cnt <= lowOne {
			s.norm[i] = 1
			distributed++
			total -= cnt
			continue
		}
		s.norm[i] = notYetAssigned
	}
	toDistribute := (1 << tableLog) - distributed

	if (total / toDistribute) > lowOne {
		lowOne = (total * 3) / (toDistribute * 2)
		for i, cnt := range s.count[:s.symbolLen] {
			if (s.norm[i] == notYetAssigned) && (cnt <= lowOne) {
				s.norm[i] = 1
				distributed++
				total -= cnt
				continue
			}
		}
		toDistribute = (1 << tableLog) - distributed
	}
	if distributed == uint32(s.symbolLen)+1 {
		var maxV int
		var maxC uint32
		for i, cnt := range s.count[:s.symbolLen] {
			if cnt > maxC {
				maxV = i
				maxC = cnt
			}
		}
		s.norm[maxV] += int16(toDistribute)
		return nil
	}

	if total == 0 {
		for i := uint32(0); toDistribute > 0; i = (i + 1) % (uint32(s.symbolLen)) {
			if s.norm[i] > 0 {
				toDistribute--
				s.norm[i]++
			}
		}
		return nil
	}

	var (
		vStepLog = 62 - uint64(tableLog)
		mid      = uint64((1 << (vStepLog - 1)) - 1)
		rStep    = (((1 << vStepLog) * uint64(toDistribute)) + mid) / uint64(total)
		tmpTotal = mid
	)
	for i, cnt := range s.count[:s.symbolLen] {
		if s.norm[i] == notYetAssigned {
			var (
				end    = tmpTotal + uint64(cnt)*rStep
				sStart = uint32(tmpTotal >> vStepLog)
				sEnd   = uint32(end >> vStepLog)
				weight = sEnd - sStart
			)
			if weight < 1 {
				return errors.New("weight < 1")
			}
			s.norm[i] = int16(weight)
			tmpTotal = end
		}
	}
	return nil
}

// optimalTableLog computes the best table log for the given input length.
func (s *fseEncoder) optimalTableLog(length int) {
	tableLog := uint8(maxEncTableLog)
	minBitsSrc := highBit(uint32(length)) + 1
	minBitsSymbols := highBit(uint32(s.symbolLen-1)) + 2
	minBits := uint8(minBitsSymbols)
	if minBitsSrc < minBitsSymbols {
		minBits = uint8(minBitsSrc)
	}
	maxBitsSrc := uint8(highBit(uint32(length-1))) - 2
	if maxBitsSrc < tableLog {
		tableLog = maxBitsSrc
	}
	if minBits > tableLog {
		tableLog = minBits
	}
	if tableLog < minEncTablelog {
		tableLog = minEncTablelog
	}
	if tableLog > maxEncTableLog {
		tableLog = maxEncTableLog
	}
	s.actualTableLog = tableLog
}

// writeCount serializes the normalized count distribution to out.
func (s *fseEncoder) writeCount(out []byte) ([]byte, error) {
	if s.useRLE {
		return append(out, s.rleVal), nil
	}
	if s.preDefined || s.reUsed {
		return out, nil
	}

	var (
		tableLog      = s.actualTableLog
		tableSize     = 1 << tableLog
		previous0     bool
		charnum       uint16
		maxHeaderSize = ((int(s.symbolLen) * int(tableLog)) >> 3) + 3 + 2
		bitStream     = uint32(tableLog - minEncTablelog)
		bitCount      = uint(4)
		remaining     = int16(tableSize + 1)
		threshold     = int16(tableSize)
		nbBits        = uint(tableLog + 1)
		outP          = len(out)
	)
	if cap(out) < outP+maxHeaderSize {
		out = append(out, make([]byte, maxHeaderSize*3)...)
		out = out[:len(out)-maxHeaderSize*3]
	}
	out = out[:outP+maxHeaderSize]

	for remaining > 1 {
		if previous0 {
			start := charnum
			for s.norm[charnum] == 0 {
				charnum++
			}
			for charnum >= start+24 {
				start += 24
				bitStream += uint32(0xFFFF) << bitCount
				out[outP] = byte(bitStream)
				out[outP+1] = byte(bitStream >> 8)
				outP += 2
				bitStream >>= 16
			}
			for charnum >= start+3 {
				start += 3
				bitStream += 3 << bitCount
				bitCount += 2
			}
			bitStream += uint32(charnum-start) << bitCount
			bitCount += 2
			if bitCount > 16 {
				out[outP] = byte(bitStream)
				out[outP+1] = byte(bitStream >> 8)
				outP += 2
				bitStream >>= 16
				bitCount -= 16
			}
		}

		count := s.norm[charnum]
		charnum++
		max := (2*threshold - 1) - remaining
		if count < 0 {
			remaining += count
		} else {
			remaining -= count
		}
		count++
		if count >= threshold {
			count += max
		}
		bitStream += uint32(count) << bitCount
		bitCount += nbBits
		if count < max {
			bitCount--
		}

		previous0 = count == 1
		if remaining < 1 {
			return nil, errors.New("internal error: remaining < 1")
		}
		for remaining < threshold {
			nbBits--
			threshold >>= 1
		}

		if bitCount > 16 {
			out[outP] = byte(bitStream)
			out[outP+1] = byte(bitStream >> 8)
			outP += 2
			bitStream >>= 16
			bitCount -= 16
		}
	}

	if outP+2 > len(out) {
		return nil, fmt.Errorf("internal error: %d > %d", outP+2, len(out))
	}
	out[outP] = byte(bitStream)
	out[outP+1] = byte(bitStream >> 8)
	outP += int((bitCount + 7) / 8)

	if charnum > s.symbolLen {
		return nil, errors.New("internal error: charnum > s.symbolLen")
	}
	return out[:outP], nil
}

// bitCost returns the fractional bit cost of encoding a symbol.
func (s *fseEncoder) bitCost(symbolValue uint8, accuracyLog uint32) uint32 {
	minNbBits := s.ct.symbolTT[symbolValue].deltaNbBits >> 16
	threshold := (minNbBits + 1) << 16
	tableSize := uint32(1) << s.actualTableLog
	deltaFromThreshold := threshold - (s.ct.symbolTT[symbolValue].deltaNbBits + tableSize)
	normalizedDeltaFromThreshold := (deltaFromThreshold << accuracyLog) >> s.actualTableLog
	bitMultiplier := uint32(1) << accuracyLog
	return (minNbBits+1)*bitMultiplier - normalizedDeltaFromThreshold
}

// approxSize estimates the compressed size in bits for the given histogram.
func (s *fseEncoder) approxSize(hist []uint32) uint32 {
	if int(s.symbolLen) < len(hist) {
		return math.MaxUint32
	}
	if s.useRLE {
		return math.MaxUint32
	}
	const kAccuracyLog = 8
	badCost := (uint32(s.actualTableLog) + 1) << kAccuracyLog
	var cost uint32
	for i, v := range hist {
		if v == 0 {
			continue
		}
		if s.norm[i] == 0 {
			return math.MaxUint32
		}
		bitCost := s.bitCost(uint8(i), kAccuracyLog)
		if bitCost > badCost {
			return math.MaxUint32
		}
		cost += v * bitCost
	}
	return cost >> kAccuracyLog
}

// maxHeaderSize returns the maximum header size in bits.
func (s *fseEncoder) maxHeaderSize() uint32 {
	if s.preDefined {
		return 0
	}
	if s.useRLE {
		return 8
	}
	return (((uint32(s.symbolLen) * uint32(s.actualTableLog)) >> 3) + 3) * 8
}

// cState tracks the current state during FSE-driven sequence encoding.
type cState struct {
	bw         *bitWriter
	stateTable []uint16
	state      uint16
}

// init initializes the compression state for the first symbol.
func (c *cState) init(bw *bitWriter, ct *cTable, first symbolTransform) {
	c.bw = bw
	c.stateTable = ct.stateTable
	if len(c.stateTable) == 1 {
		c.stateTable[0] = uint16(0)
		c.state = 0
		return
	}
	nbBitsOut := (first.deltaNbBits + (1 << 15)) >> 16
	im := int32((nbBitsOut << 16) - first.deltaNbBits)
	lu := (im >> nbBitsOut) + int32(first.deltaFindState)
	c.state = c.stateTable[lu]
}

// flush writes the final state bits to the bitstream.
func (c *cState) flush(tableLog uint8) {
	c.bw.flush32()
	c.bw.addBits16NC(c.state, tableLog)
}
