# Reviewer Guidelines

## Stack
Go 1.21, nur Standardbibliothek — keine Frameworks. Module `golemic`; nicht-öffentliche Pakete unter `internal/`.

## Verifikation
- `go build ./... && go test ./...` muss grün sein — sonst `changes_requested`.
- `gofmt`, `go vet`, `golangci-lint` (depguard-Layering) müssen sauber sein.
- Diff muss das Issue erfüllen — nicht mehr (kein Scope-Creep), nicht weniger.
- Commits: Conventional Commits mit Slice-Nummer `type(scope): summary (NNN)`.

## Prüfe gegen — Do's
- KISS/YAGNI/DRY eingehalten; kleine, klar benannte Pakete; ein Typ / eine Funktion → eine Verantwortung (SRP je Struct und Paket).
- Kleine Interfaces beim Konsumenten; konkrete Rückgabetypen; Abhängigkeiten explizit injiziert; Zero Values nutzbar.
- Fehler mit `%w` gewrappt; `context.Context` als erster Parameter; Geschäftslogik von HTTP/DB/Infra getrennt.

## Prüfe gegen — Don'ts
- Abstraktionen/Factories/Manager/Wrapper ohne Bedarf; „God Interfaces"; `utils`/`common`/`helpers`; tiefe Paketstrukturen; zyklische Abhängigkeiten.
- Globaler veränderlicher Zustand; versteckte Seiteneffekte; Panics für normale Fehler; ignorierte Fehler; `%v` in Fehlerketten.
- `context.Context` in Structs; Premature Optimization; clevere Einzeiler auf Kosten der Lesbarkeit.

## Verdikt
Genau ein `golemic submit-review --verdict approved|changes_requested`. Bei `changes_requested` konkrete, umsetzbare Punkte nennen.
