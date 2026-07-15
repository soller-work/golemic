# Golemic — Backlog

Abgeleitet aus `docs/architecture.md` (§-Referenzen dorthin). Reihenfolge
korrektheitsgetrieben und dependency-geordnet — jedes Item baut nur auf früheren
auf. Wir beginnen mit **Iteration 0**.

**Definition of Done für alle Items:** Unit-Tests grün (`go test ./...`),
externe Kommandos (`gh`, `git`, `pi`) nur hinter injizierbaren Interfaces
(§2.12). Der E2E-Smoke-Test gegen `golemic_e2e` ist Sache des Menschen am
Iterationsende, nicht der Items.

---

## Iteration 0 — Preflight-Setup

Ziel: Idempotentes, wiederholt ausführbares `golemic preflight`, das alle
Voraussetzungen prüft und mit `SUCCESS` (Exit 0) endet — sonst klar meldet, was
fehlt. Keine Loop-Logik.

### I0.1 — Repo-Aufräumung + Go-Skelett
- **Goal:** golemic wird reines Werkzeug-Repo (§2.7).
- **Scope:** `build.gradle.kts`, `settings.gradle.kts`, `gradle/`, `gradlew*`,
  `.gradle/` entfernen; `.gitignore` für Go neu schreiben; `go.mod` (Modul
  `golemic`), Struktur `cmd/golemic/main.go` (Cobra o.ä. optional, `flag` reicht),
  `internal/`, `prompts/` anlegen; `main.go` mit Subcommand-Dispatch-Skelett
  (`preflight`, `run`, `emit`, `open-pr`, `submit-review` — vorerst alle "not
  implemented", Exit ≠ 0).
- **Acceptance:** `go build ./...` und `go test ./...` grün; `golemic preflight`
  antwortet "not implemented" mit Exit ≠ 0; keine Gradle-/Kotlin-Artefakte mehr
  im Repo.
- **Out of scope:** jegliche Logik hinter den Subcommands.

### I0.2 — `.golemic/config.json`: Schema + Loader
- **Goal:** Eine maschinenlesbare Konfigurationsquelle im Host-Repo (§2.9).
- **Scope:** Go-Struct + Loader für `.golemic/config.json` mit Feldern:
  - `project` (string, Pflicht) — Schlüssel für `~/.golemic/<projekt>/`
  - `verify_command` (string, Pflicht) — Shell-Kommando, Exit 0 = verifiziert
  - `label` (string, Default `ready-for-agent`)
  - `models.dev` / `models.reviewer` (string, mit Default)
  - `timeout_minutes` (int, Default 30) — pro Rollen-Lauf
  Validierung: fehlende Pflichtfelder, unbekannte Felder, kaputtes JSON → je eine
  klare, benannte Fehlermeldung (Feldname + Erwartung).
- **Acceptance:** Tabellengetriebene Tests: valide Config lädt mit Defaults;
  jede Fehlklasse liefert die spezifische Meldung; Loader nimmt den Pfad zur
  Repo-Wurzel als Parameter (kein globaler Zustand).
- **Out of scope:** Anlegen der Datei (I0.4), Credentials.

### I0.3 — Credentials-Loader (`~/.golemic/<projekt>/credentials.json`)
- **Goal:** Bot-Tokens laden, ohne dass sie je im Repo liegen können (§2.9).
- **Scope:** Loader für `~/.golemic/<projekt>/credentials.json` mit Feldern
  `dev_token`, `reviewer_token`. Regeln:
  1. Env-Vars `GOLEMIC_DEV_TOKEN` / `GOLEMIC_REVIEWER_TOKEN` haben **Vorrang**
     vor der Datei (einzeln, pro Token).
  2. Dateirechte-Check: group/world-readable → Fehler mit Hinweis auf `chmod 600`.
  3. Fehlt ein Token aus beiden Quellen → Fehler, der beide Bezugsquellen nennt.
  Home-Verzeichnis injizierbar (Testbarkeit).
- **Acceptance:** Tests für alle Kombinationen (nur Datei, nur Env, Mischform,
  Rechte-Verstoß, fehlend); niemals wird ein Token-Wert in einer Fehlermeldung
  oder einem Log ausgegeben.
- **Out of scope:** Token-Gültigkeitsprüfung gegen GitHub (I0.4), Schreiben der
  Datei (Iteration 5).

### I0.4 — `golemic preflight`: Checks + `.golemic/`-Scaffolding
- **Goal:** Alle Voraussetzungen prüfen, `SUCCESS` am Ende; fehlende
  `.golemic/`-Templates anlegen (§3, Iteration 0).
- **Scope:** Checks in fester Reihenfolge, jede mit `OK:`/`FEHLT:`-Zeile:
  1. `gh` installiert (`gh --version`).
  2. `pi` installiert (`pi --version`).
  3. `git` installiert, Worktree-Support (`git worktree list` funktioniert),
     Aufruf aus git-Repo mit Remote `origin`, Remote-URL ist **HTTPS** (§2.4).
  4. `.golemic/` vorhanden — sonst **anlegen**: `config.json` mit Defaults
     (Projekt-Name aus Repo-Verzeichnisnamen vorbelegt), `guidelines/dev.md` und
     `guidelines/reviewer.md` als Skelette mit Pflicht-Abschnitten (Stack,
     Build/Test, Schranken) + Hinweis "vom Menschen auszufüllen". Anlegen zählt
     als `FEHLT` (Exit ≠ 0), damit der Mensch die Templates erst ausfüllt.
  5. `config.json` valide (I0.2).
  6. Credentials ladbar (I0.3), beide Tokens gültig (`gh auth status` bzw.
     `gh api user` je Token) und auf **verschiedene** Logins auflösend (§2.8).
- **Acceptance:** Vollständiges Setup → letzte Zeile exakt `SUCCESS`, Exit 0.
  Sonst Liste aller fehlenden Punkte (nicht nur der erste), Exit ≠ 0. Zweiter
  Lauf hintereinander ist idempotent (legt nichts doppelt an, überschreibt keine
  vom Menschen editierten Dateien). Externe Kommandos gefaked in Tests.
- **Out of scope:** Tokens erzeugen (Iteration 5), Loop-Logik.

---

## Iteration 1 — Loop-Kern (MVP)

`golemic run --issue N`: Dev-Worktree → Dev→PR → Reviewer-Worktree →
Reviewer→Review, **eine** Runde, Event-Log. Wirkt auf das Host-Repo.

### I1.1 — Event-Log: JSONL-Writer/-Reader + Env-Var-Vertrag
- **Goal:** Append-only Event-Log als einzige interne Wahrheit (§2.6).
- **Scope:** Paket `internal/eventlog`:
  - Event-Struct: `type`, `ts` (RFC3339), `runId`, `payload` (JSON-Objekt).
  - Writer: append-only nach `~/.golemic/<projekt>/runs/<runId>/events.jsonl`
    (Verzeichnis anlegen); eine Zeile pro Event; `O_APPEND`.
  - Reader: Datei einlesen, malformed Zeilen → Fehler (fail-closed, kein
    Überspringen); Helfer `LastEventOfType(type)`.
  - Kontext-Auflösung für Subcommands: `GOLEMIC_RUN_ID` + `GOLEMIC_EVENT_LOG`
    aus der Umgebung lesen; fehlt eine → klarer Fehler, Exit ≠ 0 (§2.6).
  - Event-Typen aus §2.6 als Konstanten inkl. Payload-Validierung für
    `review_submitted` (`verdict` ∈ {`approved`,`changes_requested`}).
- **Acceptance:** Round-trip-Test (schreiben → lesen → identisch); malformed
  Zeile → Fehler; paralleles Anhängen zweier Writer verliert keine Zeilen;
  fehlende Env-Vars → benannter Fehler.
- **Out of scope:** Live-Tailing, Recovery-Logik.

### I1.2 — `golemic emit`-Subcommand
- **Goal:** Generisches Event-Tool für die Rollen (§2.6).
- **Scope:** `golemic emit --type <t> --payload '<json>'`: liest Kontext aus
  Env (I1.1), validiert `--payload` als JSON-Objekt, hängt Event an. Unbekannte
  Typen erlaubt (Fortschrittsmarker), aber `type` nicht leer.
- **Acceptance:** Erfolgsfall schreibt exakt eine korrekte JSONL-Zeile;
  kaputtes JSON / fehlender Kontext → Exit ≠ 0 mit klarer Meldung.
- **Out of scope:** `open-pr`, `submit-review`.

### I1.3 — Runner-Gerüst: `run --issue`, Host-Repo, Kollisions-Check
- **Goal:** Deterministischer Rahmen des Laufs (§2.7, §2.11).
- **Scope:** `golemic run --issue N`:
  - Host-Repo ermitteln (git-root; wenn unter `tools/golemic` gestartet, das
    umgebende Repo), `origin`-Remote und Default-Branch `main` auflösen.
  - Config (I0.2) + Credentials (I0.3) laden.
  - `runId` erzeugen (z.B. `issue-<N>-<timestamp>`), Event-Log anlegen,
    `run_started` schreiben.
  - Issue via `gh issue view N --json title,body` laden (Dev-Token).
  - **Kollisions-Check (§2.11):** existiert Worktree unter
    `~/.golemic/<projekt>/worktrees/issue-<N>`, lokaler oder Remote-Branch
    `golemic/issue-<N>`, oder offener PR mit diesem Head-Branch → Abbruch mit
    Meldung inkl. konkreter Aufräum-Kommandos, Outcome `aborted`.
- **Acceptance:** Tests mit gefakten Executors: Happy-Path schreibt
  `run_started` mit korrekter Payload; jede Kollisionsart bricht mit `aborted`
  und der jeweiligen Aufräum-Anleitung ab; fehlende Config/Credentials → klare
  Fehler vor jeglichem GitHub-Zugriff.
- **Out of scope:** Worktree anlegen, Rollen aufrufen.

### I1.4 — Dev-Worktree-Lebenszyklus + git-Identität
- **Goal:** Isolierter, korrekt authentisierter Arbeitsbereich für den Dev
  (§2.4, §2.9).
- **Scope:** Paket `internal/worktree`:
  - `git fetch origin` + `git worktree add <pfad> -b golemic/issue-<N>
    origin/main` unter `~/.golemic/<projekt>/worktrees/issue-<N>`.
  - Lokale git-Config des Worktrees setzen: env-basierter Credential-Helper
    (§2.4, wörtlich), `user.name`/`user.email` der Bot-Identität (via
    `gh api user` mit dem jeweiligen Token ermittelt).
  - `worktree_created`-Event (`path`, `branch`, `baseSha`, `role`).
  - Cleanup-Funktion (`git worktree remove` + Branch-Löschung lokal) — wird
    nur bei `success` gerufen (§2.11).
- **Acceptance:** Tests mit Fakes: korrekte git-Kommandosequenz inkl.
  Config-Aufrufe; `baseSha` stammt aus `origin/main`; Cleanup ruft `remove`;
  bei Fehlern im Lauf wird Cleanup **nicht** gerufen.
- **Out of scope:** Reviewer-Worktree (I1.8).

### I1.5 — Prompt-Rendering: Templates + Guidelines-Injektion
- **Goal:** Vollständige, lauf-spezifische Prompts — kein implizites Wissen
  (§2.10).
- **Scope:** Paket `internal/prompt`:
  - `prompts/dev.md` + `prompts/reviewer.md` (statische Rollen-Regeln,
    System-Prompt) schreiben.
  - Go-Templates für die User-Prompts:
    - Dev: Issue-Nr/Titel/Body inline, Branchname, `verify_command`, Inhalt
      von `.golemic/guidelines/dev.md`, explizite Schrittliste endend mit dem
      wörtlichen `golemic open-pr`-Aufruf (inkl. Regel: erst nach
      `verify_command`-Exit-0).
    - Reviewer: PR-Nr, Issue inline, `verify_command`, Inhalt von
      `guidelines/reviewer.md`, Schrittliste (Diff selbst holen →
      `verify_command` → prüfen → genau ein `golemic submit-review`).
  - Fehlende Guideline-Datei → Fehler (fail-closed), nicht leerer String.
- **Acceptance:** Golden-File-Tests: gerenderte Prompts enthalten alle Fakten
  wörtlich; fehlende Guidelines → benannter Fehler.
- **Out of scope:** der Headless-Aufruf selbst.

### I1.6 — Headless-Rollen-Aufruf (pi-Executor)
- **Goal:** Eine Rolle als Subprozess mit korrektem Kontext ausführen (§2.1,
  §2.6, §2.11).
- **Scope:** Paket `internal/agent`:
  - Aufruf `pi -p --append-system-prompt @<promptfile> --tools <allowlist>
    "<user-prompt>"` im jeweiligen Worktree (cwd).
  - Env des Kindprozesses: `GOLEMIC_RUN_ID`, `GOLEMIC_EVENT_LOG`,
    rollenspezifisches `GH_TOKEN`, `PATH` mit vorangestelltem golemic-Binary
    (§2.6); Modell aus Config.
  - Timeout aus Config: Prozessgruppe killen, Ergebnis `timeout` (§2.11).
  - stdout/stderr ungeparst nach `runs/<runId>/<rolle>.stdout.log` /
    `.stderr.log` (§2.1); Exit-Code nur als Log-Info zurückgeben.
- **Acceptance:** Tests mit Fake-Executor: Env-Set vollständig und
  rollenspezifisch; Timeout killt und meldet `timeout`; Transkripte landen am
  richtigen Ort; kein Parsen von stdout.
- **Out of scope:** Interpretation des Ergebnisses (I1.10).

### I1.7 — `golemic open-pr`-Subcommand
- **Goal:** PR-Erstellung + Event **atomar** koppeln (§2.6).
- **Scope:** `golemic open-pr --title <t> --body <b>`: Kontext aus Env; Branch
  aus dem aktuellen Worktree (`git branch --show-current`); `gh pr create
  --title … --body … --base main --head <branch>` (läuft unter dem `GH_TOKEN`
  der Umgebung = Dev-Bot); PR-Nummer/URL aus der gh-Antwort parsen;
  `pr_opened`-Event schreiben. Schlägt `gh` fehl → kein Event, Exit ≠ 0.
- **Acceptance:** Tests mit Fakes: Erfolg = genau ein Event mit `prNumber`,
  `url`, `branch`; gh-Fehler = kein Event + Exit ≠ 0; fehlender Env-Kontext =
  Exit ≠ 0 vor jedem gh-Aufruf.
- **Out of scope:** Review.

### I1.8 — Reviewer-Worktree vom PR-Branch + Dirty-Check
- **Goal:** Reviewer sieht exakt den gepushten Stand (§2.10).
- **Scope:** Nach `pr_opened`: `git fetch origin` + frischer Worktree unter
  `worktrees/issue-<N>-review` von `origin/golemic/issue-<N>` (detached oder
  Tracking-Branch); gleiche git-Config-Behandlung wie I1.4 (Reviewer-Identität);
  `worktree_created`-Event mit `role: reviewer`. Nach dem Reviewer-Lauf:
  `git status --porcelain` — dirty → Fehlerpfad `review_failed` (§2.10).
- **Acceptance:** Tests: Worktree-Quelle ist der Remote-Branch (nicht der
  Dev-Worktree); Dirty-Check greift nur nach dem Lauf und mappt auf
  `review_failed`; sauberer Lauf passiert den Check.
- **Out of scope:** submit-review-Tool.

### I1.9 — `golemic submit-review`-Subcommand
- **Goal:** Review + Event **atomar** koppeln; einziger Verdikt-Kanal (§2.2,
  §2.6).
- **Scope:** `golemic submit-review --verdict approved|changes_requested
  --body <text> --pr <n>`: Kontext aus Env; Verdikt-Validierung (nur die zwei
  Werte); `gh pr review <n> --approve` bzw. `--request-changes --body …`
  (unter Reviewer-`GH_TOKEN`); `review_submitted`-Event. gh-Fehler → kein
  Event, Exit ≠ 0.
- **Acceptance:** Tests: beide Verdikte mappen auf die richtigen gh-Flags;
  ungültiges Verdikt → Exit ≠ 0 ohne gh-Aufruf; gh-Fehler → kein Event.
- **Out of scope:** Loop-Entscheidung.

### I1.10 — Loop-Orchestrierung: Outcome + `run_finished`
- **Goal:** Den ganzen Lauf zusammenstecken; fail-closed enden (§2.11).
- **Scope:** In `run --issue`: Sequenz I1.3 → I1.4 → I1.5/I1.6 (Dev) →
  Event-Log lesen: `pr_opened` vorhanden? → I1.8 → I1.5/I1.6 (Reviewer) →
  Event-Log lesen: `review_submitted` vorhanden + valide? → Outcome bestimmen:
  - `pr_opened` fehlt/malformed → `dev_failed`
  - `review_submitted` fehlt/malformed oder Dirty-Check → `review_failed`
  - Rollen-Timeout → `timeout`
  - sonst → `success` (Verdikt selbst ist in Iteration 1 nur Log-Inhalt;
    Pingpong kommt in Iteration 2)
  `run_finished` mit Outcome schreiben; bei `success` Worktrees aufräumen,
  sonst stehen lassen; Runner-Exit 0 nur bei `success`, sonst ≠ 0 mit Meldung
  + Pfad zu `runs/<runId>/`.
- **Acceptance:** Tabellengetriebene Tests über alle Outcome-Pfade mit Fakes;
  `run_finished` ist immer das letzte Event, auch in Fehlerpfaden; Cleanup
  exakt nur bei `success`.
- **Out of scope:** Pingpong, Eskalation per PR-Kommentar.

### I1.11 — E2E-Smoke-Dokumentation (`golemic_e2e`)
- **Goal:** Reproduzierbarer manueller Smoke-Test (§2.12).
- **Scope:** `docs/e2e.md`: Vorbereitung des Sandbox-Repos `golemic_e2e`
  (`.golemic/` ausfüllen, Wegwerf-Issue anlegen), Ablauf (`golemic preflight`,
  `golemic run --issue N`), erwartetes Ergebnis (PR + formales Review durch
  die zwei Bot-Identitäten), Aufräum-Kommandos.
- **Acceptance:** Doku vollständig; ein Mensch kann den Smoke-Test ohne
  Zusatzwissen ausführen.
- **Out of scope:** Automatisierung des E2E-Laufs.

---

## Iteration 2 — Pingpong (max. 3 Runden) + Eskalation per PR-Kommentar
## Iteration 3 — Autonomes 60s-Polling (`run.sh`, Label `ready-for-agent`)
## Iteration 4 — Human-in-the-Loop-Pickup (`changes_requested` → Dev)
## Iteration 5 — Installer + Setup (Zielpfad, Bot-Tokens → `~/.golemic/<projekt>/credentials.json`, Verbindungstest)
