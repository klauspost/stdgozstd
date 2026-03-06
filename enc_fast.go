// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

// Hash table parameters for the fast encoder.
const (
	tableBits        = 15
	tableSize        = 1 << tableBits
	tableFastHashLen = 6
	maxMatchLength   = 131074
)

// tableEntry stores a hash table match candidate.
type tableEntry struct {
	val    uint32
	offset int32
}

// fastEncoder implements the fast encoder (levels 1-2).
type fastEncoder struct {
	encBase
	table [tableSize]tableEntry
}

// encode compresses src into blk using the fast algorithm with history.
func (e *fastEncoder) encode(blk *blockEnc, src []byte) {
	const (
		inputMargin            = 8
		minNonLiteralBlockSize = 1 + 1 + inputMargin
	)

	// Protect against e.cur wraparound.
	if e.cur >= e.bufferReset-int32(len(e.hist)) {
		if len(e.hist) == 0 {
			for i := range e.table[:] {
				e.table[i] = tableEntry{}
			}
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

	// Override src
	src = e.hist
	sLimit := int32(len(src)) - inputMargin
	const stepSize = 2

	const hashLog = tableBits
	const kSearchStrength = 6

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

		// Don't use repeat offsets across blocks.
		canRepeat := len(blk.sequences) > 2

		for {
			nextHash := hashLen(cv, hashLog, tableFastHashLen)
			nextHash2 := hashLen(cv>>8, hashLog, tableFastHashLen)
			candidate := e.table[nextHash]
			candidate2 := e.table[nextHash2]
			repIndex := s - offset1 + 2

			e.table[nextHash] = tableEntry{offset: s + e.cur, val: uint32(cv)}
			e.table[nextHash2] = tableEntry{offset: s + e.cur + 1, val: uint32(cv >> 8)}

			if canRepeat && repIndex >= 0 && load3232(src, repIndex) == uint32(cv>>16) {
				var seq seq
				length := 4 + e.matchlen(s+6, repIndex+4, src)
				seq.matchLen = uint32(length - zstdMinMatch)

				start := s + 2
				// End search early to avoid 0 literals and special offset treatment.
				startLimit := nextEmit + 1

				sMin := max(s-e.maxMatchOff, 0)
				for repIndex > sMin && start > startLimit && src[repIndex-1] == src[start-1] && seq.matchLen < maxMatchLength-zstdMinMatch {
					repIndex--
					start--
					seq.matchLen++
				}
				addLiterals(&seq, start)

				// rep 0
				seq.offset = 1
				blk.sequences = append(blk.sequences, seq)
				s += length + 2
				nextEmit = s
				if s >= sLimit {
					break encodeLoop
				}
				cv = load6432(src, s)
				continue
			}
			coffset0 := s - (candidate.offset - e.cur)
			coffset1 := s - (candidate2.offset - e.cur) + 1
			if coffset0 < e.maxMatchOff && uint32(cv) == candidate.val {
				t = candidate.offset - e.cur
				break
			}

			if coffset1 < e.maxMatchOff && uint32(cv>>8) == candidate2.val {
				t = candidate2.offset - e.cur
				s++
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

		// Extend backwards
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
		cv = load6432(src, s)

		// Check offset 2
		if o2 := s - offset2; canRepeat && load3232(src, o2) == uint32(cv) {
			l := 4 + e.matchlen(s+4, o2+4, src)

			nextHash := hashLen(cv, hashLog, tableFastHashLen)
			e.table[nextHash] = tableEntry{offset: s + e.cur, val: uint32(cv)}
			seq.matchLen = uint32(l) - zstdMinMatch
			seq.litLen = 0
			// Since litlen is always 0, this is offset 1.
			seq.offset = 1
			s += l
			nextEmit = s
			blk.sequences = append(blk.sequences, seq)

			offset1 = offset2
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
func (e *fastEncoder) encodeNoHist(blk *blockEnc, src []byte) {
	const (
		inputMargin            = 8
		minNonLiteralBlockSize = 1 + 1 + inputMargin
	)

	// Protect against e.cur wraparound.
	if e.cur >= e.bufferReset {
		for i := range e.table[:] {
			e.table[i] = tableEntry{}
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
	const stepSize = 2

	const hashLog = tableBits
	const kSearchStrength = 6

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
			nextHash := hashLen(cv, hashLog, tableFastHashLen)
			nextHash2 := hashLen(cv>>8, hashLog, tableFastHashLen)
			candidate := e.table[nextHash]
			candidate2 := e.table[nextHash2]
			repIndex := s - offset1 + 2

			e.table[nextHash] = tableEntry{offset: s + e.cur, val: uint32(cv)}
			e.table[nextHash2] = tableEntry{offset: s + e.cur + 1, val: uint32(cv >> 8)}

			if len(blk.sequences) > 2 && load3232(src, repIndex) == uint32(cv>>16) {
				var seq seq
				length := 4 + e.matchlen(s+6, repIndex+4, src)
				seq.matchLen = uint32(length - zstdMinMatch)

				start := s + 2
				// End search early to avoid 0 literals and special offset treatment.
				startLimit := nextEmit + 1

				sMin := max(s-e.maxMatchOff, 0)
				for repIndex > sMin && start > startLimit && src[repIndex-1] == src[start-1] {
					repIndex--
					start--
					seq.matchLen++
				}
				addLiterals(&seq, start)

				// rep 0
				seq.offset = 1
				blk.sequences = append(blk.sequences, seq)
				s += length + 2
				nextEmit = s
				if s >= sLimit {
					break encodeLoop
				}
				cv = load6432(src, s)
				continue
			}
			coffset0 := s - (candidate.offset - e.cur)
			coffset1 := s - (candidate2.offset - e.cur) + 1
			if coffset0 < e.maxMatchOff && uint32(cv) == candidate.val {
				t = candidate.offset - e.cur
				break
			}

			if coffset1 < e.maxMatchOff && uint32(cv>>8) == candidate2.val {
				t = candidate2.offset - e.cur
				s++
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

		// Extend backwards
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
		cv = load6432(src, s)

		// Check offset 2
		if o2 := s - offset2; len(blk.sequences) > 2 && load3232(src, o2) == uint32(cv) {
			l := 4 + e.matchlen(s+4, o2+4, src)

			nextHash := hashLen(cv, hashLog, tableFastHashLen)
			e.table[nextHash] = tableEntry{offset: s + e.cur, val: uint32(cv)}
			seq.matchLen = uint32(l) - zstdMinMatch
			seq.litLen = 0
			// Since litlen is always 0, this is offset 1.
			seq.offset = 1
			s += l
			nextEmit = s
			blk.sequences = append(blk.sequences, seq)

			offset1 = offset2
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
	// No history stored; offset e.cur to avoid false matches next call.
	if e.cur < e.bufferReset {
		e.cur += int32(len(src))
	}
}

// reset prepares the encoder for a new stream, optionally loading a dictionary.
func (e *fastEncoder) reset(d *dict, singleBlock bool) {
	e.resetBase(d, singleBlock)
	if d == nil {
		return
	}
	e.lastDictID = d.id
	e.cur = e.maxMatchOff
	const hashLog = tableBits
	if len(d.content) < 8 {
		// Clear table to avoid stale entries from a previous dict.
		for i := range e.table[:] {
			e.table[i] = tableEntry{}
		}
		return
	}
	for i := range e.table[:] {
		e.table[i] = tableEntry{}
	}
	end := int32(len(d.content)) - 8 + e.maxMatchOff
	for i := e.maxMatchOff; i < end; i += 2 {
		cv := load6432(d.content, i-e.maxMatchOff)
		nextHash := hashLen(cv, hashLog, tableFastHashLen)
		nextHash1 := hashLen(cv>>8, hashLog, tableFastHashLen)
		e.table[nextHash] = tableEntry{val: uint32(cv), offset: i}
		e.table[nextHash1] = tableEntry{val: uint32(cv >> 8), offset: i + 1}
	}
}
