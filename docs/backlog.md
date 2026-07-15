# Golemic — Backlog

Abgeleitet aus `docs/architecture.md` (§-Referenzen dorthin). Reihenfolge
korrektheitsgetrieben und dependency-geordnet.

**Offene, implementierungsreife Issues** liegen als validierte
Implementation-Slices unter `docs/backlog/NNN_<slug>.json` (Schema:
`.pi/skills/grill-me/schema.json`). Der numerische Präfix ist die
Bearbeitungsreihenfolge; der dev-loop-Skill greift das niedrigste auf und
löscht das Issue nach Approval im Implementierungs-Commit. Neue Issues
entstehen über den grill-me-Skill.

Dieses Dokument enthält nur noch die **groben späteren Iterationen** — Input
für kommende grill-me-Interviews, noch nicht implementierungsreif.

**Definition of Done für alle Items:** Unit-Tests grün (`go test ./...`),
externe Kommandos (`gh`, `git`, `pi`) nur hinter injizierbaren Interfaces
(§2.12). Der E2E-Smoke-Test gegen `golemic_e2e` ist Sache des Menschen am
Iterationsende, nicht der Items.

Abgeschlossen: Iteration 0 (Preflight-Setup, I0.1–I0.4). Iteration 1
(Loop-Kern, I1.1–I1.11) ist als Issues 001–011 nach `docs/backlog/` migriert.

---

## Iteration 2 — Pingpong (max. 3 Runden) + Eskalation per PR-Kommentar
## Iteration 3 — Autonomes 60s-Polling (`run.sh`, Label `ready-for-agent`)
## Iteration 4 — Human-in-the-Loop-Pickup (`changes_requested` → Dev)
## Iteration 5 — Installer + Setup (Zielpfad, Bot-Tokens → `~/.golemic/<projekt>/credentials.json`, Verbindungstest)
