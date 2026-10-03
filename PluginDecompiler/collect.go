package main

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

type Nested struct {
	Name string
	Data []byte
	Sub  *Collected
}

type Collected struct {
	Classes map[string]*ClassData // key: internal name
	Res     map[string][]byte
	Nested  []*Nested
	Notes   []string // interesting findings (hidden classes, unwrapped payloads, ...)
	Corrupt []string
	HighEnt []string
	Dups    int
	MR      int // multi-release classes skipped because a base version exists
}

var reMR = regexp.MustCompile(`^META-INF/versions/\d+/`)
var skipRes = regexp.MustCompile(`(?i)^META-INF/(MANIFEST\.MF|[^/]+\.(SF|RSA|DSA|EC))$`)
var binaryExt = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|ogg|mp3|wav|webp|ttf|otf|mca|nbt|lang|properties|yml|yaml|json|txt|md|xml|html|css|js|conf|toml|csv)$`)

func cleanName(n string) string {
	n = strings.ReplaceAll(n, "\\", "/")
	for strings.HasPrefix(n, "/") || strings.HasPrefix(n, "./") {
		n = strings.TrimPrefix(strings.TrimPrefix(n, "/"), "./")
	}
	return n
}

func collect(data []byte, depth int) (*Collected, error) {
	entries, err := ReadArchive(data)
	if err != nil {
		return nil, err
	}
	c := &Collected{Classes: map[string]*ClassData{}, Res: map[string][]byte{}}
	var versioned []Entry
	var pending []Entry
	for _, e := range entries {
		e.Name = cleanName(e.Name)
		if reMR.MatchString(e.Name) {
			versioned = append(versioned, e)
			continue
		}
		pending = append(pending, e)
	}
	addClass := func(raw []byte, origin, source string) bool {
		ci, ok := ParseClass(raw)
		if !ok {
			return false
		}
		if _, dup := c.Classes[ci.Name]; dup {
			c.Dups++
			return true
		}
		c.Classes[ci.Name] = &ClassData{Info: ci, Raw: raw, Origin: origin, Source: source}
		return true
	}
	var handle func(e Entry, layer string)
	handle = func(e Entry, layer string) {
		lower := strings.ToLower(e.Name)
		src := "plain"
		if layer != "" {
			src = "unwrapped:" + layer
		} else if e.Hidden {
			src = "hidden"
		}
		if raw, ok := CarveClass(e.Data); ok {
			if len(e.Data) >= 4 && string(e.Data[:4]) != string(magicClass) && layer == "" {
				src = "carved"
			}
			if layer == "" && !strings.HasSuffix(lower, ".class") {
				c.Notes = append(c.Notes, fmt.Sprintf("Klasse getarnt als Datei `%s` (%s)", e.Name, src))
				if src == "plain" {
					src = "disguised"
				}
			}
			if layer == "" && e.Hidden {
				c.Notes = append(c.Notes, fmt.Sprintf("Klasse nur per Raw-Scan gefunden (nicht im Zip-Verzeichnis): `%s`", e.Name))
			}
			if addClass(raw, e.Name, src) {
				return
			}
		}
		if len(e.Data) > 4 && string(e.Data[:4]) == string(magicZip) && depth < 4 {
			if sub, err := collect(e.Data, depth+1); err == nil && (len(sub.Classes) > 0 || len(sub.Nested) > 0) {
				c.Nested = append(c.Nested, &Nested{Name: e.Name, Data: e.Data, Sub: sub})
				if layer != "" {
					c.Notes = append(c.Notes, fmt.Sprintf("Versteckte Jar entpackt aus `%s` (%s)", e.Name, layer))
				}
				return
			}
		}
		if strings.HasSuffix(lower, ".class") {
			c.Corrupt = append(c.Corrupt, e.Name)
			return
		}
		if layer == "" && len(e.Data) >= 16 && !binaryExt.MatchString(lower) {
			if x, how, ok := TryUnwrap(e.Data); ok {
				c.Notes = append(c.Notes, fmt.Sprintf("Verschluesselte/gepackte Ressource `%s` entpackt (%s)", e.Name, how))
				handle(Entry{Name: e.Name + ".unwrapped", Data: x}, how)
			} else if len(e.Data) > 256 && Entropy(e.Data) > 7.5 {
				c.HighEnt = append(c.HighEnt, e.Name)
			}
		}
		if layer == "" && !skipRes.MatchString(e.Name) && e.Name != "" {
			c.Res[e.Name] = e.Data
		}
	}
	for _, e := range pending {
		handle(e, "")
	}
	for _, e := range versioned {
		if raw, ok := CarveClass(e.Data); ok {
			if ci, ok := ParseClass(raw); ok {
				if _, exists := c.Classes[ci.Name]; exists {
					c.MR++
				} else {
					c.Classes[ci.Name] = &ClassData{Info: ci, Raw: raw, Origin: e.Name, Source: "multi-release"}
				}
				continue
			}
		}
		if !strings.HasSuffix(strings.ToLower(e.Name), ".class") {
			c.Res[e.Name] = e.Data
		}
	}
	return c, nil
}

func safeRel(n string) string {
	parts := strings.Split(cleanName(n), "/")
	var out []string
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			continue
		}
		p = strings.Map(func(r rune) rune {
			if strings.ContainsRune(`<>:"|?*`, r) || r < 32 {
				return '_'
			}
			return r
		}, p)
		out = append(out, p)
	}
	return path.Join(out...)
}
