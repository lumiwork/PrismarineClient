package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Meta is what we learned about the plugin from its descriptor files.
type Meta struct {
	Platform    string // paper | spigot | bungee | velocity | fabric | forge | unknown
	Descriptor  string
	Name        string
	Version     string
	Main        string
	APIVersion  string
	Description string
	Authors     []string
	Depend      []string
	SoftDepend  []string
	Raw         map[string]any
}

func listOf(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var o []string
		for _, x := range t {
			o = append(o, fmt.Sprint(x))
		}
		return o
	case map[string]any: // paper-plugin.yml dependencies
		var o []string
		for k := range t {
			o = append(o, k)
		}
		return o
	}
	return nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func parseMeta(files map[string][]byte) Meta {
	m := Meta{Platform: "unknown"}
	for _, d := range []struct{ file, platform string }{
		{"paper-plugin.yml", "paper"}, {"plugin.yml", "spigot"}, {"bungee.yml", "bungee"}, {"velocity-plugin.json", "velocity"},
		{"fabric.mod.json", "fabric"}, {"META-INF/mods.toml", "forge"}, {"META-INF/neoforge.mods.toml", "forge"}, {"mcmod.info", "forge"},
	} {
		data, ok := files[d.file]
		if !ok {
			continue
		}
		m.Platform, m.Descriptor = d.platform, d.file
		raw := map[string]any{}
		switch {
		case strings.HasSuffix(d.file, ".yml"):
			_ = yaml.Unmarshal(data, &raw)
			m.Name, m.Version, m.Main = str(raw["name"]), yamlText(data, "version", raw), str(raw["main"])
			m.APIVersion, m.Description = yamlText(data, "api-version", raw), str(raw["description"])
			m.Authors = append(listOf(raw["author"]), listOf(raw["authors"])...)
			m.Depend, m.SoftDepend = listOf(raw["depend"]), listOf(raw["softdepend"])
			if d.platform == "paper" || d.platform == "spigot" {
				if deps, ok := raw["dependencies"].(map[string]any); ok {
					for _, sec := range deps {
						m.Depend = append(m.Depend, listOf(sec)...)
					}
				}
			}
		case d.file == "velocity-plugin.json" || d.file == "fabric.mod.json":
			_ = json.Unmarshal(data, &raw)
			m.Name, m.Version, m.Description = str(raw["name"]), str(raw["version"]), str(raw["description"])
			if m.Name == "" {
				m.Name = str(raw["id"])
			}
			m.Main = str(raw["main"])
			if ep, ok := raw["entrypoints"].(map[string]any); ok && m.Main == "" {
				for _, v := range ep {
					if l := listOf(v); len(l) > 0 {
						m.Main = l[0]
						break
					}
				}
			}
		default:
			s := string(data)
			m.Name = firstMatch(s, `(?m)displayName\s*=\s*"([^"]+)"`)
			m.Version = firstMatch(s, `(?m)^\s*version\s*=\s*"([^"]+)"`)
		}
		m.Raw = raw
		// Paper plugins that ship both files: prefer paper-plugin.yml but keep plugin.yml's main.
		return m
	}
	return m
}

func firstMatch(s, re string) string {
	if g := regexp.MustCompile(re).FindStringSubmatch(s); len(g) > 1 {
		return g[1]
	}
	return ""
}

// yamlText returns a scalar exactly as written ("1.20" must not become the float 1.2).
func yamlText(data []byte, key string, raw map[string]any) string {
	if g := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*:\s*['"]?([^'"\r\n#]+?)['"]?\s*(?:#.*)?$`).FindSubmatch(data); len(g) > 1 {
		return strings.TrimSpace(string(g[1]))
	}
	return str(raw[key])
}
