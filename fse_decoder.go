// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

// tablelogAbsoluteMax is the maximum FSE table log allowed by the zstd spec.
const (
	tablelogAbsoluteMax = 9
)

// FSE decoder size limits derived from tablelogAbsoluteMax.
const (
	maxMemoryUsage = tablelogAbsoluteMax + 2
	maxTableLog    = maxMemoryUsage - 2
	maxTablesize   = 1 << maxTableLog
	maxTableMask   = (1 << maxTableLog) - 1
	minTablelog    = 5
	maxSymbolValue = 255
)

// fseDecoder holds a decoded FSE table for sequence decompression.
type fseDecoder struct {
	dt             [maxTablesize]decSymbol
	symbolLen      uint16
	actualTableLog uint8
	maxBits        uint8
	stateTable     [256]uint16
	norm           [maxSymbolValue + 1]int16
	preDefined     bool
}

// readNCount reads the normalized count distribution from b.
func (s *fseDecoder) readNCount(b *byteReader, maxSymbol uint16) error {
	var (
		charnum   uint16
		previous0 bool
	)
	if b.remain() < 4 {
		return corruptedError("FSE table: input too small")
	}
	bitStream := b.Uint32NC()
	nbBits := uint((bitStream & 0xF) + minTablelog)
	if nbBits > tablelogAbsoluteMax {
		return corruptedError("FSE table: tableLog too large")
	}
	bitStream >>= 4
	bitCount := uint(4)

	s.actualTableLog = uint8(nbBits)
	remaining := int32((1 << nbBits) + 1)
	threshold := int32(1 << nbBits)
	gotTotal := int32(0)
	nbBits++

	for remaining > 1 && charnum <= maxSymbol {
		if previous0 {
			n0 := charnum
			for (bitStream & 0xFFFF) == 0xFFFF {
				n0 += 24
				if r := b.remain(); r > 5 {
					b.advance(2)
					bitStream = b.Uint32NC() >> bitCount
				} else {
					bitStream >>= 16
					bitCount += 16
				}
			}
			for (bitStream & 3) == 3 {
				n0 += 3
				bitStream >>= 2
				bitCount += 2
			}
			n0 += uint16(bitStream & 3)
			bitCount += 2

			if n0 > maxSymbolValue {
				return corruptedError("FSE table: maxSymbolValue too small")
			}
			for charnum < n0 {
				s.norm[uint8(charnum)] = 0
				charnum++
			}

			if r := b.remain(); r >= 7 || r-int(bitCount>>3) >= 4 {
				b.advance(bitCount >> 3)
				bitCount &= 7
				bitStream = b.Uint32NC() >> bitCount
			} else {
				bitStream >>= 2
			}
		}

		max := (2*threshold - 1) - remaining
		var count int32

		if int32(bitStream)&(threshold-1) < max {
			count = int32(bitStream) & (threshold - 1)
			bitCount += nbBits - 1
		} else {
			count = int32(bitStream) & (2*threshold - 1)
			if count >= threshold {
				count -= max
			}
			bitCount += nbBits
		}

		count--
		if count < 0 {
			remaining += count
			gotTotal -= count
		} else {
			remaining -= count
			gotTotal += count
		}
		s.norm[charnum&0xff] = int16(count)
		charnum++
		previous0 = count == 0
		for remaining < threshold {
			nbBits--
			threshold >>= 1
		}

		if r := b.remain(); r >= 7 || r-int(bitCount>>3) >= 4 {
			b.advance(bitCount >> 3)
			bitCount &= 7
			bitStream = b.Uint32NC() >> (bitCount & 31)
		} else {
			bitCount -= (uint)(8 * (len(b.b) - 4 - b.off))
			b.off = len(b.b) - 4
			bitStream = b.Uint32() >> (bitCount & 31)
		}
	}
	s.symbolLen = charnum
	if s.symbolLen <= 1 {
		return corruptedErrorf("symbolLen (%d) too small", s.symbolLen)
	}
	if s.symbolLen > maxSymbolValue+1 {
		return corruptedErrorf("symbolLen (%d) too big", s.symbolLen)
	}
	if remaining != 1 {
		return corruptedErrorf("corruption detected (remaining %d != 1)", remaining)
	}
	if bitCount > 32 {
		return corruptedErrorf("corruption detected (bitCount %d > 32)", bitCount)
	}
	if gotTotal != 1<<s.actualTableLog {
		return corruptedErrorf("corruption detected (total %d != %d)", gotTotal, 1<<s.actualTableLog)
	}
	b.advance((bitCount + 7) >> 3)
	return s.buildDtable()
}

// decSymbol packs FSE decoder fields (nbBits, addBits, newState, baseline) into a uint64.
type decSymbol uint64

// newDecSymbol packs decoder fields into a single uint64.
func newDecSymbol(nbits, addBits uint8, newState uint16, baseline uint32) decSymbol {
	return decSymbol(nbits) | (decSymbol(addBits) << 8) | (decSymbol(newState) << 16) | (decSymbol(baseline) << 32)
}

// nbBits returns the number of bits to read.
func (d decSymbol) nbBits() uint8 { return uint8(d) }

// addBits returns the extra bits count.
func (d decSymbol) addBits() uint8 { return uint8(d >> 8) }

// newState returns the next FSE state base.
func (d decSymbol) newState() uint16 { return uint16(d >> 16) }

// baselineInt returns the baseline value.
func (d decSymbol) baselineInt() int { return int(d >> 32) }

// final returns the baseline value and extra bits count.
func (d decSymbol) final() (int, uint8) { return d.baselineInt(), d.addBits() }

// setNBits sets the number of bits field.
func (d *decSymbol) setNBits(nBits uint8) {
	const mask = 0xffffffffffffff00
	*d = (*d & mask) | decSymbol(nBits)
}

// setAddBits sets the extra bits field.
func (d *decSymbol) setAddBits(addBits uint8) {
	const mask = 0xffffffffffff00ff
	*d = (*d & mask) | (decSymbol(addBits) << 8)
}

// setNewState sets the next-state field.
func (d *decSymbol) setNewState(state uint16) {
	const mask = 0xffffffff0000ffff
	*d = (*d & mask) | decSymbol(state)<<16
}

// setExt sets both extra bits and baseline fields.
func (d *decSymbol) setExt(addBits uint8, baseline uint32) {
	const mask = 0xffff00ff
	*d = (*d & mask) | (decSymbol(addBits) << 8) | (decSymbol(baseline) << 32)
}

// decSymbolValue creates a decSymbol from a symbol and its base/bits table.
func decSymbolValue(symb uint8, t []baseOffset) (decSymbol, error) {
	if int(symb) >= len(t) {
		return 0, corruptedErrorf("rle symbol %d >= max %d", symb, len(t))
	}
	lu := t[symb]
	return newDecSymbol(0, lu.addBits, 0, lu.baseLine), nil
}

// setRLE configures the decoder for a single repeated symbol.
func (s *fseDecoder) setRLE(symbol decSymbol) {
	s.actualTableLog = 0
	s.maxBits = symbol.addBits()
	s.dt[0] = symbol
}

// transform applies base/bits from the symbol table to each decoder entry.
func (s *fseDecoder) transform(t []baseOffset) error {
	tableSize := uint16(1 << s.actualTableLog)
	s.maxBits = 0
	for i, v := range s.dt[:tableSize] {
		add := v.addBits()
		if int(add) >= len(t) {
			return corruptedErrorf("invalid decoding table entry %d, symbol %d >= max (%d)", i, v.addBits(), len(t))
		}
		lu := t[add]
		if lu.addBits > s.maxBits {
			s.maxBits = lu.addBits
		}
		v.setExt(lu.addBits, lu.baseLine)
		s.dt[i] = v
	}
	return nil
}

// fseState tracks the current state during FSE-driven sequence decoding.
type fseState struct {
	dt    []decSymbol
	state decSymbol
}

// init reads the initial state from the bitstream.
func (s *fseState) init(br *bitReader, tableLog uint8, dt []decSymbol) {
	s.dt = dt
	br.fill()
	s.state = dt[br.getBits(tableLog)]
}

// buildDtable will build the decoding table.
func (s *fseDecoder) buildDtable() error {
	tableSize := uint32(1 << s.actualTableLog)
	highThreshold := tableSize - 1
	symbolNext := s.stateTable[:256]

	for i, v := range s.norm[:s.symbolLen] {
		if v == -1 {
			s.dt[highThreshold].setAddBits(uint8(i))
			highThreshold--
			v = 1
		}
		symbolNext[i] = uint16(v)
	}

	{
		tableMask := tableSize - 1
		step := tableStep(tableSize)
		position := uint32(0)
		for ss, v := range s.norm[:s.symbolLen] {
			for i := 0; i < int(v); i++ {
				s.dt[position].setAddBits(uint8(ss))
				for {
					position = (position + step) & tableMask
					if position <= highThreshold {
						break
					}
				}
			}
		}
		if position != 0 {
			return corruptedError("FSE table: position != 0")
		}
	}

	{
		tableSize := uint16(1 << s.actualTableLog)
		for u, v := range s.dt[:tableSize] {
			symbol := v.addBits()
			nextState := symbolNext[symbol]
			symbolNext[symbol] = nextState + 1
			nBits := s.actualTableLog - byte(highBit(uint32(nextState)))
			s.dt[u&maxTableMask].setNBits(nBits)
			newState := (nextState << nBits) - tableSize
			if newState > tableSize {
				return corruptedErrorf("newState (%d) outside table size (%d)", newState, tableSize)
			}
			if newState == uint16(u) && nBits == 0 {
				return corruptedErrorf("newState (%d) == oldState (%d) and no bits", newState, u)
			}
			s.dt[u&maxTableMask].setNewState(newState)
		}
	}
	return nil
}
