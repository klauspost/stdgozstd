// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package fse provides Finite State Entropy encoding.
package fse

import (
	"errors"
	"fmt"
	"math/bits"
)

// FSE table configuration limits.
const (
	maxMemoryUsage     = 14
	defaultMemoryUsage = 13
	maxTableLog        = maxMemoryUsage - 2
	maxTablesize       = 1 << maxTableLog
	defaultTablelog    = defaultMemoryUsage - 2
	minTablelog        = 5
	maxSymbolValue     = 255
)

// Compression sentinel errors.
var (
	ErrIncompressible = errors.New("input is not compressible")
	ErrUseRLE         = errors.New("input is single value repeated")
)

// Scratch holds reusable state for FSE compression and decompression.
type Scratch struct {
	count    [maxSymbolValue + 1]uint32
	norm     [maxSymbolValue + 1]int16
	br       byteReader
	bits     bitReader
	bw       bitWriter
	ct       cTable
	decTable []decSymbol
	maxCount int

	Out             []byte
	DecompressLimit int

	symbolLen      uint16
	actualTableLog uint8
	zeroBits       bool
	clearCount     bool

	MaxSymbolValue uint8
	TableLog       uint8
}

// Histogram returns the symbol count histogram.
func (s *Scratch) Histogram() []uint32 {
	return s.count[:]
}

// HistogramFinished marks the histogram as externally populated.
func (s *Scratch) HistogramFinished(maxSymbol uint8, maxCount int) {
	s.maxCount = maxCount
	s.symbolLen = uint16(maxSymbol) + 1
	s.clearCount = maxCount != 0
}

// prepare initializes scratch with defaults and validates parameters.
func (s *Scratch) prepare(in []byte) (*Scratch, error) {
	if s == nil {
		s = &Scratch{}
	}
	if s.MaxSymbolValue == 0 {
		s.MaxSymbolValue = 255
	}
	if s.TableLog == 0 {
		s.TableLog = defaultTablelog
	}
	if s.TableLog > maxTableLog {
		return nil, fmt.Errorf("tableLog (%d) > maxTableLog (%d)", s.TableLog, maxTableLog)
	}
	if cap(s.Out) == 0 {
		s.Out = make([]byte, 0, len(in))
	}
	if s.clearCount && s.maxCount == 0 {
		for i := range s.count {
			s.count[i] = 0
		}
		s.clearCount = false
	}
	s.br.init(in)
	if s.DecompressLimit == 0 {
		s.DecompressLimit = (2 << 30) - 1
	}
	return s, nil
}

// tableStep returns the step size for spreading symbols across the table.
func tableStep(tableSize uint32) uint32 {
	return (tableSize >> 1) + (tableSize >> 3) + 3
}

// highBits returns the position of the highest set bit (0-indexed).
func highBits(val uint32) (n uint32) {
	return uint32(bits.Len32(val) - 1)
}
