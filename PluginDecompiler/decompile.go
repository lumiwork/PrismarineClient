package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const vineflowerVersion = "1.11.1"

func vineflowerURL() string {
	return "https://repo1.maven.org/maven2/org/vineflower/vineflower/" + vineflowerVersion + "/vineflower-" + vineflowerVersion + ".jar"
}

func httpGet(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ensureVineflower returns the path to the decompiler jar (explicit path, next to the exe, or cached download).
func ensureVineflower(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "vineflower.jar")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir := filepath.Join(cache, "PluginDecompiler")
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "vineflower-"+vineflowerVersion+".jar")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	logf("Lade Vineflower %s von Maven Central ...", vineflowerVersion)
	data, err := httpGet(vineflowerURL())
	if err != nil {
		return "", fmt.Errorf("Download fehlgeschlagen (%v). Lege vineflower.jar neben die EXE oder nutze --vineflower <pfad>", err)
	}
	if sum, err := httpGet(vineflowerURL() + ".sha1"); err == nil {
		want := strings.Fields(string(sum))
		h := sha1.Sum(data)
		if len(want) > 0 && want[0] != hex.EncodeToString(h[:]) {
			return "", fmt.Errorf("SHA-1 des Vineflower-Downloads stimmt nicht")
		}
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

func javaMajor() (int, error) {
	out, err := exec.Command("java", "-version").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("Java wurde nicht gefunden. Installiere Java 17 oder neuer (z.B. https://adoptium.net)")
	}
	m := regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`).FindStringSubmatch(string(out))
	if len(m) < 2 {
		return 0, fmt.Errorf("Java-Version nicht erkennbar")
	}
	v, _ := strconv.Atoi(m[1])
	if v == 1 {
		v, _ = strconv.Atoi(m[2])
	}
	return v, nil
}

func writeJar(file string, classes map[string]*ClassData, filter func(string) bool) (int, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	n := 0
	for name, c := range classes {
		if !filter(name) {
			continue
		}
		w, err := zw.Create(name + ".class")
		if err != nil {
			return 0, err
		}
		_, _ = w.Write(c.Raw)
		n++
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return n, os.WriteFile(file, buf.Bytes(), 0o644)
}

type DecompileResult struct {
	Files   int
	Failed  []string
	Output  string
	Renamed bool
}

// decompile runs Vineflower over the given classes and writes the sources to destDir.
func decompile(vf, tmp string, classes map[string]*ClassData, filter func(string) bool, libs []string, rename bool, destDir string) (*DecompileResult, error) {
	in := filepath.Join(tmp, "in.jar")
	n, err := writeJar(in, classes, filter)
	if err != nil {
		return nil, err
	}
	res := &DecompileResult{Renamed: rename}
	if n == 0 {
		return res, nil
	}
	out := filepath.Join(tmp, "out.jar")
	_ = os.Remove(out)
	args := []string{"-Xmx3g", "-jar", vf,
		"-dgs=1", "-asc=1", "-rsy=1", "-rbr=1", "-hdc=0", "-udv=1", "-mpm=90", "-log=WARN", "-ind=    "}
	if rename {
		args = append(args, "-ren=1")
	}
	for _, l := range libs {
		args = append(args, "-e="+l)
	}
	args = append(args, in, out)
	cmd := exec.Command("java", args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Vineflower-Fehler: %v\n%s", err, tail(stderr.String(), 2000))
	}
	res.Output = stderr.String()
	zr, err := zip.OpenReader(out)
	if err != nil {
		return nil, fmt.Errorf("Vineflower hat keine Ausgabe erzeugt: %v", err)
	}
	defer zr.Close()
	used := map[string]bool{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(f.Name, ".java") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		rel := safeRel(f.Name)
		low := strings.ToLower(rel)
		for i := 1; used[low]; i++ { // case-insensitive collision (a.java / A.java)
			rel = strings.TrimSuffix(safeRel(f.Name), ".java") + "_dup" + strconv.Itoa(i) + ".java"
			low = strings.ToLower(rel)
		}
		used[low] = true
		if bytes.Contains(b, []byte("$VF: Couldn't be decompiled")) || bytes.Contains(b, []byte("Unable to decompile class")) || bytes.Contains(b, []byte("$VF: Unable")) {
			res.Failed = append(res.Failed, rel)
		}
		if err := writeFileIn(destDir, rel, b); err != nil {
			return nil, err
		}
		res.Files++
	}
	return res, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func writeFileIn(root, rel string, data []byte) error {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if r, err := filepath.Rel(root, p); err != nil || strings.HasPrefix(r, "..") {
		return fmt.Errorf("unsicherer Pfad: %s", rel)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
