package main

import (
	"bytes"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Known obfuscator / packer fingerprints (searched in class constants and raw bytes).
var obfMarkers = []struct{ name, marker string }{
	{"Allatori", "ALLATORIxDEMO"}, {"Allatori", "Allatori"},
	{"Zelix KlassMaster", "ZKM"}, {"Zelix KlassMaster", "com/zelix"},
	{"Stringer", "Stringer"}, {"DashO", "PreEmptive"}, {"DashO", "DashO"},
	{"Paramorphism", "paramorphism"}, {"Skidfuscator", "skidfuscator"}, {"Skidfuscator", "dev/skidfuscator"},
	{"Radon", "me/itzsomebody/radon"}, {"Caesium", "xyz/itzsomebody"}, {"qProtect", "qProtect"},
	{"Binscure", "binscure"}, {"Obfuscator (Jnic/JNIC)", "dev/jnic"}, {"JNIC", "jnic"},
	{"BlackObf", "blackobf"}, {"SuperBlaubeere27", "superblaubeere27"}, {"Bozar", "bozar"},
	{"Branchlock", "branchlock"}, {"Grunt", "net/spartanb312/grunt"}, {"ClassGuard/BytecodeViewer-style loader", "ClassLoader.defineClass"},
	{"Sandbox/anti-debug loader", "defineClass"},
}

var knownLibRoots = []string{
	"kotlin/", "kotlinx/", "org/jetbrains/", "org/intellij/", "org/apache/", "com/google/", "com/zaxxer/", "org/slf4j/",
	"net/kyori/", "com/mysql/", "org/mariadb/", "org/sqlite/", "org/postgresql/", "com/fasterxml/", "okhttp3/", "okio/",
	"javassist/", "org/checkerframework/", "redis/clients/", "org/bstats/", "co/aikar/", "com/github/benmanes/",
	"com/mongodb/", "org/bson/", "org/objectweb/", "org/reactivestreams/", "io/reactivex/", "com/j256/", "org/h2/",
	"org/hibernate/", "org/jboss/", "javax/", "jakarta/", "org/json/", "org/yaml/", "com/typesafe/", "org/spongepowered/",
	"io/papermc/lib/", "com/mojang/", "org/jspecify/", "org/jline/", "com/electronwill/", "org/joml/", "net/jodah/",
	"com/squareup/", "io/leangen/", "cloud/commandframework/", "org/incendo/", "dev/triumphteam/", "me/lucko/",
	"com/esotericsoftware/", "org/xerial/", "org/codehaus/", "org/eclipse/", "io/lettuce/", "io/netty/", "reactor/",
	"org/aopalliance/", "com/github/luben/", "org/tukaani/", "net/jpountz/", "org/ow2/", "com/jcraft/", "org/threeten/",
}

var libSegment = regexp.MustCompile(`(^|/)(libs?|shaded|shadow|relocated|thirdparty|third_party|vendor|external|deps|dependencies)(/|$)`)

func isLibClass(internal, mainPrefix string) bool {
	if mainPrefix != "" && strings.HasPrefix(internal, mainPrefix) && !libSegment.MatchString(strings.TrimPrefix(internal, mainPrefix)) {
		return false
	}
	for _, r := range knownLibRoots {
		if strings.HasPrefix(internal, r) {
			return true
		}
	}
	if mainPrefix != "" && strings.HasPrefix(internal, mainPrefix) {
		return libSegment.MatchString(strings.TrimPrefix(internal, mainPrefix))
	}
	return libSegment.MatchString(path.Dir(internal))
}

// mainPackagePrefix returns "de/foo/" (first two package segments) for a main class "de.foo.bar.Main".
func mainPackagePrefix(main string) string {
	p := strings.Split(strings.ReplaceAll(main, ".", "/"), "/")
	if len(p) < 3 {
		return ""
	}
	return strings.Join(p[:2], "/") + "/"
}

// shortOrIllegal reports whether a simple class name looks machine-generated.
func shortOrIllegal(simple string) bool {
	simple = simple[strings.LastIndexAny(simple, "/$")+1:]
	if len(simple) <= 2 {
		return true
	}
	for _, r := range simple {
		if r > 127 || r < 32 {
			return true
		}
	}
	if regexp.MustCompile(`^[Il1O0]+$`).MatchString(simple) {
		return true
	}
	return false
}

type Obf struct {
	ShortRatio        float64
	Tools             []string
	HasProguardFiles  bool
	StringDecryptHint int // classes that look like they carry a string decryptor
}

func analyzeObfuscation(classes map[string]*ClassData, files map[string][]byte, own func(string) bool) Obf {
	var o Obf
	if len(classes) == 0 {
		return o
	}
	short, counted := 0, 0
	found := map[string]bool{}
	for name, c := range classes {
		if own(name) {
			counted++
			if shortOrIllegal(path.Base(name)) {
				short++
			}
		}
		for _, m := range obfMarkers {
			if m.marker == "ZKM" || m.marker == "jnic" || m.marker == "defineClass" || m.marker == "ClassLoader.defineClass" || m.marker == "Stringer" || m.marker == "Allatori" {
				// short/ambiguous markers: only trust if exact constant present
				for _, u := range c.Info.Utf8 {
					if u == m.marker || strings.HasPrefix(u, m.marker+"/") || strings.HasPrefix(u, "ALLATORIxDEMO") {
						found[m.name] = true
					}
				}
				continue
			}
			if bytes.Contains(c.Raw, []byte(m.marker)) {
				found[m.name] = true
			}
		}
		// string decryptors: a static method returning String that takes String/int args plus
		// lots of single-char utf8 constants is typical for ZKM/Allatori/Stringer.
		hasDecr := false
		for _, u := range c.Info.Utf8 {
			if u == "(Ljava/lang/String;I)Ljava/lang/String;" || u == "(Ljava/lang/String;)Ljava/lang/String;" || u == "([C)Ljava/lang/String;" {
				hasDecr = true
			}
		}
		if hasDecr && (bytes.Contains(c.Raw, []byte("toCharArray")) || bytes.Contains(c.Raw, []byte("charAt"))) && shortOrIllegal(path.Base(name)) {
			o.StringDecryptHint++
		}
	}
	if counted > 0 {
		o.ShortRatio = float64(short) / float64(counted)
	}
	for t := range found {
		o.Tools = append(o.Tools, t)
	}
	sort.Strings(o.Tools)
	for f := range files {
		l := strings.ToLower(f)
		if strings.Contains(l, "proguard") || strings.HasSuffix(l, "mapping.txt") {
			o.HasProguardFiles = true
		}
	}
	return o
}

// ClassData is one class collected from an archive.
type ClassData struct {
	Info   ClassInfo
	Raw    []byte
	Origin string // original entry name
	Source string // "plain", "hidden", "carved", "unwrapped:<how>", "nested"
}
