# PluginDecompiler

Windows-EXE (`plugindecompiler.exe`): Minecraft-Plugin (`.jar`) rein, **baubares Gradle-Projekt mit Quellcode** raus.

## Benutzung

- `.jar` per Drag & Drop auf die EXE ziehen, oder
- `plugindecompiler.exe [Optionen] plugin.jar` (auch ein Ordner mit Jars geht)

Voraussetzung: **Java 17+**. Der Dekompiler (Vineflower) wird beim ersten Start automatisch von Maven Central geladen (SHA-1-geprüft) oder liegt als `vineflower.jar` neben der EXE.

Ausgabe `<name>-src/`:

```
build.gradle.kts, settings.gradle.kts   -> gradle build
src/main/java        dekompilierter Quellcode
src/main/resources   plugin.yml, config.yml, alle anderen Dateien
libs/bundled-libs.jar  eingebettete (shaded) Bibliotheken, beim Build wieder eingepackt
libs/nested, nested-sources/  Jars in der Jar (auch getarnte)
DECOMPILE_REPORT.md  was gefunden wurde, was nicht dekompiliert werden konnte
```

Optionen: `-o <ordner>`, `--rename auto|always|never`, `--include-libs`, `--lib <jar>` (Abhängigkeits-Jars für bessere Typen), `--mc 1.20.4`, `--build`, `--vineflower <pfad>`.

## Was es findet / entschlüsselt

- Klassen, die im Zip-Verzeichnis fehlen (Raw-Scan der Local-Header), kaputtes/abgeschnittenes Zip, Junk vor dem Zip
- Klassen mit falscher Endung (`.png`, `.dat`, ...) per `CAFEBABE`-Magic, auch mit Offset
- Versteckte Nutzlasten in Ressourcen: gzip, zlib, deflate, base64, Ein-Byte-XOR → Klassen oder Jars
- Jar-in-Jar (rekursiv), Multi-Release-Klassen
- Plattform-Erkennung (Paper/Spigot/Bukkit, Bungee/Waterfall, Velocity) und passende API-Dependency
- ProGuard & Co.: unleserliche Namen werden von Vineflower umbenannt (`class_N`, `method_N`, ...), die Main-Klasse in `plugin.yml` wird dabei nachgezogen. Erkennung von Allatori, ZKM, Stringer, Skidfuscator, Paramorphism u. a.

## Grenzen (ehrlich)

- **"Alles entschlüsseln" ist nicht möglich.** Originalnamen nach ProGuard-Umbenennung sind weg (nur mit `mapping.txt` rekonstruierbar, das wird nicht angewendet). String-Verschlüsselung und Flow-Obfuskierung kommerzieller Tools werden nicht rückgängig gemacht (der Bericht warnt davor). Was nur zur Laufzeit mit Schlüssel/Lizenzserver/Native-Code entschlüsselt wird, ist statisch nicht lösbar.
- Dekompilierter Code kompiliert nicht immer ohne Handarbeit; fehlende Plugin-Abhängigkeiten (Vault, ...) als Jar in `libs/` legen.
- Nur für eigene Plugins, Sicherheitsanalyse/Interop oder wenn die Lizenz es erlaubt.

## Selbst bauen

```
go build -ldflags="-s -w" -o plugindecompiler.exe .   # GOOS=windows GOARCH=amd64
```
