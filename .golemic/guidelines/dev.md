# Dev Guidelines

## Stack
Go 1.21, nur Standardbibliothek — keine Frameworks. Module `golemic`; nicht-öffentliche Pakete unter `internal/`. Der Runner ist deterministisch, tool-gesteuert; LLM-Urteil steckt nur in den Rollen-Prompts.

## Build/Test
- Grün-Pflicht: `go build ./... && go test ./...`
- Sauber halten: `gofmt`, `go vet`, `golangci-lint` (depguard erzwingt Import-Layering).
- Neue Logik testgetrieben; Unit-Tests hermetisch (Abhängigkeiten injizieren, kein echtes Netz/GitHub).

## Commits
Conventional Commits mit Slice-Nummer: `type(scope): summary (NNN)` — z.B. `fix(runner): … (018)`.

## Code-Qualität — Do's
- KISS, YAGNI, DRY (Wissen deduplizieren, nicht jede Zeile).
- Kleine, klar benannte Pakete; kleine Interfaces beim Konsumenten definieren; konkrete Typen zurückgeben.
- Abhängigkeiten explizit injizieren; Zero Values nutzbar machen; Komposition statt Abstraktion.
- Fehler mit `%w` und Kontext wrappen; `context.Context` als erster Parameter.
- Geschäftslogik von HTTP, DB und Infrastruktur trennen; ein Typ / eine Funktion → eine Verantwortung.

## Code-Qualität — Don'ts
- Keine Abstraktionen/Factories/Manager/Wrapper ohne konkreten Bedarf; keine „God Interfaces".
- Keine `utils`/`common`/`helpers`-Pakete; keine unnötig tiefen Paketstrukturen; keine zyklischen Abhängigkeiten.
- Kein globaler veränderlicher Zustand; keine versteckten Seiteneffekte.
- Keine Panics für normale Fehler; Fehler nicht ignorieren; Fehlerketten nicht mit `%v` zerstören.
- `context.Context` nicht in Structs speichern; kein Premature Optimization; keine cleveren Einzeiler auf Kosten der Lesbarkeit.
