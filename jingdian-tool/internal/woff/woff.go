// Package woff converts WOFF 1.0 fonts into plain sfnt (TTF) byte streams.
//
// The site serves obfuscated fonts as WOFF 1.0, but Go's sfnt parser only
// understands raw sfnt data, so the tables have to be inflated and repacked
// first. Only what sfnt parsing actually needs is reconstructed: the table
// directory and the inflated table bodies.
package woff

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
)

// IsWOFF reports whether data starts with the WOFF 1.0 signature.
func IsWOFF(data []byte) bool {
	return len(data) >= 4 && string(data[:4]) == "wOFF"
}

// IsWOFF2 reports whether data starts with the WOFF 2.0 signature.
func IsWOFF2(data []byte) bool {
	return len(data) >= 4 && string(data[:4]) == "wOF2"
}

// ToSFNT inflates a WOFF 1.0 file into a minimal but valid sfnt stream.
func ToSFNT(data []byte) ([]byte, error) {
	if !IsWOFF(data) {
		return nil, fmt.Errorf("woff: not a WOFF 1.0 file")
	}
	if len(data) < 44 {
		return nil, fmt.Errorf("woff: truncated header")
	}
	flavor := binary.BigEndian.Uint32(data[4:8])
	numTables := int(binary.BigEndian.Uint16(data[12:14]))
	if numTables <= 0 || numTables > 512 {
		return nil, fmt.Errorf("woff: bad table count %d", numTables)
	}
	dirEnd := 44 + numTables*20
	if len(data) < dirEnd {
		return nil, fmt.Errorf("woff: truncated table directory")
	}

	type table struct {
		tag      []byte
		data     []byte
		checksum uint32
	}
	tables := make([]table, 0, numTables)
	for i := 0; i < numTables; i++ {
		off := 44 + i*20
		e := data[off : off+20]
		tag := e[0:4]
		tOff := int(binary.BigEndian.Uint32(e[4:8]))
		compLen := int(binary.BigEndian.Uint32(e[8:12]))
		origLen := int(binary.BigEndian.Uint32(e[12:16]))
		sum := binary.BigEndian.Uint32(e[16:20])
		if tOff < 0 || compLen < 0 || origLen < 0 || tOff+compLen > len(data) || origLen > 1<<26 {
			return nil, fmt.Errorf("woff: bad table bounds for %q", string(tag))
		}
		raw := data[tOff : tOff+compLen]
		var body []byte
		if compLen == origLen {
			body = raw
		} else {
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil, fmt.Errorf("woff: zlib %q: %w", string(tag), err)
			}
			body, err = io.ReadAll(io.LimitReader(zr, int64(origLen)+1))
			zr.Close()
			if err != nil {
				return nil, fmt.Errorf("woff: inflate %q: %w", string(tag), err)
			}
			if len(body) != origLen {
				return nil, fmt.Errorf("woff: %q inflated to %d, want %d", string(tag), len(body), origLen)
			}
		}
		tables = append(tables, table{tag: append([]byte(nil), tag...), data: body, checksum: sum})
	}

	// header + directory + padded table bodies
	headerLen := 12 + numTables*16
	total := headerLen
	for _, t := range tables {
		total += (len(t.data) + 3) &^ 3
	}
	out := make([]byte, headerLen, total)
	binary.BigEndian.PutUint32(out[0:4], flavor)
	binary.BigEndian.PutUint16(out[4:6], uint16(numTables))
	// searchRange / entrySelector / rangeShift
	pow := 1
	for pow*2 <= numTables {
		pow *= 2
	}
	binary.BigEndian.PutUint16(out[6:8], uint16(pow*16))
	binary.BigEndian.PutUint16(out[8:10], uint16(log2(pow)))
	binary.BigEndian.PutUint16(out[10:12], uint16(numTables*16-pow*16))

	offset := headerLen
	for i, t := range tables {
		rec := out[12+i*16 : 12+(i+1)*16]
		copy(rec[0:4], t.tag)
		binary.BigEndian.PutUint32(rec[4:8], uint32(t.checksum))
		binary.BigEndian.PutUint32(rec[8:12], uint32(offset))
		binary.BigEndian.PutUint32(rec[12:16], uint32(len(t.data)))
		out = append(out, t.data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		offset += (len(t.data) + 3) &^ 3
	}
	return out, nil
}

func log2(n int) int {
	i := 0
	for n > 1 {
		n >>= 1
		i++
	}
	return i
}
