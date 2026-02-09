// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fse

import (
	"errors"
	"fmt"
)

const tablelogAbsoluteMax = 15

func Decompress(b []byte, s *Scratch) ([]byte, error) {
	s, err := s.prepare(b)
	if err != nil {
		return nil, err
	}
	s.Out = s.Out[:0]
	if err = s.readNCount(); err != nil {
		return nil, err
	}
	if err = s.buildDtable(); err != nil {
		return nil, err
	}
	if err = s.decompress(); err != nil {
		return nil, err
	}
	return s.Out, nil
}

func (s *Scratch) readNCount() error {
	var (
		charnum   uint16
		previous0 bool
		b         = &s.br
	)
	iend := b.remain()
	if iend < 4 {
		return errors.New("input too small")
	}
	bitStream := b.Uint32()
	nbBits := uint((bitStream & 0xF) + minTablelog)
	if nbBits > tablelogAbsoluteMax {
		return errors.New("tableLog too large")
	}
	bitStream >>= 4
	bitCount := uint(4)

	s.actualTableLog = uint8(nbBits)
	remaining := int32((1 << nbBits) + 1)
	threshold := int32(1 << nbBits)
	gotTotal := int32(0)
	nbBits++

	for remaining > 1 {
		if previous0 {
			n0 := charnum
			for (bitStream & 0xFFFF) == 0xFFFF {
				n0 += 24
				if b.off < iend-5 {
					b.advance(2)
					bitStream = b.Uint32() >> bitCount
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
				return errors.New("maxSymbolValue too small")
			}
			for charnum < n0 {
				s.norm[charnum&0xff] = 0
				charnum++
			}

			if b.off <= iend-7 || b.off+int(bitCount>>3) <= iend-4 {
				b.advance(bitCount >> 3)
				bitCount &= 7
				bitStream = b.Uint32() >> bitCount
			} else {
				bitStream >>= 2
			}
		}

		max := (2 * threshold) - 1 - remaining
		var count int32

		if (int32(bitStream) & (threshold - 1)) < max {
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
		if b.off <= iend-7 || b.off+int(bitCount>>3) <= iend-4 {
			b.advance(bitCount >> 3)
			bitCount &= 7
		} else {
			bitCount -= uint(8 * (len(b.b) - 4 - b.off))
			b.off = len(b.b) - 4
		}
		bitStream = b.Uint32() >> (bitCount & 31)
	}
	s.symbolLen = charnum

	if s.symbolLen <= 1 {
		return fmt.Errorf("symbolLen (%d) too small", s.symbolLen)
	}
	if s.symbolLen > maxSymbolValue+1 {
		return fmt.Errorf("symbolLen (%d) too big", s.symbolLen)
	}
	if remaining != 1 {
		return fmt.Errorf("corruption detected (remaining %d != 1)", remaining)
	}
	if bitCount > 32 {
		return fmt.Errorf("corruption detected (bitCount %d > 32)", bitCount)
	}
	if gotTotal != 1<<s.actualTableLog {
		return fmt.Errorf("corruption detected (total %d != %d)", gotTotal, 1<<s.actualTableLog)
	}
	b.advance((bitCount + 7) >> 3)
	return nil
}

type decSymbol struct {
	newState uint16
	symbol   uint8
	nbBits   uint8
}

func (s *Scratch) allocDtable() {
	tableSize := 1 << s.actualTableLog
	if cap(s.decTable) < tableSize {
		s.decTable = make([]decSymbol, tableSize)
	}
	s.decTable = s.decTable[:tableSize]

	if cap(s.ct.tableSymbol) < 256 {
		s.ct.tableSymbol = make([]byte, 256)
	}
	s.ct.tableSymbol = s.ct.tableSymbol[:256]

	if cap(s.ct.stateTable) < 256 {
		s.ct.stateTable = make([]uint16, 256)
	}
	s.ct.stateTable = s.ct.stateTable[:256]
}

func (s *Scratch) buildDtable() error {
	tableSize := uint32(1 << s.actualTableLog)
	highThreshold := tableSize - 1
	s.allocDtable()
	symbolNext := s.ct.stateTable[:256]

	s.zeroBits = false
	{
		largeLimit := int16(1 << (s.actualTableLog - 1))
		for i, v := range s.norm[:s.symbolLen] {
			if v == -1 {
				s.decTable[highThreshold].symbol = uint8(i)
				highThreshold--
				symbolNext[i] = 1
			} else {
				if v >= largeLimit {
					s.zeroBits = true
				}
				symbolNext[i] = uint16(v)
			}
		}
	}
	{
		tableMask := tableSize - 1
		step := tableStep(tableSize)
		position := uint32(0)
		for ss, v := range s.norm[:s.symbolLen] {
			for i := 0; i < int(v); i++ {
				s.decTable[position].symbol = uint8(ss)
				position = (position + step) & tableMask
				for position > highThreshold {
					position = (position + step) & tableMask
				}
			}
		}
		if position != 0 {
			return errors.New("corrupted input (position != 0)")
		}
	}
	{
		tableSize := uint16(1 << s.actualTableLog)
		for u, v := range s.decTable {
			symbol := v.symbol
			nextState := symbolNext[symbol]
			symbolNext[symbol] = nextState + 1
			nBits := s.actualTableLog - byte(highBits(uint32(nextState)))
			s.decTable[u].nbBits = nBits
			newState := (nextState << nBits) - tableSize
			if newState >= tableSize {
				return fmt.Errorf("newState (%d) outside table size (%d)", newState, tableSize)
			}
			if newState == uint16(u) && nBits == 0 {
				return fmt.Errorf("newState (%d) == oldState (%d) and no bits", newState, u)
			}
			s.decTable[u].newState = newState
		}
	}
	return nil
}

func (s *Scratch) decompress() error {
	br := &s.bits
	if err := br.init(s.br.unread()); err != nil {
		return err
	}

	var s1, s2 decoder
	s1.init(br, s.decTable, s.actualTableLog)
	s2.init(br, s.decTable, s.actualTableLog)

	var tmp = s.ct.tableSymbol[:256]
	var off uint8

	if !s.zeroBits {
		for br.off >= 8 {
			br.fillFast()
			tmp[off+0] = s1.nextFast()
			tmp[off+1] = s2.nextFast()
			br.fillFast()
			tmp[off+2] = s1.nextFast()
			tmp[off+3] = s2.nextFast()
			off += 4
			if off == 0 {
				s.Out = append(s.Out, tmp...)
				if len(s.Out) >= s.DecompressLimit {
					return fmt.Errorf("output size (%d) > DecompressLimit (%d)", len(s.Out), s.DecompressLimit)
				}
			}
		}
	} else {
		for br.off >= 8 {
			br.fillFast()
			tmp[off+0] = s1.next()
			tmp[off+1] = s2.next()
			br.fillFast()
			tmp[off+2] = s1.next()
			tmp[off+3] = s2.next()
			off += 4
			if off == 0 {
				s.Out = append(s.Out, tmp...)
				if len(s.Out) >= s.DecompressLimit {
					return fmt.Errorf("output size (%d) > DecompressLimit (%d)", len(s.Out), s.DecompressLimit)
				}
			}
		}
	}
	s.Out = append(s.Out, tmp[:off]...)

	for {
		if s1.finished() {
			s.Out = append(s.Out, s1.final(), s2.final())
			break
		}
		br.fill()
		s.Out = append(s.Out, s1.next())
		if s2.finished() {
			s.Out = append(s.Out, s2.final(), s1.final())
			break
		}
		s.Out = append(s.Out, s2.next())
		if len(s.Out) >= s.DecompressLimit {
			return fmt.Errorf("output size (%d) > DecompressLimit (%d)", len(s.Out), s.DecompressLimit)
		}
	}
	return br.close()
}

type decoder struct {
	state uint16
	br    *bitReader
	dt    []decSymbol
}

func (d *decoder) init(in *bitReader, dt []decSymbol, tableLog uint8) {
	d.dt = dt
	d.br = in
	d.state = in.getBits(tableLog)
}

func (d *decoder) next() uint8 {
	n := &d.dt[d.state]
	lowBits := d.br.getBits(n.nbBits)
	d.state = n.newState + lowBits
	return n.symbol
}

func (d *decoder) finished() bool {
	return d.br.finished() && d.dt[d.state].nbBits > 0
}

func (d *decoder) final() uint8 {
	return d.dt[d.state].symbol
}

func (d *decoder) nextFast() uint8 {
	n := d.dt[d.state]
	lowBits := d.br.getBitsFast(n.nbBits)
	d.state = n.newState + lowBits
	return n.symbol
}
