// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

// Hash table parameters for the better encoder.
const (
	betterLongTableBits = 19
	betterLongTableSize = 1 << betterLongTableBits
	betterLongLen       = 8

	// Note: Increasing the short table bits or making the hash shorter
	// can actually lead to compression degradation since it will 'steal' more from the
	// long match table and match offsets are quite big.
	// This greatly depends on the type of input.
	betterShortTableBits = 13
	betterShortTableSize = 1 << betterShortTableBits
	betterShortLen       = 5
)

// prevEntry stores current and previous hash table offsets for two-deep matching.
type prevEntry struct {
	offset int32
	prev   int32
}

// betterFastEncoder implements the better encoder (levels 5-7).
type betterFastEncoder struct {
	encBase
	table     [betterShortTableSize]tableEntry
	longTable [betterLongTableSize]prevEntry
}

// encode compresses src into blk using the better algorithm with history.
func (e *betterFastEncoder) encode(blk *blockEnc, src []byte) {
	const (
		inputMargin            = 8 + 2
		minNonLiteralBlockSize = 16
	)

	// Protect against e.cur wraparound.
	if e.cur >= e.bufferReset-int32(len(e.hist)) {
		if len(e.hist) == 0 {
			e.table = [betterShortTableSize]tableEntry{}
			e.longTable = [betterLongTableSize]prevEntry{}
		} else {
			minOff := e.cur + int32(len(e.hist)) - e.maxMatchOff
			for i := range e.table[:] {
				v := e.table[i].offset
				if v < minOff {
					v = 0
				} else {
					v = v - e.cur + e.maxMatchOff
				}
				e.table[i].offset = v
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

	src = e.hist
	sLimit := int32(len(src)) - inputMargin
	const stepSize = 1
	const kSearchStrength = 9

	nextEmit := s
	cv := load6432(src, s)

	offset1 := int32(blk.recentOffsets[0])
	offset2 := int32(blk.recentOffsets[1])

	addLiterals := func(s *seq, until int32) {
		if until == nextEmit {
			return
		}
		blk.literals = append(blk.literals, src[nextEmit:until]...)
		s.litLen = uint32(until - nextEmit)
	}

encodeLoop:
	for {
		var t int32
		canRepeat := len(blk.sequences) > 2
		var matched, index0 int32

		for {
			nextHashL := hashLen(cv, betterLongTableBits, betterLongLen)
			nextHashS := hashLen(cv, betterShortTableBits, betterShortLen)
			candidateL := e.longTable[nextHashL]
			candidateS := e.table[nextHashS]

			const repOff = 1
			repIndex := s - offset1 + repOff
			off := s + e.cur
			e.longTable[nextHashL] = prevEntry{offset: off, prev: candidateL.offset}
			e.table[nextHashS] = tableEntry{offset: off, val: uint32(cv)}
			index0 = s + 1

			if canRepeat {
				if repIndex >= 0 && load3232(src, repIndex) == uint32(cv>>(repOff*8)) {
					var seq seq
					length := 4 + e.matchlen(s+4+repOff, repIndex+4, src)

					seq.matchLen = uint32(length - zstdMinMatch)

					start := s + repOff
					startLimit := nextEmit + 1

					tMin := max(s-e.maxMatchOff, 0)
					for repIndex > tMin && start > startLimit && src[repIndex-1] == src[start-1] && seq.matchLen < maxMatchLength-zstdMinMatch-1 {
						repIndex--
						start--
						seq.matchLen++
					}
					addLiterals(&seq, start)

					seq.offset = 1
					blk.sequences = append(blk.sequences, seq)

					index0 := s + repOff
					s += length + repOff

					nextEmit = s
					if s >= sLimit {
						break encodeLoop
					}
					for index0 < s-1 {
						cv0 := load6432(src, index0)
						cv1 := cv0 >> 8
						h0 := hashLen(cv0, betterLongTableBits, betterLongLen)
						off := index0 + e.cur
						e.longTable[h0] = prevEntry{offset: off, prev: e.longTable[h0].offset}
						e.table[hashLen(cv1, betterShortTableBits, betterShortLen)] = tableEntry{offset: off + 1, val: uint32(cv1)}
						index0 += 2
					}
					cv = load6432(src, s)
					continue
				}
			}
			// Find the offsets of our two matches.
			coffsetL := candidateL.offset - e.cur
			coffsetLP := candidateL.prev - e.cur

			// Check if we have a long match.
			if s-coffsetL < e.maxMatchOff && cv == load6432(src, coffsetL) {
				matched = e.matchlen(s+8, coffsetL+8, src) + 8
				t = coffsetL

				if s-coffsetLP < e.maxMatchOff && cv == load6432(src, coffsetLP) {
					prevMatch := e.matchlen(s+8, coffsetLP+8, src) + 8
					if prevMatch > matched {
						matched = prevMatch
						t = coffsetLP
					}
				}
				break
			}

			// Check if we have a long match on prev.
			if s-coffsetLP < e.maxMatchOff && cv == load6432(src, coffsetLP) {
				matched = e.matchlen(s+8, coffsetLP+8, src) + 8
				t = coffsetLP
				break
			}

			coffsetS := candidateS.offset - e.cur

			// Check if we have a short match.
			if s-coffsetS < e.maxMatchOff && uint32(cv) == candidateS.val {
				matched = e.matchlen(s+4, coffsetS+4, src) + 4

				// See if we can find a long match at s+1
				const checkAt = 1
				cv := load6432(src, s+checkAt)
				nextHashL = hashLen(cv, betterLongTableBits, betterLongLen)
				candidateL = e.longTable[nextHashL]
				coffsetL = candidateL.offset - e.cur

				e.longTable[nextHashL] = prevEntry{offset: s + checkAt + e.cur, prev: candidateL.offset}
				if s-coffsetL < e.maxMatchOff && cv == load6432(src, coffsetL) {
					matchedNext := e.matchlen(s+8+checkAt, coffsetL+8, src) + 8
					if matchedNext > matched {
						t = coffsetL
						s += checkAt
						matched = matchedNext
						break
					}
				}

				// Check prev long...
				coffsetL = candidateL.prev - e.cur
				if s-coffsetL < e.maxMatchOff && cv == load6432(src, coffsetL) {
					matchedNext := e.matchlen(s+8+checkAt, coffsetL+8, src) + 8
					if matchedNext > matched {
						t = coffsetL
						s += checkAt
						matched = matchedNext
						break
					}
				}
				t = coffsetS
				break
			}

			// No match found, move forward in input.
			s += stepSize + ((s - nextEmit) >> (kSearchStrength - 1))
			if s >= sLimit {
				break encodeLoop
			}
			cv = load6432(src, s)
		}

		// Try to find a better match by searching for a long match at the end of the current best match
		if s+matched < sLimit {
			// Allow some bytes at the beginning to mismatch.
			// Sweet spot is around 3 bytes, but depends on input.
			// The skipped bytes are tested in Extend backwards,
			// and still picked up as part of the match if they do.
			const skipBeginning = 3

			nextHashL := hashLen(load6432(src, s+matched), betterLongTableBits, betterLongLen)
			s2 := s + skipBeginning
			cv := load3232(src, s2)
			candidateL := e.longTable[nextHashL]
			coffsetL := candidateL.offset - e.cur - matched + skipBeginning
			if coffsetL >= 0 && coffsetL < s2 && s2-coffsetL < e.maxMatchOff && cv == load3232(src, coffsetL) {
				matchedNext := e.matchlen(s2+4, coffsetL+4, src) + 4
				if matchedNext > matched {
					t = coffsetL
					s = s2
					matched = matchedNext
				}
			}

			// Check prev long...
			coffsetL = candidateL.prev - e.cur - matched + skipBeginning
			if coffsetL >= 0 && coffsetL < s2 && s2-coffsetL < e.maxMatchOff && cv == load3232(src, coffsetL) {
				matchedNext := e.matchlen(s2+4, coffsetL+4, src) + 4
				if matchedNext > matched {
					t = coffsetL
					s = s2
					matched = matchedNext
				}
			}
		}

		// A match has been found. Update recent offsets.
		offset2 = offset1
		offset1 = s - t

		l := matched

		// Extend backwards
		tMin := max(s-e.maxMatchOff, 0)
		for t > tMin && s > nextEmit && src[t-1] == src[s-1] && l < maxMatchLength {
			s--
			t--
			l++
		}

		// Write our sequence
		var seq seq
		seq.litLen = uint32(s - nextEmit)
		seq.matchLen = uint32(l - zstdMinMatch)
		if seq.litLen > 0 {
			blk.literals = append(blk.literals, src[nextEmit:s]...)
		}
		seq.offset = uint32(s-t) + 3
		s += l
		blk.sequences = append(blk.sequences, seq)
		nextEmit = s
		if s >= sLimit {
			break encodeLoop
		}

		// Index match start+1 (long) -> s - 1
		off := index0 + e.cur
		for index0 < s-1 {
			cv0 := load6432(src, index0)
			cv1 := cv0 >> 8
			h0 := hashLen(cv0, betterLongTableBits, betterLongLen)
			e.longTable[h0] = prevEntry{offset: off, prev: e.longTable[h0].offset}
			e.table[hashLen(cv1, betterShortTableBits, betterShortLen)] = tableEntry{offset: off + 1, val: uint32(cv1)}
			index0 += 2
			off += 2
		}

		cv = load6432(src, s)
		if !canRepeat {
			continue
		}

		// Check offset 2
		for {
			o2 := s - offset2
			if load3232(src, o2) != uint32(cv) {
				break
			}

			nextHashL := hashLen(cv, betterLongTableBits, betterLongLen)
			nextHashS := hashLen(cv, betterShortTableBits, betterShortLen)

			l := 4 + e.matchlen(s+4, o2+4, src)

			e.longTable[nextHashL] = prevEntry{offset: s + e.cur, prev: e.longTable[nextHashL].offset}
			e.table[nextHashS] = tableEntry{offset: s + e.cur, val: uint32(cv)}
			seq.matchLen = uint32(l) - zstdMinMatch
			seq.litLen = 0

			// Since litlen is always 0, this is offset 1.
			seq.offset = 1
			s += l
			nextEmit = s
			blk.sequences = append(blk.sequences, seq)

			offset1, offset2 = offset2, offset1
			if s >= sLimit {
				break encodeLoop
			}
			cv = load6432(src, s)
		}
	}

	if int(nextEmit) < len(src) {
		blk.literals = append(blk.literals, src[nextEmit:]...)
		blk.extraLits = len(src) - int(nextEmit)
	}
	blk.recentOffsets[0] = uint32(offset1)
	blk.recentOffsets[1] = uint32(offset2)
}

// encodeNoHist compresses src into blk without preserving history between calls.
func (e *betterFastEncoder) encodeNoHist(blk *blockEnc, src []byte) {
	e.ensureHist(len(src))
	e.encode(blk, src)
}

// reset prepares the encoder for a new stream, optionally loading a dictionary.
func (e *betterFastEncoder) reset(d *dict, singleBlock bool) {
	e.resetBase(d, singleBlock)
	if d == nil {
		return
	}
	e.lastDictID = d.id
	e.cur = e.maxMatchOff
	for i := range e.table[:] {
		e.table[i] = tableEntry{}
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
		e.table[hashLen(cv, betterShortTableBits, betterShortLen)] = tableEntry{val: uint32(cv), offset: i}
		e.table[hashLen(cv>>8, betterShortTableBits, betterShortLen)] = tableEntry{val: uint32(cv >> 8), offset: i + 1}
		e.table[hashLen(cv>>16, betterShortTableBits, betterShortLen)] = tableEntry{val: uint32(cv >> 16), offset: i + 2}
		e.table[hashLen(cv>>24, betterShortTableBits, betterShortLen)] = tableEntry{val: uint32(cv >> 24), offset: i + 3}
	}
	cv := load6432(d.content, 0)
	h := hashLen(cv, betterLongTableBits, betterLongLen)
	e.longTable[h] = prevEntry{offset: e.maxMatchOff, prev: e.longTable[h].offset}
	for i := e.maxMatchOff + 1; i < end; i++ {
		cv = cv>>8 | (uint64(d.content[i-e.maxMatchOff+7]) << 56)
		h = hashLen(cv, betterLongTableBits, betterLongLen)
		e.longTable[h] = prevEntry{offset: i, prev: e.longTable[h].offset}
	}
}
