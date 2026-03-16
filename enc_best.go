// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import "math"

// Hash table parameters for the best encoder.
const (
	bestLongTableBits = 22
	bestLongTableSize = 1 << bestLongTableBits
	bestLongLen       = 8

	// Note: Increasing the short table bits or making the hash shorter
	// can actually lead to compression degradation since it will 'steal' more from the
	// long match table and match offsets are quite big.
	// This greatly depends on the type of input.
	bestShortTableBits = 18
	bestShortTableSize = 1 << bestShortTableBits
	bestShortLen       = 4
)

// match represents a candidate match during best-level encoding.
type match struct {
	offset int32
	s      int32
	length int32
	rep    int32
	est    int32
}

// highScore is the initial "no match" cost sentinel.
const highScore = maxMatchLength * 8

// shannonEntropyBits returns the Shannon entropy of b in bits.
func shannonEntropyBits(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	var hist [256]int
	for _, c := range b {
		hist[c]++
	}
	shannon := float64(0)
	invTotal := 1.0 / float64(len(b))
	for _, v := range hist[:] {
		if v > 0 {
			n := float64(v)
			shannon += math.Ceil(-math.Log2(n*invTotal) * n)
		}
	}
	return int(math.Ceil(shannon))
}

// estBits estimates the encoding cost of this match in bits.
func (m *match) estBits(bitsPerByte int32) {
	mlc := mlCode(uint32(m.length - zstdMinMatch))
	var ofc uint8
	if m.rep < 0 {
		ofc = ofCode(uint32(m.s-m.offset) + 3)
	} else {
		ofc = ofCode(uint32(m.rep) & 3)
	}
	ofTT, mlTT := fsePredefEnc[tableOffsets].ct.symbolTT[ofc], fsePredefEnc[tableMatchLengths].ct.symbolTT[mlc]

	m.est = int32(ofTT.outBits + mlTT.outBits)
	m.est += int32(ofTT.deltaNbBits>>16 + mlTT.deltaNbBits>>16)
	// Subtract savings compared to literal encoding...
	m.est -= (m.length * bitsPerByte) >> 10
	if m.est > 0 {
		m.length = 0
		m.est = highScore
	}
}

// bestFastEncoder implements the highest-compression encoder (levels 8-9).
type bestFastEncoder struct {
	encBase
	table     [bestShortTableSize]prevEntry
	longTable [bestLongTableSize]prevEntry
}

// encode compresses src into blk using the best algorithm with history.
func (e *bestFastEncoder) encode(blk *blockEnc, src []byte) {
	const (
		inputMargin            = 8 + 4
		minNonLiteralBlockSize = 16
	)

	// Protect against e.cur wraparound.
	if e.cur >= e.bufferReset-int32(len(e.hist)) {
		if len(e.hist) == 0 {
			e.table = [bestShortTableSize]prevEntry{}
			e.longTable = [bestLongTableSize]prevEntry{}
		} else {
			minOff := e.cur + int32(len(e.hist)) - e.maxMatchOff
			for i := range e.table[:] {
				v := e.table[i].offset
				v2 := e.table[i].prev
				if v < minOff {
					v = 0
					v2 = 0
				} else {
					v = v - e.cur + e.maxMatchOff
					if v2 < minOff {
						v2 = 0
					} else {
						v2 = v2 - e.cur + e.maxMatchOff
					}
				}
				e.table[i] = prevEntry{
					offset: v,
					prev:   v2,
				}
			}
			for i := range e.longTable[:] {
				v := e.longTable[i].offset
				v2 := e.longTable[i].prev
				if v < minOff {
					v = 0
					v2 = 0
				} else {
					v = v - e.cur + e.maxMatchOff
					if v2 < minOff {
						v2 = 0
					} else {
						v2 = v2 - e.cur + e.maxMatchOff
					}
				}
				e.longTable[i] = prevEntry{
					offset: v,
					prev:   v2,
				}
			}
		}
		e.cur = e.maxMatchOff
	}

	s := e.addBlock(src)
	blk.size = len(src)

	// Check RLE first
	if len(src) > zstdMinMatch {
		ml := matchLen(src[1:], src)
		if ml == len(src)-1 {
			blk.literals = append(blk.literals, src[0])
			blk.sequences = append(blk.sequences, seq{litLen: 1, matchLen: uint32(len(src)-1) - zstdMinMatch, offset: 1 + 3})
			return
		}
	}

	if len(src) < minNonLiteralBlockSize {
		blk.extraLits = len(src)
		blk.literals = blk.literals[:len(src)]
		copy(blk.literals, src)
		return
	}

	// Use this to estimate literal cost.
	// Scaled by 10 bits.
	bitsPerByte := max(int32((shannonEntropyBits(src)*1024)/len(src)), 1024)

	// Override src
	src = e.hist
	sLimit := int32(len(src)) - inputMargin
	const kSearchStrength = 10

	nextEmit := s

	offset1 := int32(blk.recentOffsets[0])
	offset2 := int32(blk.recentOffsets[1])
	offset3 := int32(blk.recentOffsets[2])

	addLiterals := func(s *seq, until int32) {
		if until == nextEmit {
			return
		}
		blk.literals = append(blk.literals, src[nextEmit:until]...)
		s.litLen = uint32(until - nextEmit)
	}

encodeLoop:
	for {
		canRepeat := len(blk.sequences) > 2

		const goodEnough = 250

		cv := load6432(src, s)

		nextHashL := hashLen(cv, bestLongTableBits, bestLongLen)
		nextHashS := hashLen(cv, bestShortTableBits, bestShortLen)
		candidateL := e.longTable[nextHashL]
		candidateS := e.table[nextHashS]

		improve := func(m *match, offset int32, s int32, first uint32, rep int32) {
			delta := s - offset
			if delta >= e.maxMatchOff || delta <= 0 || load3232(src, offset) != first {
				return
			}
			if m.length > 16 {
				left := len(src) - int(m.s+m.length)
				if left <= 0 {
					return
				}
				checkLen := m.length - (s - m.s) - 8
				if left > 2 && checkLen > 4 {
					a := load3232(src, offset+checkLen)
					b := load3232(src, s+checkLen)
					if a != b {
						return
					}
				}
			}
			l := 4 + e.matchlen(s+4, offset+4, src)
			if m.rep <= 0 {
				tMin := max(s-e.maxMatchOff, 0)
				for offset > tMin && s > nextEmit && src[offset-1] == src[s-1] && l < maxMatchLength {
					s--
					offset--
					l++
				}
			}
			cand := match{offset: offset, s: s, length: l, rep: rep}
			cand.estBits(bitsPerByte)
			if m.est >= highScore || cand.est-m.est+(cand.s-m.s)*bitsPerByte>>10 < 0 {
				*m = cand
			}
		}

		best := match{s: s, est: highScore}
		improve(&best, candidateL.offset-e.cur, s, uint32(cv), -1)
		improve(&best, candidateL.prev-e.cur, s, uint32(cv), -1)
		improve(&best, candidateS.offset-e.cur, s, uint32(cv), -1)
		improve(&best, candidateS.prev-e.cur, s, uint32(cv), -1)

		if canRepeat && best.length < goodEnough {
			if s == nextEmit {
				improve(&best, s-offset2, s, uint32(cv), 1|4)
				improve(&best, s-offset3, s, uint32(cv), 2|4)
				if offset1 > 1 {
					improve(&best, s-(offset1-1), s, uint32(cv), 3|4)
				}
			}

			if best.rep <= 0 {
				cv32 := uint32(cv >> 8)
				spp := s + 1
				improve(&best, spp-offset1, spp, cv32, 1)
				improve(&best, spp-offset2, spp, cv32, 2)
				improve(&best, spp-offset3, spp, cv32, 3)
				if best.rep < 0 {
					cv32 = uint32(cv >> 24)
					spp += 2
					improve(&best, spp-offset1, spp, cv32, 1)
					improve(&best, spp-offset2, spp, cv32, 2)
					improve(&best, spp-offset3, spp, cv32, 3)
				}
			}
		}

		e.longTable[nextHashL] = prevEntry{offset: s + e.cur, prev: candidateL.offset}
		e.table[nextHashS] = prevEntry{offset: s + e.cur, prev: candidateS.offset}
		index0 := s + 1

		if best.length < goodEnough {
			if best.length < 4 {
				s += 1 + (s-nextEmit)>>(kSearchStrength-1)
				if s >= sLimit {
					break encodeLoop
				}
				continue
			}

			candidateS = e.table[hashLen(cv>>8, bestShortTableBits, bestShortLen)]
			cv = load6432(src, s+1)
			cv2 := load6432(src, s+2)
			candidateL = e.longTable[hashLen(cv, bestLongTableBits, bestLongLen)]
			candidateL2 := e.longTable[hashLen(cv2, bestLongTableBits, bestLongLen)]

			improve(&best, candidateS.offset-e.cur, s+1, uint32(cv), -1)
			improve(&best, candidateL.offset-e.cur, s+1, uint32(cv), -1)
			improve(&best, candidateL.prev-e.cur, s+1, uint32(cv), -1)
			improve(&best, candidateL2.offset-e.cur, s+2, uint32(cv2), -1)
			improve(&best, candidateL2.prev-e.cur, s+2, uint32(cv2), -1)

			const skipBeginning = 2
			if best.s > s-skipBeginning {
				if sAt := best.s + best.length; sAt < sLimit {
					nextHashL := hashLen(load6432(src, sAt), bestLongTableBits, bestLongLen)
					candidateEnd := e.longTable[nextHashL]

					if off := candidateEnd.offset - e.cur - best.length + skipBeginning; off >= 0 {
						improve(&best, off, best.s+skipBeginning, load3232(src, best.s+skipBeginning), -1)
						if off := candidateEnd.prev - e.cur - best.length + skipBeginning; off >= 0 {
							improve(&best, off, best.s+skipBeginning, load3232(src, best.s+skipBeginning), -1)
						}
					}
				}
			}
		}

		s = best.s
		if best.rep > 0 {
			var seq seq
			seq.matchLen = uint32(best.length - zstdMinMatch)
			addLiterals(&seq, best.s)

			seq.offset = uint32(best.rep & 3)
			blk.sequences = append(blk.sequences, seq)

			s = best.s + best.length
			nextEmit = s

			end := min(s, sLimit+4)
			off := index0 + e.cur
			for index0 < end {
				cv0 := load6432(src, index0)
				h0 := hashLen(cv0, bestLongTableBits, bestLongLen)
				h1 := hashLen(cv0, bestShortTableBits, bestShortLen)
				e.longTable[h0] = prevEntry{offset: off, prev: e.longTable[h0].offset}
				e.table[h1] = prevEntry{offset: off, prev: e.table[h1].offset}
				off++
				index0++
			}

			switch best.rep {
			case 2, 4 | 1:
				offset1, offset2 = offset2, offset1
			case 3, 4 | 2:
				offset1, offset2, offset3 = offset3, offset1, offset2
			case 4 | 3:
				offset1, offset2, offset3 = offset1-1, offset1, offset2
			}
			if s >= sLimit {
				break encodeLoop
			}
			continue
		}

		t := best.offset
		offset1, offset2, offset3 = s-t, offset1, offset2

		var seq seq
		l := best.length
		seq.litLen = uint32(s - nextEmit)
		seq.matchLen = uint32(l - zstdMinMatch)
		if seq.litLen > 0 {
			blk.literals = append(blk.literals, src[nextEmit:s]...)
		}
		seq.offset = uint32(s-t) + 3
		s += l
		blk.sequences = append(blk.sequences, seq)
		nextEmit = s

		end := min(s, sLimit-4)
		off := index0 + e.cur
		for index0 < end {
			cv0 := load6432(src, index0)
			h0 := hashLen(cv0, bestLongTableBits, bestLongLen)
			h1 := hashLen(cv0, bestShortTableBits, bestShortLen)
			e.longTable[h0] = prevEntry{offset: off, prev: e.longTable[h0].offset}
			e.table[h1] = prevEntry{offset: off, prev: e.table[h1].offset}
			index0++
			off++
		}
		if s >= sLimit {
			break encodeLoop
		}
	}

	if int(nextEmit) < len(src) {
		blk.literals = append(blk.literals, src[nextEmit:]...)
		blk.extraLits = len(src) - int(nextEmit)
	}
	blk.recentOffsets[0] = uint32(offset1)
	blk.recentOffsets[1] = uint32(offset2)
	blk.recentOffsets[2] = uint32(offset3)
}

// encodeNoHist compresses src into blk without preserving history between calls.
func (e *bestFastEncoder) encodeNoHist(blk *blockEnc, src []byte) {
	e.ensureHist(len(src))
	e.encode(blk, src)
}

// reset prepares the encoder for a new stream, optionally loading a dictionary.
func (e *bestFastEncoder) reset(d *dict, singleBlock bool) {
	e.resetBase(d, singleBlock)
	if d == nil {
		return
	}
	e.lastDictID = d.id
	e.cur = e.maxMatchOff
	for i := range e.table[:] {
		e.table[i] = prevEntry{}
	}
	for i := range e.longTable[:] {
		e.longTable[i] = prevEntry{}
	}
	if len(d.content) < 8 {
		return
	}
	end := int32(len(d.content)) - 8 + e.maxMatchOff
	for i := e.maxMatchOff; i < end; i += 4 {
		cv := load6432(d.content, i-e.maxMatchOff)
		h0 := hashLen(cv, bestShortTableBits, bestShortLen)
		h1 := hashLen(cv>>8, bestShortTableBits, bestShortLen)
		h2 := hashLen(cv>>16, bestShortTableBits, bestShortLen)
		h3 := hashLen(cv>>24, bestShortTableBits, bestShortLen)
		e.table[h0] = prevEntry{offset: i, prev: e.table[h0].offset}
		e.table[h1] = prevEntry{offset: i + 1, prev: e.table[h1].offset}
		e.table[h2] = prevEntry{offset: i + 2, prev: e.table[h2].offset}
		e.table[h3] = prevEntry{offset: i + 3, prev: e.table[h3].offset}
	}
	cv := load6432(d.content, 0)
	h := hashLen(cv, bestLongTableBits, bestLongLen)
	e.longTable[h] = prevEntry{offset: e.maxMatchOff, prev: e.longTable[h].offset}
	for i := e.maxMatchOff + 1; i < end; i++ {
		cv = cv>>8 | (uint64(d.content[i-e.maxMatchOff+7]) << 56)
		h = hashLen(cv, bestLongTableBits, bestLongLen)
		e.longTable[h] = prevEntry{offset: i, prev: e.longTable[h].offset}
	}
}
