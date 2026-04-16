// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package huff0

import (
	"fmt"
	"math"
)

// Compress1X compresses in using single-stream Huffman coding.
func Compress1X(in []byte, s *Scratch) (out []byte, reUsed bool, err error) {
	s, err = s.prepare(in)
	if err != nil {
		return nil, false, err
	}
	return compress(in, s, s.compress1X)
}

// Compress4X compresses in using four-stream Huffman coding.
func Compress4X(in []byte, s *Scratch) (out []byte, reUsed bool, err error) {
	s, err = s.prepare(in)
	if err != nil {
		return nil, false, err
	}
	return compress(in, s, s.compress4X)
}

// compress handles table building, reuse logic, and delegates to the compressor function.
func compress(in []byte, s *Scratch, compressor func(src []byte) ([]byte, error)) (out []byte, reUsed bool, err error) {
	if s.Reuse == ReusePolicyNone {
		s.prevTable = s.prevTable[:0]
	}

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

	s.clearCount = true
	s.maxCount = 0
	if maxCount >= len(in) {
		if maxCount > len(in) {
			return nil, false, fmt.Errorf("maxCount (%d) > length (%d)", maxCount, len(in))
		}
		if len(in) == 1 {
			return nil, false, ErrIncompressible
		}
		return nil, false, ErrUseRLE
	}
	if maxCount == 1 || maxCount < (len(in)>>7) {
		return nil, false, ErrIncompressible
	}
	if s.Reuse == ReusePolicyMust && !canReuse {
		return nil, false, ErrIncompressible
	}
	if (s.Reuse == ReusePolicyPrefer || s.Reuse == ReusePolicyMust) && canReuse {
		keepTable := s.cTable
		keepTL := s.actualTableLog
		s.cTable = s.prevTable
		s.actualTableLog = s.prevTableLog
		s.Out, err = compressor(in)
		s.cTable = keepTable
		s.actualTableLog = keepTL
		if err == nil && len(s.Out) < wantSize {
			s.OutData = s.Out
			return s.Out, true, nil
		}
		if s.Reuse == ReusePolicyMust {
			return nil, false, ErrIncompressible
		}
		s.prevTable = s.prevTable[:0]
	}

	err = s.buildCTable()
	if err != nil {
		return nil, false, err
	}

	if s.Reuse == ReusePolicyAllow && canReuse {
		hSize := len(s.Out)
		oldSize := s.prevTable.estimateSize(s.count[:s.symbolLen])
		newSize := s.cTable.estimateSize(s.count[:s.symbolLen])
		if oldSize <= hSize+newSize || hSize+12 >= wantSize {
			keepTable := s.cTable
			keepTL := s.actualTableLog

			s.cTable = s.prevTable
			s.actualTableLog = s.prevTableLog
			s.Out, err = compressor(in)

			s.cTable = keepTable
			s.actualTableLog = keepTL
			if err != nil {
				return nil, false, err
			}
			if len(s.Out) >= wantSize {
				return nil, false, ErrIncompressible
			}
			s.OutData = s.Out
			return s.Out, true, nil
		}
	}

	err = s.cTable.write(s)
	if err != nil {
		s.OutTable = nil
		return nil, false, err
	}
	s.OutTable = s.Out

	s.Out, err = compressor(in)
	if err != nil {
		s.OutTable = nil
		return nil, false, err
	}
	if len(s.Out) >= wantSize {
		s.OutTable = nil
		return nil, false, ErrIncompressible
	}
	s.prevTable, s.prevTableLog, s.cTable = s.cTable, s.actualTableLog, s.prevTable[:0]
	s.OutData = s.Out[len(s.OutTable):]
	return s.Out, false, nil
}

// compress1X compresses src into a single Huffman stream.
func (s *Scratch) compress1X(src []byte) ([]byte, error) {
	return s.compress1xDo(s.Out, src), nil
}

// compress1xDo performs single-stream Huffman encoding of src, appending to dst.
func (s *Scratch) compress1xDo(dst, src []byte) []byte {
	bw := bitWriter{out: dst}

	n := len(src)
	n -= n & 3
	cTable := s.cTable[:256]

	for i := len(src) & 3; i > 0; i-- {
		bw.encSymbol(cTable, src[n+i-1])
	}
	n -= 4
	if s.actualTableLog <= 8 {
		for ; n >= 0; n -= 4 {
			tmp := src[n : n+4]
			bw.flush32()
			bw.encFourSymbols(cTable[tmp[3]], cTable[tmp[2]], cTable[tmp[1]], cTable[tmp[0]])
		}
	} else {
		for ; n >= 0; n -= 4 {
			tmp := src[n : n+4]
			bw.flush32()
			bw.encTwoSymbols(cTable, tmp[3], tmp[2])
			bw.flush32()
			bw.encTwoSymbols(cTable, tmp[1], tmp[0])
		}
	}
	bw.close()
	return bw.out
}

// sixZeros is used as placeholder for the 4-stream segment size header.
var sixZeros [6]byte

// compress4X compresses src into four interleaved Huffman streams.
func (s *Scratch) compress4X(src []byte) ([]byte, error) {
	if len(src) < 12 {
		return nil, ErrIncompressible
	}
	segmentSize := (len(src) + 3) / 4

	offsetIdx := len(s.Out)
	s.Out = append(s.Out, sixZeros[:]...)

	for i := range 4 {
		toDo := src
		if len(toDo) > segmentSize {
			toDo = toDo[:segmentSize]
		}
		src = src[len(toDo):]

		idx := len(s.Out)
		s.Out = s.compress1xDo(s.Out, toDo)
		if len(s.Out)-idx > math.MaxUint16 {
			return nil, ErrIncompressible
		}
		if i < 3 {
			length := len(s.Out) - idx
			s.Out[i*2+offsetIdx] = byte(length)
			s.Out[i*2+offsetIdx+1] = byte(length >> 8)
		}
	}

	return s.Out, nil
}
