// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

// Hash table parameters for the double-fast encoder.
const (
	dFastLongTableBits = 17
	dFastLongTableSize = 1 << dFastLongTableBits
	dFastLongLen       = 8

	dFastShortTableBits = tableBits
	dFastShortTableSize = 1 << dFastShortTableBits
	dFastShortLen       = 5
)

// doubleFastEncoder implements the double-fast encoder (levels 3-4).
type doubleFastEncoder struct {
	fastEncoder
	longTable [dFastLongTableSize]tableEntry
}

// encode compresses src into blk using the double-fast algorithm with history.
func (e *doubleFastEncoder) encode(blk *blockEnc, src []byte) {
	const (
		inputMargin            = 8 + 2
		minNonLiteralBlockSize = 16
	)

	// Protect against e.cur wraparound.
	if e.cur >= e.bufferReset-int32(len(e.hist)) {
		if len(e.hist) == 0 {
			e.table = [dFastShortTableSize]tableEntry{}
			e.longTable = [dFastLongTableSize]tableEntry{}
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
				if v < minOff {
					v = 0
				} else {
					v = v - e.cur + e.maxMatchOff
				}
				e.longTable[i].offset = v
			}
		}
		e.cur = e.maxMatchOff
	}

	s := e.addBlock(src)
	blk.size = len(src)
	if len(src) < minNonLiteralBlockSize {
		blk.extraLits = len(src)
		blk.literals = blk.literals[:len(src)]
		copy(blk.literals, src)
		return
	}

	src = e.hist
	sLimit := int32(len(src)) - inputMargin
	const stepSize = 1
	const kSearchStrength = 8

	nextEmit := s
	cv := load6432(src, s)

	offset1 := int32(blk.recentOffsets[0])
	var offset2 int32

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

		for {
			nextHashL := hashLen(cv, dFastLongTableBits, dFastLongLen)
			nextHashS := hashLen(cv, dFastShortTableBits, dFastShortLen)
			candidateL := e.longTable[nextHashL]
			candidateS := e.table[nextHashS]

			const repOff = 1
			repIndex := s - offset1 + repOff
			entry := tableEntry{offset: s + e.cur, val: uint32(cv)}
			e.longTable[nextHashL] = entry
			e.table[nextHashS] = entry

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

					// rep 0
					seq.offset = 1
					blk.sequences = append(blk.sequences, seq)
					s += length + repOff
					nextEmit = s
					if s >= sLimit {
						break encodeLoop
					}
					cv = load6432(src, s)
					continue
				}
			}
			coffsetL := s - (candidateL.offset - e.cur)
			coffsetS := s - (candidateS.offset - e.cur)

			if coffsetL < e.maxMatchOff && uint32(cv) == candidateL.val {
				t = candidateL.offset - e.cur
				break
			}

			if coffsetS < e.maxMatchOff && uint32(cv) == candidateS.val {
				const checkAt = 1
				cv := load6432(src, s+checkAt)
				nextHashL = hashLen(cv, dFastLongTableBits, dFastLongLen)
				candidateL = e.longTable[nextHashL]
				coffsetL = s - (candidateL.offset - e.cur) + checkAt

				e.longTable[nextHashL] = tableEntry{offset: s + checkAt + e.cur, val: uint32(cv)}
				if coffsetL < e.maxMatchOff && uint32(cv) == candidateL.val {
					t = candidateL.offset - e.cur
					s += checkAt
					break
				}

				t = candidateS.offset - e.cur
				break
			}

			s += stepSize + ((s - nextEmit) >> (kSearchStrength - 1))
			if s >= sLimit {
				break encodeLoop
			}
			cv = load6432(src, s)
		}

		offset2 = offset1
		offset1 = s - t

		l := e.matchlen(s+4, t+4, src) + 4

		tMin := max(s-e.maxMatchOff, 0)
		for t > tMin && s > nextEmit && src[t-1] == src[s-1] && l < maxMatchLength {
			s--
			t--
			l++
		}

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

		// Index match start+1 (long) and start+2 (short)
		index0 := s - l + 1
		// Index match end-2 (long) and end-1 (short)
		index1 := s - 2

		cv0 := load6432(src, index0)
		cv1 := load6432(src, index1)
		te0 := tableEntry{offset: index0 + e.cur, val: uint32(cv0)}
		te1 := tableEntry{offset: index1 + e.cur, val: uint32(cv1)}
		e.longTable[hashLen(cv0, dFastLongTableBits, dFastLongLen)] = te0
		e.longTable[hashLen(cv1, dFastLongTableBits, dFastLongLen)] = te1
		cv0 >>= 8
		cv1 >>= 8
		te0.offset++
		te1.offset++
		te0.val = uint32(cv0)
		te1.val = uint32(cv1)
		e.table[hashLen(cv0, dFastShortTableBits, dFastShortLen)] = te0
		e.table[hashLen(cv1, dFastShortTableBits, dFastShortLen)] = te1

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

			nextHashS := hashLen(cv, dFastShortTableBits, dFastShortLen)
			nextHashL := hashLen(cv, dFastLongTableBits, dFastLongLen)

			l := 4 + e.matchlen(s+4, o2+4, src)

			entry := tableEntry{offset: s + e.cur, val: uint32(cv)}
			e.longTable[nextHashL] = entry
			e.table[nextHashS] = entry
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
func (e *doubleFastEncoder) encodeNoHist(blk *blockEnc, src []byte) {
	const (
		inputMargin            = 8 + 2
		minNonLiteralBlockSize = 16
	)

	// Protect against e.cur wraparound.
	if e.cur >= e.bufferReset {
		for i := range e.table[:] {
			e.table[i] = tableEntry{}
		}
		for i := range e.longTable[:] {
			e.longTable[i] = tableEntry{}
		}
		e.cur = e.maxMatchOff
	}

	s := int32(0)
	blk.size = len(src)
	if len(src) < minNonLiteralBlockSize {
		blk.extraLits = len(src)
		blk.literals = blk.literals[:len(src)]
		copy(blk.literals, src)
		return
	}

	sLimit := int32(len(src)) - inputMargin
	const stepSize = 1
	const kSearchStrength = 8

	nextEmit := s
	cv := load6432(src, s)

	offset1 := int32(blk.recentOffsets[0])
	var offset2 int32

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
		for {
			nextHashL := hashLen(cv, dFastLongTableBits, dFastLongLen)
			nextHashS := hashLen(cv, dFastShortTableBits, dFastShortLen)
			candidateL := e.longTable[nextHashL]
			candidateS := e.table[nextHashS]

			const repOff = 1
			repIndex := s - offset1 + repOff
			entry := tableEntry{offset: s + e.cur, val: uint32(cv)}
			e.longTable[nextHashL] = entry
			e.table[nextHashS] = entry

			if len(blk.sequences) > 2 {
				if load3232(src, repIndex) == uint32(cv>>(repOff*8)) {
					var seq seq
					length := 4 + e.matchlen(s+4+repOff, repIndex+4, src)

					seq.matchLen = uint32(length - zstdMinMatch)

					start := s + repOff
					startLimit := nextEmit + 1

					tMin := max(s-e.maxMatchOff, 0)
					for repIndex > tMin && start > startLimit && src[repIndex-1] == src[start-1] {
						repIndex--
						start--
						seq.matchLen++
					}
					addLiterals(&seq, start)

					// rep 0
					seq.offset = 1
					blk.sequences = append(blk.sequences, seq)
					s += length + repOff
					nextEmit = s
					if s >= sLimit {
						break encodeLoop
					}
					cv = load6432(src, s)
					continue
				}
			}
			coffsetL := s - (candidateL.offset - e.cur)
			coffsetS := s - (candidateS.offset - e.cur)

			if coffsetL < e.maxMatchOff && uint32(cv) == candidateL.val {
				t = candidateL.offset - e.cur
				break
			}

			if coffsetS < e.maxMatchOff && uint32(cv) == candidateS.val {
				const checkAt = 1
				cv := load6432(src, s+checkAt)
				nextHashL = hashLen(cv, dFastLongTableBits, dFastLongLen)
				candidateL = e.longTable[nextHashL]
				coffsetL = s - (candidateL.offset - e.cur) + checkAt

				e.longTable[nextHashL] = tableEntry{offset: s + checkAt + e.cur, val: uint32(cv)}
				if coffsetL < e.maxMatchOff && uint32(cv) == candidateL.val {
					t = candidateL.offset - e.cur
					s += checkAt
					break
				}

				t = candidateS.offset - e.cur
				break
			}

			s += stepSize + ((s - nextEmit) >> (kSearchStrength - 1))
			if s >= sLimit {
				break encodeLoop
			}
			cv = load6432(src, s)
		}

		offset2 = offset1
		offset1 = s - t

		l := e.matchlen(s+4, t+4, src) + 4

		tMin := max(s-e.maxMatchOff, 0)
		for t > tMin && s > nextEmit && src[t-1] == src[s-1] {
			s--
			t--
			l++
		}

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

		// Index match start+1 (long) and start+2 (short)
		index0 := s - l + 1
		// Index match end-2 (long) and end-1 (short)
		index1 := s - 2

		cv0 := load6432(src, index0)
		cv1 := load6432(src, index1)
		te0 := tableEntry{offset: index0 + e.cur, val: uint32(cv0)}
		te1 := tableEntry{offset: index1 + e.cur, val: uint32(cv1)}
		e.longTable[hashLen(cv0, dFastLongTableBits, dFastLongLen)] = te0
		e.longTable[hashLen(cv1, dFastLongTableBits, dFastLongLen)] = te1
		cv0 >>= 8
		cv1 >>= 8
		te0.offset++
		te1.offset++
		te0.val = uint32(cv0)
		te1.val = uint32(cv1)
		e.table[hashLen(cv0, dFastShortTableBits, dFastShortLen)] = te0
		e.table[hashLen(cv1, dFastShortTableBits, dFastShortLen)] = te1

		cv = load6432(src, s)

		if len(blk.sequences) <= 2 {
			continue
		}

		// Check offset 2
		for {
			o2 := s - offset2
			if load3232(src, o2) != uint32(cv) {
				break
			}

			nextHashS := hashLen(cv1>>8, dFastShortTableBits, dFastShortLen)
			nextHashL := hashLen(cv, dFastLongTableBits, dFastLongLen)

			l := 4 + e.matchlen(s+4, o2+4, src)

			entry := tableEntry{offset: s + e.cur, val: uint32(cv)}
			e.longTable[nextHashL] = entry
			e.table[nextHashS] = entry
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
	if e.cur < e.bufferReset {
		e.cur += int32(len(src))
	}
}

// reset prepares the encoder for a new stream, optionally loading a dictionary.
func (e *doubleFastEncoder) reset(d *dict, singleBlock bool) {
	e.fastEncoder.reset(d, singleBlock)
	if d == nil {
		return
	}
	for i := range e.longTable[:] {
		e.longTable[i] = tableEntry{}
	}
	if len(d.content) < 8 {
		return
	}
	cv := load6432(d.content, 0)
	h := hashLen(cv, dFastLongTableBits, dFastLongLen)
	e.longTable[h] = tableEntry{val: uint32(cv), offset: e.maxMatchOff}
	end := int32(len(d.content)) - 8 + e.maxMatchOff
	for i := e.maxMatchOff + 1; i < end; i++ {
		cv = cv>>8 | (uint64(d.content[i-e.maxMatchOff+7]) << 56)
		h = hashLen(cv, dFastLongTableBits, dFastLongLen)
		e.longTable[h] = tableEntry{val: uint32(cv), offset: i}
	}
}
