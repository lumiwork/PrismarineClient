package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Latest published API patch per Minecraft minor version.
var apiPatch = map[int]string{
	8: "1.8.8", 9: "1.9.4", 10: "1.10.2", 11: "1.11.2", 12: "1.12.2", 13: "1.13.2", 14: "1.14.4", 15: "1.15.2",
	16: "1.16.5", 17: "1.17.1", 18: "1.18.2", 19: "1.19.4", 20: "1.20.6", 21: "1.21.4",
}

func minorOf(apiVersion string, classMajor int) int {
	if m := regexp.MustCompile(`^1\.(\d+)`).FindStringSubmatch(apiVersion); len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	switch {
	case classMajor >= 65:
		return 21
	case classMajor >= 61:
		return 18
	case classMajor >= 60:
		return 17
	case classMajor >= 55:
		return 16
	}
	return 12 // Java 8 era plugin without api-version
}

type thirdParty struct{ prefix, dep, repo string }

var thirdParties = []thirdParty{
	{"me.clip.placeholderapi", "me.clip:placeholderapi:2.11.6", "https://repo.extendedclip.com/releases/"},
	{"net.milkbowl.vault", "com.github.MilkBowl:VaultAPI:1.7.1", "https://jitpack.io"},
	{"com.comphenix.protocol", "com.comphenix.protocol:ProtocolLib:5.1.0", "https://repo.dmulloy2.net/repository/public/"},
	{"net.luckperms.api", "net.luckperms:api:5.4", ""},
	{"com.sk89q.worldedit", "com.sk89q.worldedit:worldedit-bukkit:7.2.15", "https://maven.enginehub.org/repo/"},
	{"com.sk89q.worldguard", "com.sk89q.worldguard:worldguard-bukkit:7.0.9", "https://maven.enginehub.org/repo/"},
}

func scanImports(srcDir string) map[string]bool {
	found := map[string]bool{}
	_ = filepath.Walk(srcDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".java") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, tp := range thirdParties {
			if strings.Contains(string(b), "import "+tp.prefix) || strings.Contains(string(b), "import static "+tp.prefix) {
				found[tp.prefix] = true
			}
		}
		return nil
	})
	return found
}

type GradleOpts struct {
	Meta       Meta
	ProjectKey string
	ClassMajor int
	MCOverride string
	HasLibsJar bool
	Imports    map[string]bool
}

func writeGradle(root string, g GradleOpts) (apiDesc string, err error) {
	m := g.Meta
	release := g.ClassMajor - 44
	if release < 8 {
		release = 8
	}
	minor := minorOf(m.APIVersion, g.ClassMajor)
	version := apiPatch[minor]
	if g.MCOverride != "" {
		version = g.MCOverride
		if mm := regexp.MustCompile(`^1\.(\d+)`).FindStringSubmatch(version); len(mm) > 1 {
			minor, _ = strconv.Atoi(mm[1])
		}
	}
	if version == "" {
		version = apiPatch[21]
	}
	var repos, deps []string
	repos = append(repos, `mavenCentral()`)
	switch m.Platform {
	case "bungee":
		repos = append(repos, `maven("https://repo.papermc.io/repository/maven-public/")`, `maven("https://oss.sonatype.org/content/repositories/snapshots/")`)
		deps = append(deps, `compileOnly("io.github.waterfallmc:waterfall-api:1.20-R0.2-SNAPSHOT")`)
		apiDesc = "Waterfall/BungeeCord API 1.20"
	case "velocity":
		repos = append(repos, `maven("https://repo.papermc.io/repository/maven-public/")`)
		deps = append(deps, `compileOnly("com.velocitypowered:velocity-api:3.3.0-SNAPSHOT")`, `annotationProcessor("com.velocitypowered:velocity-api:3.3.0-SNAPSHOT")`)
		apiDesc = "Velocity API 3.3.0"
	case "paper", "spigot":
		if minor <= 15 {
			repos = append(repos, `maven("https://hub.spigotmc.org/nexus/content/repositories/snapshots/")`)
			deps = append(deps, fmt.Sprintf(`compileOnly("org.spigotmc:spigot-api:%s-R0.1-SNAPSHOT")`, version))
			apiDesc = "Spigot API " + version
		} else {
			repos = append(repos, `maven("https://repo.papermc.io/repository/maven-public/")`)
			deps = append(deps, fmt.Sprintf(`compileOnly("io.papermc.paper:paper-api:%s-R0.1-SNAPSHOT")`, version))
			apiDesc = "Paper API " + version
		}
		if minor >= 17 && release < 17 {
			release = 17
		}
		if minor >= 21 && release < 21 {
			release = 21
		}
	default:
		apiDesc = "keine Plattform-API (Plattform nicht erkannt: Jars in libs/ ablegen)"
	}
	seenRepo := map[string]bool{}
	for _, tp := range thirdParties {
		if g.Imports[tp.prefix] {
			if tp.repo != "" && !seenRepo[tp.repo] {
				repos = append(repos, fmt.Sprintf(`maven("%s")`, tp.repo))
				seenRepo[tp.repo] = true
			}
			deps = append(deps, fmt.Sprintf(`compileOnly("%s")`, tp.dep))
		}
	}
	deps = append(deps, `compileOnly(fileTree("libs") { include("*.jar"); exclude("bundled-libs.jar") })`)
	jarBlock := ""
	if g.HasLibsJar {
		deps = append(deps, `implementation(files("libs/bundled-libs.jar"))`)
		jarBlock = `
// Die im Original eingebetteten (shaded) Bibliotheken wieder mit ins Jar packen.
tasks.jar {
    duplicatesStrategy = DuplicatesStrategy.EXCLUDE
    from({ zipTree("libs/bundled-libs.jar") }) { exclude("META-INF/*.SF", "META-INF/*.RSA", "META-INF/*.DSA") }
}
`
	}
	sort.Strings(repos[1:])
	name := sanitizeName(g.ProjectKey)
	ver := m.Version
	if ver == "" {
		ver = "1.0.0"
	}
	build := fmt.Sprintf(`plugins {
    java
}

group = "decompiled"
version = %q

repositories {
    %s
}

dependencies {
    %s
}

tasks.withType<JavaCompile>().configureEach {
    options.encoding = "UTF-8"
    options.release.set(%d)
    options.compilerArgs.addAll(listOf("-nowarn", "-Xmaxerrs", "1000"))
}
%s`, ver, strings.Join(repos, "\n    "), strings.Join(deps, "\n    "), release, jarBlock)
	if err := os.WriteFile(filepath.Join(root, "build.gradle.kts"), []byte(build), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "settings.gradle.kts"), []byte(fmt.Sprintf("rootProject.name = %q\n", name)), 0o644); err != nil {
		return "", err
	}
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".gradle/\nbuild/\n.idea/\n*.iml\n"), 0o644)
	return apiDesc + fmt.Sprintf(", Java %d", release), nil
}

func sanitizeName(s string) string {
	s = regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "decompiled-plugin"
	}
	return s
}
