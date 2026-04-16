// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "io"

// seq holds a single encoded sequence (literal length, match length, offset).
type seq struct {
	litLen                 uint32
	matchLen               uint32
	offset                 uint32
	llCode, mlCode, ofCode uint8
}

// seqCompMode identifies the compression mode for a sequence section FSE table.
type seqCompMode uint8

// Sequence compression modes as defined in the zstd specification.
const (
	compModePredefined seqCompMode = iota
	compModeRLE
	compModeFSE
	compModeRepeat
)

// sequenceDec decodes one sequence component (litLen, offset, or matchLen).
type sequenceDec struct {
	fse    *fseDecoder
	state  fseState
	repeat bool
}

// init reads the initial FSE state from the bitstream.
func (s *sequenceDec) init(br *bitReader) error {
	if s.fse == nil {
		return corruptedError("sequence decoder not defined")
	}
	s.state.init(br, s.fse.actualTableLog, s.fse.dt[:1<<s.fse.actualTableLog])
	return nil
}

// sequenceDecs holds the three sequence decoders and their shared state.
type sequenceDecs struct {
	litLengths   sequenceDec
	offsets      sequenceDec
	matchLengths sequenceDec
	prevOffset   [3]int
	dict         []byte
	literals     []byte
	out          []byte
	nSeqs        int
	br           *bitReader
	windowSize   int
	maxBits      uint8
}

// initialize prepares all three sequence decoders for decoding.
func (s *sequenceDecs) initialize(br *bitReader, hist *history, out []byte) error {
	if err := s.litLengths.init(br); err != nil {
		return &ErrCorrupted{msg: "litLengths", err: err}
	}
	if err := s.offsets.init(br); err != nil {
		return &ErrCorrupted{msg: "offsets", err: err}
	}
	if err := s.matchLengths.init(br); err != nil {
		return &ErrCorrupted{msg: "matchLengths", err: err}
	}
	s.br = br
	s.prevOffset = hist.recentOffsets
	s.maxBits = s.litLengths.fse.maxBits + s.offsets.fse.maxBits + s.matchLengths.fse.maxBits
	s.windowSize = hist.windowSize
	s.out = out
	s.dict = nil
	if hist.dict != nil {
		s.dict = hist.dict.content
	}
	return nil
}

// freeDecoders returns non-predefined FSE decoders to the pool.
func (s *sequenceDecs) freeDecoders() {
	if f := s.litLengths.fse; f != nil && !f.preDefined {
		fseDecoderPool.Put(f)
		s.litLengths.fse = nil
	}
	if f := s.offsets.fse; f != nil && !f.preDefined {
		fseDecoderPool.Put(f)
		s.offsets.fse = nil
	}
	if f := s.matchLengths.fse; f != nil && !f.preDefined {
		fseDecoderPool.Put(f)
		s.matchLengths.fse = nil
	}
}

// decodeSync decodes all sequences and executes them synchronously.
func (s *sequenceDecs) decodeSync(hist []byte) error {
	br := s.br
	seqs := s.nSeqs
	startSize := len(s.out)
	llTable, mlTable, ofTable := s.litLengths.fse.dt[:maxTablesize], s.matchLengths.fse.dt[:maxTablesize], s.offsets.fse.dt[:maxTablesize]
	llState, mlState, ofState := s.litLengths.state.state, s.matchLengths.state.state, s.offsets.state.state
	out := s.out
	maxBlkSize := min(s.windowSize, maxCompressedBlockSize)

	for i := seqs - 1; i >= 0; i-- {
		if br.overread() {
			return io.ErrUnexpectedEOF
		}
		var ll, mo, ml int
		if br.cursor > 4+((maxOffsetBits+16+16)>>3) {
			var llB, mlB, moB uint8
			ll, llB = llState.final()
			ml, mlB = mlState.final()
			mo, moB = ofState.final()

			br.fillFast()
			mo += br.getBits(moB)
			if s.maxBits > 32 {
				br.fillFast()
			}
			ml += br.getBits(mlB)
			ll += br.getBits(llB)

			if moB > 1 {
				s.prevOffset[2] = s.prevOffset[1]
				s.prevOffset[1] = s.prevOffset[0]
				s.prevOffset[0] = mo
			} else {
				if ll == 0 {
					mo++
				}
				if mo == 0 {
					mo = s.prevOffset[0]
				} else {
					var temp int
					if mo == 3 {
						temp = s.prevOffset[0] - 1
					} else {
						temp = s.prevOffset[mo]
					}
					if temp == 0 {
						temp = 1
					}
					if mo != 1 {
						s.prevOffset[2] = s.prevOffset[1]
					}
					s.prevOffset[1] = s.prevOffset[0]
					s.prevOffset[0] = temp
					mo = temp
				}
			}
			br.fillFast()
		} else {
			ll, mo, ml = s.next(br, llState, mlState, ofState)
			br.fill()
		}

		if ll > len(s.literals) {
			return corruptedErrorf("unexpected literal count, want %d bytes, but only %d available", ll, len(s.literals))
		}
		size := ll + ml + len(out)
		if size-startSize > maxBlkSize {
			return corruptedErrorf("output bigger than max block size (%d)", maxBlkSize)
		}
		if size > cap(out) {
			used := len(out) - startSize
			addBytes := 256 + ll + ml + used>>2
			if used+addBytes > maxBlkSize {
				addBytes = maxBlkSize - used
			}
			out = append(out, make([]byte, addBytes)...)
			out = out[:len(out)-addBytes]
		}
		if ml > maxMatchLen {
			return corruptedErrorf("match len (%d) bigger than max allowed length", ml)
		}

		out = append(out, s.literals[:ll]...)
		s.literals = s.literals[ll:]

		if mo == 0 && ml > 0 {
			return corruptedErrorf("zero matchoff and matchlen (%d) > 0", ml)
		}

		if mo > len(out)+len(hist) || mo > s.windowSize {
			if len(s.dict) == 0 {
				return corruptedErrorf("match offset (%d) bigger than current history (%d)", mo, len(out)+len(hist)-startSize)
			}
			dictO := len(s.dict) - (mo - (len(out) + len(hist)))
			if dictO < 0 || dictO >= len(s.dict) {
				return corruptedErrorf("match offset (%d) bigger than current history (%d)", mo, len(out)+len(hist)-startSize)
			}
			end := dictO + ml
			if end > len(s.dict) {
				out = append(out, s.dict[dictO:]...)
				ml -= len(s.dict) - dictO
			} else {
				out = append(out, s.dict[dictO:end]...)
				mo = 0
				ml = 0
			}
		}

		if v := mo - len(out); v > 0 {
			start := len(hist) - v
			if ml > v {
				out = append(out, hist[start:]...)
				ml -= v
			} else {
				out = append(out, hist[start:start+ml]...)
				ml = 0
			}
		}
		if ml > 0 {
			start := len(out) - mo
			if ml <= len(out)-start {
				out = append(out, out[start:start+ml]...)
			} else {
				out = out[:len(out)+ml]
				src := out[start : start+ml]
				dst := out[len(out)-ml:]
				dst = dst[:len(src)]
				// copy must not be used here: src and dst overlap, and
				// the zstd match-copy semantics require byte-at-a-time
				// expansion (e.g. offset=1 replicates a single byte).
				for i := range src {
					dst[i] = src[i]
				}
			}
		}
		if i == 0 {
			break
		}

		nBits := llState.nbBits() + mlState.nbBits() + ofState.nbBits()
		if nBits == 0 {
			llState = llTable[llState.newState()&maxTableMask]
			mlState = mlTable[mlState.newState()&maxTableMask]
			ofState = ofTable[ofState.newState()&maxTableMask]
		} else {
			bits := br.get32BitsFast(nBits)
			lowBits := uint16(bits >> ((ofState.nbBits() + mlState.nbBits()) & 31))
			llState = llTable[(llState.newState()+lowBits)&maxTableMask]

			lowBits = uint16(bits >> (ofState.nbBits() & 31))
			lowBits &= bitMask[mlState.nbBits()&15]
			mlState = mlTable[(mlState.newState()+lowBits)&maxTableMask]

			lowBits = uint16(bits) & bitMask[ofState.nbBits()&15]
			ofState = ofTable[(ofState.newState()+lowBits)&maxTableMask]
		}
	}

	if size := len(s.literals) + len(out) - startSize; size > maxBlkSize {
		return corruptedErrorf("output bigger than max block size (%d)", maxBlkSize)
	}

	s.out = append(out, s.literals...)
	return br.close()
}

// bitMask maps bit count to a 16-bit mask used in FSE state transitions.
var bitMask [16]uint16

// init populates the bitMask lookup table.
func init() {
	for i := range bitMask[:] {
		bitMask[i] = uint16((1 << uint(i)) - 1)
	}
}

// next decodes one sequence from the bitstream (slow path).
func (s *sequenceDecs) next(br *bitReader, llState, mlState, ofState decSymbol) (ll, mo, ml int) {
	ll, llB := llState.final()
	ml, mlB := mlState.final()
	mo, moB := ofState.final()

	br.fill()
	mo += br.getBits(moB)
	if s.maxBits > 32 {
		br.fill()
	}
	ml += br.getBits(mlB)
	ll += br.getBits(llB)
	mo = s.adjustOffset(mo, ll, moB)
	return
}

// adjustOffset resolves repeat offsets per the zstd spec.
func (s *sequenceDecs) adjustOffset(offset, litLen int, offsetB uint8) int {
	if offsetB > 1 {
		s.prevOffset[2] = s.prevOffset[1]
		s.prevOffset[1] = s.prevOffset[0]
		s.prevOffset[0] = offset
		return offset
	}
	if litLen == 0 {
		offset++
	}
	if offset == 0 {
		return s.prevOffset[0]
	}
	var temp int
	if offset == 3 {
		temp = s.prevOffset[0] - 1
	} else {
		temp = s.prevOffset[offset]
	}
	if temp == 0 {
		temp = 1
	}
	if offset != 1 {
		s.prevOffset[2] = s.prevOffset[1]
	}
	s.prevOffset[1] = s.prevOffset[0]
	s.prevOffset[0] = temp
	return temp
}
