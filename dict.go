// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zstd

import (
	"encoding/binary"

	"github.com/klauspost/stdgozstd/internal/huff0"
)

// dict is the internal representation of a dictionary
type dict struct {
	id                  uint32
	litEnc              *huff0.Scratch
	llDec, ofDec, mlDec sequenceDec
	offsets             [3]int
	content             []byte
}

// dictMagic is the four-byte magic number for zstd dictionaries.
const dictMagic = "\x37\xa4\x30\xec"

// ID returns the dictionary ID, or 0 if nil.
func (d *dict) ID() uint32 {
	if d == nil {
		return 0
	}
	return d.id
}

// Dict is a parsed zstd dictionary.
type Dict struct {
	d   *dict
	raw []byte
}

// ParseDict parses a zstd dictionary from its binary representation.
func ParseDict(b []byte) (*Dict, error) {
	initPredefined()
	d, err := loadDict(b)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, len(b))
	copy(raw, b)
	return &Dict{d: d, raw: raw}, nil
}

// ID returns the dictionary ID.
func (d *Dict) ID() uint32 {
	if d == nil || d.d == nil {
		return 0
	}
	return d.d.id
}

// Bytes returns the raw dictionary bytes.
func (d *Dict) Bytes() []byte {
	if d == nil {
		return nil
	}
	return d.raw
}

// MarshalBinary implements encoding.BinaryMarshaler.
func (d *Dict) MarshalBinary() ([]byte, error) {
	b := make([]byte, len(d.raw))
	copy(b, d.raw)
	return b, nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler.
func (d *Dict) UnmarshalBinary(b []byte) error {
	parsed, err := ParseDict(b)
	if err != nil {
		return err
	}
	*d = *parsed
	return nil
}

// loadDict parses a zstd dictionary from its binary representation.
func loadDict(b []byte) (*dict, error) {
	if len(b) <= 8+(3*4) {
		return nil, corruptedError("dictionary too short")
	}
	d := dict{
		llDec: sequenceDec{fse: &fseDecoder{}},
		ofDec: sequenceDec{fse: &fseDecoder{}},
		mlDec: sequenceDec{fse: &fseDecoder{}},
	}
	if string(b[:4]) != dictMagic {
		return nil, errMagicMismatch
	}
	d.id = binary.LittleEndian.Uint32(b[4:8])
	if d.id == 0 {
		return nil, corruptedError("dictionary ID 0")
	}

	var err error
	d.litEnc, b, err = huff0.ReadTable(b[8:], nil)
	if err != nil {
		return nil, &ErrCorrupted{msg: "loading dictionary huffman table", err: err}
	}
	d.litEnc.Reuse = huff0.ReusePolicyMust

	br := byteReader{b: b, off: 0}
	readDec := func(i tableIndex, dec *fseDecoder) error {
		if err := dec.readNCount(&br, uint16(maxTableSymbol[i])); err != nil {
			return err
		}
		if br.overread() {
			return corruptedError("dictionary FSE table truncated")
		}
		if err := dec.transform(symbolTableX[i]); err != nil {
			return err
		}
		dec.preDefined = true
		return nil
	}

	if err := readDec(tableOffsets, d.ofDec.fse); err != nil {
		return nil, err
	}
	if err := readDec(tableMatchLengths, d.mlDec.fse); err != nil {
		return nil, err
	}
	if err := readDec(tableLiteralLengths, d.llDec.fse); err != nil {
		return nil, err
	}
	if br.remain() < 12 {
		return nil, corruptedError("dictionary offsets truncated")
	}

	d.offsets[0] = int(br.Uint32())
	br.advance(4)
	d.offsets[1] = int(br.Uint32())
	br.advance(4)
	d.offsets[2] = int(br.Uint32())
	br.advance(4)
	if d.offsets[0] <= 0 || d.offsets[1] <= 0 || d.offsets[2] <= 0 {
		return nil, corruptedError("invalid offset in dictionary")
	}
	d.content = make([]byte, br.remain())
	copy(d.content, br.unread())
	if d.offsets[0] > len(d.content) || d.offsets[1] > len(d.content) || d.offsets[2] > len(d.content) {
		return nil, corruptedErrorf("initial offset bigger than dictionary content size %d, offsets: %v", len(d.content), d.offsets)
	}

	return &d, nil
}
