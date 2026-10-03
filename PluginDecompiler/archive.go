package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"io"
	"math"
	"strings"
)

// Entry is one file found inside an archive (by central directory or by raw scan).
type Entry struct {
	Name   string
	Data   []byte
	Hidden bool // found only by raw local-header scan (not listed in central directory)
}

var (
	magicClass = []byte{0xCA, 0xFE, 0xBA, 0xBE}
	magicZip   = []byte{'P', 'K', 3, 4}
)

// ReadArchive reads every entry it can. It uses the central directory first and
// afterwards scans for local file headers, so entries that an obfuscator removed
// from the central directory, or a damaged directory, still yield their content.
func ReadArchive(data []byte) ([]Entry, error) {
	var out []Entry
	seen := map[string]int{}
	add := func(e Entry) {
		key := e.Name
		if n := seen[key]; n > 0 {
			e.Name = key + "#dup" + itoa(n)
		}
		seen[key]++
		out = append(out, e)
	}
	var cdErr error
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err == nil {
		for _, f := range zr.File {
			if strings.HasSuffix(f.Name, "/") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(rc, 1<<30))
			rc.Close()
			add(Entry{Name: f.Name, Data: b})
		}
	} else {
		cdErr = err
	}
	known := map[string]bool{}
	for _, e := range out {
		known[e.Name] = true
	}
	for _, e := range scanLocalHeaders(data) {
		if !known[e.Name] {
			e.Hidden = true
			add(e)
		}
	}
	if len(out) == 0 && cdErr != nil {
		return nil, cdErr
	}
	return out, nil
}

func scanLocalHeaders(data []byte) []Entry {
	var out []Entry
	pos := 0
	for {
		i := bytes.Index(data[pos:], magicZip)
		if i < 0 {
			break
		}
		p := pos + i
		pos = p + 4
		if p+30 > len(data) {
			break
		}
		h := data[p:]
		flags := binary.LittleEndian.Uint16(h[6:])
		method := binary.LittleEndian.Uint16(h[8:])
		csize := int(binary.LittleEndian.Uint32(h[18:]))
		usize := int(binary.LittleEndian.Uint32(h[22:]))
		nlen := int(binary.LittleEndian.Uint16(h[26:]))
		xlen := int(binary.LittleEndian.Uint16(h[28:]))
		start := p + 30 + nlen + xlen
		if nlen == 0 || start > len(data) {
			continue
		}
		name := string(h[30 : 30+nlen])
		if strings.HasSuffix(name, "/") || flags&1 != 0 {
			continue
		}
		var body []byte
		switch method {
		case 0:
			if usize <= 0 || start+usize > len(data) {
				continue
			}
			body = data[start : start+usize]
		case 8:
			end := len(data)
			if csize > 0 && start+csize <= len(data) {
				end = start + csize
			}
			b, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(data[start:end])), 1<<30))
			if err != nil && len(b) == 0 {
				continue
			}
			body = b
		default:
			continue
		}
		out = append(out, Entry{Name: name, Data: body})
	}
	return out
}

// CarveClass returns class bytes if data is, or contains near its start, a class file.
func CarveClass(data []byte) ([]byte, bool) {
	if i := bytes.Index(data[:min(len(data), 8192)], magicClass); i >= 0 {
		c := data[i:]
		if _, ok := ParseClass(c); ok {
			return c, true
		}
	}
	return nil, false
}

// ClassInfo is the minimum we need from a class file.
type ClassInfo struct {
	Major int
	Name  string // internal name, a/b/C
	Super string
	Utf8  []string
}

func ParseClass(b []byte) (ci ClassInfo, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	if len(b) < 10 || !bytes.Equal(b[:4], magicClass) {
		return ci, false
	}
	ci.Major = int(binary.BigEndian.Uint16(b[6:]))
	if ci.Major < 45 || ci.Major > 80 {
		return ci, false
	}
	n := int(binary.BigEndian.Uint16(b[8:]))
	p := 10
	utf := make([]string, n)
	cls := make([]int, n)
	for i := 1; i < n; i++ {
		tag := b[p]
		p++
		switch tag {
		case 1:
			l := int(binary.BigEndian.Uint16(b[p:]))
			utf[i] = string(b[p+2 : p+2+l])
			p += 2 + l
		case 3, 4, 9, 10, 11, 12, 17, 18:
			p += 4
		case 5, 6:
			p += 8
			i++
		case 7:
			cls[i] = int(binary.BigEndian.Uint16(b[p:]))
			p += 2
		case 8, 16, 19, 20:
			p += 2
		case 15:
			p += 3
		default:
			return ci, false
		}
	}
	p += 2
	this := int(binary.BigEndian.Uint16(b[p:]))
	sup := int(binary.BigEndian.Uint16(b[p+2:]))
	ci.Name = utf[cls[this]]
	if sup != 0 {
		ci.Super = utf[cls[sup]]
	}
	if ci.Name == "" {
		return ci, false
	}
	for _, s := range utf {
		if s != "" {
			ci.Utf8 = append(ci.Utf8, s)
		}
	}
	return ci, true
}

func Entropy(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var c [256]int
	for _, x := range b {
		c[x]++
	}
	var h float64
	for _, n := range c {
		if n > 0 {
			p := float64(n) / float64(len(b))
			h -= p * math.Log2(p)
		}
	}
	return h
}

// TryUnwrap attempts common cheap "encryption/packing" layers used to hide
// classes or jars inside resources: gzip, zlib, raw deflate, base64 and
// single-byte XOR. Returns the unwrapped payload and a description.
func TryUnwrap(b []byte) ([]byte, string, bool) {
	isPayload := func(x []byte) bool {
		if bytes.HasPrefix(x, magicZip) {
			return true
		}
		_, ok := CarveClass(x)
		return ok
	}
	if len(b) < 16 {
		return nil, "", false
	}
	if r, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
		if x, _ := io.ReadAll(r); isPayload(x) {
			return x, "gzip", true
		}
	}
	if r, err := zlib.NewReader(bytes.NewReader(b)); err == nil {
		if x, _ := io.ReadAll(r); isPayload(x) {
			return x, "zlib", true
		}
	}
	if x, _ := io.ReadAll(flate.NewReader(bytes.NewReader(b))); isPayload(x) {
		return x, "deflate", true
	}
	if x, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b))); err == nil && isPayload(x) {
		return x, "base64", true
	}
	head := b[:min(len(b), 64)]
	for k := 1; k < 256; k++ {
		t := make([]byte, len(head))
		for i, c := range head {
			t[i] = c ^ byte(k)
		}
		if bytes.HasPrefix(t, magicClass) || bytes.HasPrefix(t, magicZip) {
			x := make([]byte, len(b))
			for i, c := range b {
				x[i] = c ^ byte(k)
			}
			if isPayload(x) {
				return x, "xor-0x" + hex2(byte(k)), true
			}
		}
	}
	return nil, "", false
}

func hex2(b byte) string {
	const d = "0123456789abcdef"
	return string([]byte{d[b>>4], d[b&15]})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}
