package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

type Options struct {
	Out         string
	Rename      string // auto|always|never
	IncludeLibs bool
	Vineflower  string
	Libs        stringList
	MC          string
	Build       bool
	NoPause     bool
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func logf(f string, a ...any) { fmt.Printf(f+"\n", a...) }

func main() {
	var o Options
	fs := flag.NewFlagSet("plugindecompiler", flag.ExitOnError)
	fs.StringVar(&o.Out, "o", "", "Ausgabeordner (Standard: <jarname>-src neben der Jar)")
	fs.StringVar(&o.Rename, "rename", "auto", "Umbenennen unleserlicher Namen: auto|always|never")
	fs.BoolVar(&o.IncludeLibs, "include-libs", false, "Auch eingebettete Bibliotheken dekompilieren statt als bundled-libs.jar zu behalten")
	fs.StringVar(&o.Vineflower, "vineflower", "", "Pfad zu vineflower.jar (Standard: neben der EXE oder automatischer Download)")
	fs.Var(&o.Libs, "lib", "Zusaetzliche Bibliothek (Jar) fuer bessere Typ-Erkennung, mehrfach moeglich")
	fs.StringVar(&o.MC, "mc", "", "Minecraft-API-Version erzwingen, z.B. 1.20.4")
	fs.BoolVar(&o.Build, "build", false, "Danach 'gradle build' ausfuehren, falls gradle installiert ist")
	fs.BoolVar(&o.NoPause, "no-pause", false, "Am Ende nicht auf Enter warten (Windows)")
	fs.Usage = func() {
		fmt.Println("PluginDecompiler - Minecraft-Plugin (.jar) -> Gradle-Projekt mit Quellcode")
		fmt.Println("Nutzung: plugindecompiler [Optionen] <plugin.jar|ordner> [weitere ...]")
		fmt.Println("Oder eine .jar einfach auf die EXE ziehen.")
		fs.PrintDefaults()
	}
	var inputs []string
	args := os.Args[1:]
	for len(args) > 0 {
		_ = fs.Parse(args)
		if fs.NArg() == 0 {
			break
		}
		inputs = append(inputs, fs.Arg(0))
		args = fs.Args()[1:]
	}
	interactive := len(inputs) == 0
	if interactive {
		fmt.Print("Pfad zur Plugin-Jar (oder hierher ziehen): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		line = strings.Trim(strings.TrimSpace(line), `"'`)
		if line == "" {
			fs.Usage()
			pause(&o, true)
			os.Exit(1)
		}
		inputs = []string{line}
	}
	var jars []string
	for _, in := range inputs {
		if fi, err := os.Stat(in); err == nil && fi.IsDir() {
			m, _ := filepath.Glob(filepath.Join(in, "*.jar"))
			jars = append(jars, m...)
		} else {
			jars = append(jars, in)
		}
	}
	code := 0
	for _, j := range jars {
		logf("\n=== %s ===", j)
		if err := runJar(j, &o, len(jars) > 1); err != nil {
			fmt.Println("FEHLER:", err)
			code = 1
		}
	}
	pause(&o, interactive || runtime.GOOS == "windows")
	os.Exit(code)
}

func pause(o *Options, want bool) {
	if want && !o.NoPause && runtime.GOOS == "windows" {
		fmt.Print("\nEnter druecken zum Beenden ...")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
}

func runJar(jarPath string, o *Options, multi bool) error {
	data, err := os.ReadFile(jarPath)
	if err != nil {
		return err
	}
	base := strings.TrimSuffix(filepath.Base(jarPath), filepath.Ext(jarPath))
	outDir := o.Out
	if outDir == "" {
		outDir = filepath.Join(filepath.Dir(jarPath), base+"-src")
	} else if multi {
		outDir = filepath.Join(outDir, base)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	jv, err := javaMajor()
	if err != nil {
		return err
	}
	if jv < 17 {
		return fmt.Errorf("Java %d gefunden, Vineflower braucht Java 17 oder neuer", jv)
	}
	vf, err := ensureVineflower(o.Vineflower)
	if err != nil {
		return err
	}

	logf("[1/5] Archiv tief scannen (inkl. versteckter, getarnter und verschluesselter Klassen) ...")
	col, err := collect(data, 0)
	if err != nil {
		return fmt.Errorf("Datei ist kein lesbares Archiv: %v", err)
	}
	total := len(col.Classes)
	for _, n := range col.Nested {
		total += len(n.Sub.Classes)
	}
	logf("      %d Klassen, %d Ressourcen, %d verschachtelte Archive", len(col.Classes), len(col.Res), len(col.Nested))
	if total == 0 {
		return fmt.Errorf("keine Klassen gefunden")
	}

	meta := parseMeta(col.Res)
	// Also look for a descriptor inside nested archives (jar-in-jar loaders).
	if meta.Platform == "unknown" {
		for _, n := range col.Nested {
			if m := parseMeta(n.Sub.Res); m.Platform != "unknown" {
				meta = m
				break
			}
		}
	}
	logf("[2/5] Plattform: %s, Name: %s, Main: %s", meta.Platform, meta.Name, meta.Main)

	prefix := mainPackagePrefix(meta.Main)
	isLib := func(n string) bool { return !o.IncludeLibs && isLibClass(n, prefix) }
	own := func(n string) bool { return !isLib(n) }

	obf := analyzeObfuscation(col.Classes, col.Res, own)
	doRename := o.Rename == "always" || (o.Rename == "auto" && (obf.ShortRatio > 0.05 || len(obf.Tools) > 0))
	logf("[3/5] Obfuskierung: %.0f%% unleserliche Klassennamen, Tools: %v, Umbenennen: %v", obf.ShortRatio*100, obf.Tools, doRename)

	tmp, err := os.MkdirTemp("", "plugindec-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// Resources + libs jar
	resDir := filepath.Join(outDir, "src", "main", "resources")
	for name, b := range col.Res {
		rel := safeRel(name)
		if rel == "" || strings.HasPrefix(rel, "META-INF/maven/") || strings.HasPrefix(rel, "META-INF/versions/") {
			continue
		}
		if err := writeFileIn(resDir, rel, b); err != nil {
			return err
		}
	}
	libsDir := filepath.Join(outDir, "libs")
	_ = os.MkdirAll(libsDir, 0o755)
	hasLibsJar := false
	if n, _ := writeJar(filepath.Join(libsDir, "bundled-libs.jar"), col.Classes, isLib); n > 0 {
		hasLibsJar = true
	} else {
		_ = os.Remove(filepath.Join(libsDir, "bundled-libs.jar"))
	}
	var libPaths []string
	if hasLibsJar {
		libPaths = append(libPaths, filepath.Join(libsDir, "bundled-libs.jar"))
	}
	libPaths = append(libPaths, o.Libs...)

	logf("[4/5] Dekompiliere mit Vineflower %s ...", vineflowerVersion)
	srcDir := filepath.Join(outDir, "src", "main", "java")
	ownCount := 0
	maxMajor := 0
	for n, c := range col.Classes {
		if own(n) {
			ownCount++
		}
		if c.Info.Major > maxMajor {
			maxMajor = c.Info.Major
		}
	}
	res, err := decompile(vf, tmp, col.Classes, own, libPaths, doRename, srcDir)
	if err != nil {
		return err
	}
	logf("      %d von %d Klassen als .java geschrieben, %d mit Dekompilier-Problemen", res.Files, ownCount, len(res.Failed))

	// Nested archives: decompile into separate trees (not part of the Gradle build).
	var nestedReport []string
	for i, n := range col.Nested {
		nd := filepath.Join(outDir, "nested-sources", fmt.Sprintf("%02d_%s", i+1, sanitizeName(path.Base(n.Name))))
		_ = os.MkdirAll(filepath.Join(outDir, "libs", "nested"), 0o755)
		_ = os.WriteFile(filepath.Join(outDir, "libs", "nested", fmt.Sprintf("%02d_%s", i+1, sanitizeName(path.Base(n.Name)))), n.Data, 0o644)
		nt := filepath.Join(tmp, fmt.Sprintf("n%d", i))
		_ = os.MkdirAll(nt, 0o755)
		r, err := decompile(vf, nt, n.Sub.Classes, func(string) bool { return true }, nil, doRename, nd)
		if err != nil {
			nestedReport = append(nestedReport, fmt.Sprintf("`%s`: Fehler %v", n.Name, err))
			continue
		}
		nestedReport = append(nestedReport, fmt.Sprintf("`%s`: %d Klassen -> `nested-sources/`", n.Name, r.Files))
	}

	// Fix main class reference if renaming changed it.
	patched := ""
	if meta.Main != "" && doRename {
		if _, err := os.Stat(filepath.Join(srcDir, filepath.FromSlash(strings.ReplaceAll(meta.Main, ".", "/"))+".java")); err != nil {
			if nm := findMainClass(srcDir); nm != "" && nm != meta.Main && meta.Descriptor != "" {
				if patchDescriptor(filepath.Join(resDir, filepath.FromSlash(meta.Descriptor)), meta.Main, nm) {
					patched = fmt.Sprintf("`%s` -> `%s` (wegen Umbenennung)", meta.Main, nm)
				}
			}
		}
	}

	logf("[5/5] Gradle-Projekt erzeugen ...")
	key := meta.Name
	if key == "" {
		key = base
	}
	apiDesc, err := writeGradle(outDir, GradleOpts{Meta: meta, ProjectKey: key, ClassMajor: maxMajor, MCOverride: o.MC, HasLibsJar: hasLibsJar, Imports: scanImports(srcDir)})
	if err != nil {
		return err
	}
	if err := writeReport(outDir, jarPath, meta, col, obf, doRename, res, ownCount, hasLibsJar, nestedReport, patched, apiDesc); err != nil {
		return err
	}
	logf("\nFertig: %s", outDir)
	logf("Bauen: cd \"%s\" && gradle build   (Bericht: DECOMPILE_REPORT.md)", outDir)
	if o.Build {
		if gp, err := exec.LookPath("gradle"); err == nil {
			cmd := exec.Command(gp, "build", "--console=plain")
			cmd.Dir, cmd.Stdout, cmd.Stderr = outDir, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				logf("gradle build meldet Fehler (bei dekompiliertem Code normal, siehe Bericht): %v", err)
			}
		} else {
			logf("gradle nicht gefunden - bitte installieren oder einen Gradle-Wrapper erzeugen.")
		}
	}
	return nil
}

var reMainClass = regexp.MustCompile(`(?m)^public\s+(?:final\s+)?class\s+(\w+)\s+extends\s+(?:\w+\.)*(?:JavaPlugin|Plugin)\b`)
var rePkg = regexp.MustCompile(`(?m)^package\s+([\w.]+);`)

func findMainClass(srcDir string) string {
	best := ""
	_ = filepath.Walk(srcDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || best != "" || fi.IsDir() || !strings.HasSuffix(p, ".java") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if m := reMainClass.FindSubmatch(b); m != nil {
			pkg := ""
			if pm := rePkg.FindSubmatch(b); pm != nil {
				pkg = string(pm[1]) + "."
			}
			best = pkg + string(m[1])
		}
		return nil
	})
	return best
}

func patchDescriptor(file, oldMain, newMain string) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	s := strings.Replace(string(b), oldMain, newMain, 1)
	if s == string(b) {
		return false
	}
	return os.WriteFile(file, []byte(s), 0o644) == nil
}

func writeReport(outDir, jarPath string, m Meta, col *Collected, obf Obf, renamed bool, res *DecompileResult, ownCount int, libs bool, nested []string, patched, api string) error {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("# Dekompilier-Bericht\n")
	w("- Quelle: `%s`", filepath.Base(jarPath))
	w("- Plattform: **%s** (Descriptor: `%s`)", m.Platform, m.Descriptor)
	w("- Name / Version: %s / %s, Main: `%s`, api-version: `%s`", m.Name, m.Version, m.Main, m.APIVersion)
	if len(m.Authors) > 0 {
		w("- Autoren: %s", strings.Join(m.Authors, ", "))
	}
	if len(m.Depend)+len(m.SoftDepend) > 0 {
		w("- Abhaengigkeiten (depend/softdepend): %s — passende Jars bei Compile-Fehlern in `libs/` legen", strings.Join(append(append([]string{}, m.Depend...), m.SoftDepend...), ", "))
	}
	w("- Build-Ziel: %s", api)
	w("\n## Ergebnis\n")
	w("- Eigene Klassen: %d, als .java geschrieben: %d, mit Dekompilier-Problemen: %d", ownCount, res.Files, len(res.Failed))
	if libs {
		w("- Eingebettete Bibliotheken wurden **nicht** dekompiliert, sondern als `libs/bundled-libs.jar` beibehalten (beim Build wieder eingepackt). Mit `--include-libs` werden sie ebenfalls dekompiliert.")
	}
	if patched != "" {
		w("- Main-Klasse im Descriptor angepasst: %s", patched)
	}
	w("\n## Obfuskierung\n")
	w("- Anteil unleserlicher Klassennamen: %.0f%%", obf.ShortRatio*100)
	if len(obf.Tools) > 0 {
		w("- Erkannte Tools/Spuren: %s", strings.Join(obf.Tools, ", "))
	}
	if obf.HasProguardFiles {
		w("- ProGuard/Mapping-Dateien im Jar gefunden")
	}
	if obf.StringDecryptHint > 0 {
		w("- **%d Klassen mit Verdacht auf String-Verschluesselung.** Vineflower entschluesselt Strings nicht, die Decrypt-Methoden bleiben im Code stehen und funktionieren beim Kompilieren weiter.", obf.StringDecryptHint)
	}
	if renamed {
		w("- Unleserliche Namen wurden automatisch umbenannt (`class_N`, `method_N`, `field_N`). Die Originalnamen sind nach ProGuard-Umbenennung verloren, das laesst sich nicht rekonstruieren. Reflection/Strings, die alte Namen enthalten, muessen von Hand angepasst werden.")
	}
	w("\n## Gefundene Besonderheiten\n")
	if len(col.Notes)+len(col.Corrupt)+len(col.HighEnt)+len(nested) == 0 && col.Dups == 0 {
		w("Keine.")
	}
	for _, n := range col.Notes {
		w("- %s", n)
	}
	for _, n := range nested {
		w("- Verschachteltes Archiv: %s", n)
	}
	for _, n := range col.Corrupt {
		w("- Beschaedigte/absichtlich kaputte Klasse uebersprungen: `%s`", n)
	}
	for _, n := range col.HighEnt {
		w("- Ressource mit hoher Entropie (evtl. verschluesselte Nutzlast, die zur Laufzeit per eigenem Key geladen wird): `%s`", n)
	}
	if col.Dups > 0 {
		w("- %d doppelte Klassen-Eintraege ignoriert", col.Dups)
	}
	if col.MR > 0 {
		w("- %d Multi-Release-Klassen (META-INF/versions) ignoriert, weil es eine Basisversion gibt", col.MR)
	}
	if len(res.Failed) > 0 {
		w("\n## Klassen mit Dekompilier-Problemen\n")
		sort.Strings(res.Failed)
		for _, f := range res.Failed {
			w("- `%s`", f)
		}
	}
	w("\n## Grenzen\n")
	w("- Dekompilierter Code kompiliert nicht immer fehlerfrei; bei starker Obfuskierung (Flow-Obfuskierung, native Code, Laufzeit-Entschluesselung mit externem Key) sind manuelle Korrekturen noetig.")
	w("- Verschluesselung, deren Schluessel nur zur Laufzeit existiert (Lizenzserver, Native-Loader), kann statisch nicht geknackt werden.")
	w("- Nur fuer eigene Plugins, Interop/Sicherheitsanalyse oder wenn die Lizenz es erlaubt verwenden.")
	return os.WriteFile(filepath.Join(outDir, "DECOMPILE_REPORT.md"), []byte(b.String()), 0o644)
}
