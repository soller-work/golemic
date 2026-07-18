# Golemic — Architektur

**Status:** Design-Interview abgeschlossen (2026-07-15)
**Prinzip:** So schlank wie möglich. Der Kern ist ein Loop; alles andere ist Harness.

## 1. Zweck

Golemic ist ein **GitHub-getriebener, autonomer Entwicklungs-Loop**. Er nimmt fertig
beschriebene Arbeitseinheiten (GitHub-Issues) entgegen und produziert daraus
begutachtete Pull Requests — ohne dass ein Mensch den Ablauf steuern muss.

Der Ablauf pro Arbeitseinheit:

1. Ein **Issue** ist bereit (Label, keine offenen Abhängigkeiten).
2. Eine **Dev-Rolle** implementiert das Issue, committet, öffnet einen PR.
3. Eine **Reviewer-Rolle** prüft den Branch und gibt ein Review ab (approve / changes_requested).
4. Dev und Reviewer spielen **maximal 3 Runden** Pingpong.
5. Bleibt es unaufgelöst, **eskaliert** der Loop an den Menschen (PR-Kommentar).
6. Ist approved, bleibt der PR für den Menschen offen. Ein menschliches
   `changes_requested`-Review nimmt der Dev automatisch wieder auf.

## 2. Architekturentscheidungen

### 2.1 Standalone-Runner + zwei Rollen-Prompts (keine Agent-Extension)
Golemic ist **kein** Plugin einer LLM-Session. Eine Agent-Extension gibt einer
*laufenden LLM-Session* neue Tools — aber Golemics Orchestrator ist gar kein LLM.
Eine Extension würde also eine überflüssige LLM-Schicht über den deterministischen
Runner stülpen. Deshalb verworfen (ebenso der alte skill-/prompt-getriebene Ansatz).

Golemic besteht aus genau zwei Dingen:
- **Ein Runner-Programm** (deterministisch, tool-gesteuert): pollt GitHub (`gh`),
  wählt Issues, legt Worktrees an, zählt Runden, eskaliert.
- **Zwei Rollen-Prompts** (`dev`, `reviewer`): die einzigen LLM-Anteile.

Der Runner ruft die Rollen **headless** auf, z.B.
`pi -p --append-system-prompt @prompts/dev.md --tools read,bash,write,edit "…"`.
Vom Rollen-Prozess wertet der Runner nur **Exit-Code und Timeout** aus; stdout/stderr
haben **keine Steuerfunktion** und werden lediglich als Transkript archiviert
(§2.9). Die semantische Rückmeldung der Rollen läuft ausschließlich über das
Event-Log (§2.6). Das ist maximal schlank (keine Extension-Infra, kein SDK, keine
Host-Session) und automatisch agent-agnostisch: der Runner könnte statt `pi` auch
einen anderen Agent-CLI aufrufen.

Alles um den Kern herum — Installer, `run.sh`, Setup-Routine — ist bewusst
**dünnes Harness**: austauschbar, minimal, ohne eigene Fachlogik. Der Runner selbst
ist bereits Harness um die zwei LLM-Aufrufe herum.

### 2.2 Loop-Steuerung ist deterministischer Code, nicht LLM-Urteil
Die **Kontrollmechanik** des Loops ist stumpfer, testbarer Code im Runner:
Issue holen, Dev-Rolle starten, PR öffnen, Reviewer-Rolle starten, Verdikt
auswerten, Runden zählen, eskalieren. Für einen autonomen Loop dürfen Runden-Zählung,
Verdikt-Auswertung, Fehlerbehandlung und Eskalation **nicht** von LLM-Laune abhängen.
Nur *Dev* und *Reviewer* sind LLMs; alles andere ist tool-gesteuert.

Die **LLM-Urteilskraft** steckt ausschließlich **innerhalb** der Rollen:
- *Dev*: implementiert, testet, committet.
- *Reviewer*: prüft und fällt ein Verdikt.

Vertrag zwischen Kern und Rollen: der Reviewer beendet seine Arbeit mit genau
**einem `golemic submit-review`-Aufruf** (Verdikt `approved`/`changes_requested`
+ Findings). Das daraus entstehende Event ist der **einzige** autoritative
Verdikt-Kanal; es gibt keine Verdikt-Zeile im Text-Output und kein Output-Parsing.

### 2.3 Agent-Agnostik
Die Architektur beschreibt **Rollen** (Dev, Reviewer) und einen **Loop-Runner**
unabhängig vom konkreten Agent-Runtime. Der Runtime ist ein Implementierungsdetail
des Kerns; die Fach- und Kontrolllogik bleibt davon getrennt.

### 2.4 GitHub-Interaktion über `gh` CLI
Kern und Rollen sprechen GitHub über die **`gh` CLI** (`gh issue view`,
`gh pr create`, `gh pr review`), Auth über `GH_TOKEN`. Grund: maximal schlank —
`gh` deckt Issues, PRs und Reviews out-of-the-box ab, regelt Auth und Pagination,
und die Rollen nutzen dieselben Befehle in ihrer Shell. Direkte REST/GraphQL-Aufrufe
werden nur gezielt nachgerüstet, wenn `gh` etwas nicht kann. Der Verbindungstest im
Setup ist dann simpel (`gh auth status`).

**`git push` und Commit-Identität:** git ignoriert `GH_TOKEN`. Damit der Dev nicht
versehentlich mit den lokalen Credentials des Menschen pusht, konfiguriert der
Runner beim Anlegen jedes Worktrees dessen **lokale git-Config**:
- Credential-Helper, der den Token aus der Umgebung liest (kein Token auf Platte):
  `credential.helper = "!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f"`.
  Funktioniert für beide Rollen, weil `GH_TOKEN` je Lauf rollenspezifisch gesetzt ist.
  Voraussetzung: HTTPS-Remote (Preflight-Check).
- `user.name`/`user.email` auf die jeweilige Bot-Identität.

Identität ist damit deterministisch Runner-Sache, kein Prompt-Thema.

### 2.5 Runner in Go
Der Runner ist ein **einzelnes Go-Binary** — keine Runtime-Abhängigkeit, ideal für
einen Installer, der golemic in ein Unterverzeichnis eines beliebigen
Fremdprojekts dropt (Ort frei wählbar, Konvention/Default: `tools/golemic`;
unabhängig von dessen Sprache). Go ist stark im Shellen von `gh`/`pi`, hat robustes
JSON und ist gut testbar. Bewusst **nicht** Bash: Verdikt-Auswertung und
Eskalationslogik sind zu wichtig für fragiles Shell-Glue.

### 2.6 Ereignisgetriebener Kontrollfluss + Event-Log
Dev/Reviewer reden mit dem Orchestrator nicht über stdout-Prosa, sondern über
**strukturierte Events**. Sie rufen dazu Subcommands desselben Go-Binaries per Shell
auf (`golemic emit …`, `golemic open-pr …`, `golemic submit-review …`) — kein
Socket, kein Server, keine Agent-Extension, damit agent-agnostisch.

Der Orchestrator konsumiert diese Events, schreibt sie **append-only als JSONL**
(pro Lauf, Ablage siehe §2.9) und trifft daraus seine Loop-Entscheidungen. Das
Event-Log ist zugleich **Historie, Audit und Recovery**: der Runner kann den
Zustand daraus rekonstruieren und einen abgebrochenen Lauf fortsetzen (Recovery
ist eine spätere Iteration, siehe §2.11).

**Ein Kanal, keine Divergenz.** Der Reviewer ruft *ein* Tool `submit-review`, das
**beides** tut: Event ins Log schreiben **und** via `gh pr review` nach GitHub
spiegeln. Das Event-Log ist die *interne Wahrheit*, GitHub die *Projektion*.
Analog `open-pr` für den Dev.

**Lauf-Kontext-Vertrag (Env-Vars).** Die Subcommands laufen als eigene Prozesse in
der Shell des LLM und finden ihren Lauf-Kontext über Umgebungsvariablen, die der
Runner beim Headless-Aufruf setzt:
- `GOLEMIC_RUN_ID` — die Lauf-ID.
- `GOLEMIC_EVENT_LOG` — absoluter Pfad zum JSONL-Event-Log.
- `GH_TOKEN` — bereits rollenspezifisch gesetzt (§2.8).
- Das `golemic`-Binary wird dem `PATH` des Kindprozesses vorangestellt.

Die Subcommands sind **fail-closed**: fehlt `GOLEMIC_EVENT_LOG`, brechen sie mit
klarer Fehlermeldung ab.

**Fail-closed:** Der LLM lässt sich nicht zwingen, ein Tool zu rufen. Fehlt ein
erwartetes Event oder ist es malformed, behandelt der Runner das als Fehlerpfad
(§2.11). Determinismus bleibt beim Runner.

**Mensch-Brücke.** Ein menschliches `changes_requested` auf GitHub erzeugt kein
Event. Der Runner pollt den GitHub-Review-State und **synthetisiert** daraus ein
Event ins Log — so bleibt das Log die eine interne Wahrheit (Agent-Events direkt,
Mensch-Events über die GitHub→Event-Brücke).

*MVP-Vereinfachung:* Der Runner **liest** das JSONL nach dem Headless-Lauf (der LLM
hat es währenddessen per `golemic emit` geschrieben). Echtes Live-Tailing/Socket
erst später (Live-Monitoring).

**Tool-Granularität:** zwei spezialisierte Tools für die GitHub-Seiteneffekte
(`open-pr`, `submit-review`) — sie koppeln gh-Aufruf + Logging **atomar**; dazu ein
generisches `emit --type … --payload …` für reine Status-/Fortschrittsmarker.
Lifecycle-Events schreibt der Runner selbst.

**Event-Satz Iteration 1:**

| Emittent | Event | Payload |
|---|---|---|
| Runner | `run_started` | `issue`, `runId` |
| Runner | `worktree_created` | `path`, `branch`, `baseSha`, `role` |
| Dev (`emit`) | `dev_started` | – |
| Dev (`open-pr`) | `pr_opened` | `prNumber`, `url`, `branch` |
| Reviewer (`submit-review`) | `review_submitted` | `verdict` (`approved`/`changes_requested`), `body`, `prNumber` |
| Runner | `run_finished` | `outcome` (§2.11) |

Loop-Entscheidung in Iteration 1 = `verdict` aus `review_submitted`.

### 2.7 Golemic ist ein Werkzeug, das auf das Host-Repo wirkt
Golemic wird per Installer in ein **Unterverzeichnis** eines ausgecheckten
Zielprojekts gedroppt — der Ort ist frei wählbar, Konvention/Default ist
`<ziel>/tools/golemic`. Aus diesem Verzeichnis aufgerufen ermittelt der Runner
das **umgebende Host-Repo**, indem er die Verzeichnisebenen bis zum nächsten
git-root hochwandert, und arbeitet auf **dessen** GitHub-Remote, Issues, `main`
und Worktrees. Golemic baut also das *Zielprojekt*, nicht sich selbst.

> Der Ort ist in beiden Fällen beliebig, ohne hartkodierte Pfadkomponente
> (`ResolveHostRepo`): Liegt das Aufrufverzeichnis innerhalb des von git
> aufgelösten Roots (echtes Unterverzeichnis), gilt dieser Root direkt. Liegt es
> außerhalb (golemic per **Symlink** aus einem separaten Checkout eingebunden),
> läuft der Runner den logischen Pfad aufwärts bis zum umgebenden Host-Repo.

Dieses Repo (`golemic`) ist der reine **Werkzeug-Lieferant**: Go-Sourcen + Binary,
die Rollen-Prompts (`dev`, `reviewer`), der Installer und diese Doku. Der alte
Kotlin-/Formal-Kernel-Ballast (Gradle, `build.gradle.kts`, alte Backlogs) gehört
nicht zu golemic und wird entfernt.

### 2.8 Drei getrennte GitHub-Identitäten
Drei Identitäten arbeiten auf dem Host-Repo:
- **Mensch** (du) — eigener Account, gibt später Human-Reviews.
- **Dev-Bot** — eigener Token; committet, pusht Branch, öffnet PR.
- **Reviewer-Bot** — eigener Token; submittet das Review.

Grund: GitHub verbietet, den **eigenen** PR formal zu approven
(`APPROVE`/`REQUEST_CHANGES` auf eigenem PR → nur `COMMENT` erlaubt). Damit der
Reviewer echte, grün-hakige Reviews geben kann, muss seine Identität von der des
Dev (PR-Autor) verschieden sein.

Der Runner setzt beim Aufruf der jeweiligen Rolle bzw. der gh-Seiteneffekte den
passenden Token als `GH_TOKEN`. Ablage der Tokens siehe §2.9. Das Event-Log
bleibt die interne Wahrheit; die formalen GitHub-Reviews sind die Projektion.

### 2.9 Verzeichnis-Layout: Projekt-Wissen vs. Maschinen-Zustand
Scharfe Trennlinie: **Ins Host-Repo gehört, was zum Projekt gehört und versioniert
werden soll; nach `~/.golemic` gehört, was zur Maschine/zum Betrieb gehört.**

**Im Host-Repo, eingecheckt:**
```
.golemic/
├── config.json          # maschinenlesbar, Vertrag mit dem Runner
└── guidelines/          # Prosa für die Agenten, rollen-spezifisch
    ├── dev.md
    └── reviewer.md
```
- `config.json`: Projekt-Schlüssel (für den Zustands-/Credentials-Lookup unter
  `~/.golemic/<projekt>/`), Modell pro Rolle (mit Default), Label-Name
  (`ready-for-agent`), `verify_command` (Pflicht), Timeout pro Rollen-Lauf.
- `guidelines/*.md`: Stack, Arbeitsweise, Schranken, Konventionen — **pro Rolle
  getrennt**. Der Runner injiziert die zur Rolle passende Datei wörtlich in den
  Rollen-Prompt. Änderungen an Guidelines sind reviewbare Verhaltensänderungen
  der Agenten und gehören deshalb in den Git-Verlauf.
- Das Setup-Script legt `.golemic/` mit Default-Templates an, wenn es fehlt
  (`config.json` mit Defaults, Guideline-Skelette mit Pflicht-Abschnitten:
  Stack, Build/Test, Schranken), die der Mensch einmalig ausfüllt.

**Pro Maschine, niemals im Repo:**
```
~/.golemic/<projekt>/
├── credentials.json     # Dev-Bot- und Reviewer-Bot-Token, 0600
├── runs/<runId>/        # events.jsonl + stdout/stderr-Transkripte der Rollen-Läufe
└── worktrees/           # temporäre Arbeitsverzeichnisse
```
- Tokens strukturell außerhalb des Repos — ein vergessener gitignore-Eintrag im
  Fremdprojekt kann so kein Token leaken. Bereits gesetzte Umgebungsvariablen
  (`GOLEMIC_DEV_TOKEN`, `GOLEMIC_REVIEWER_TOKEN`) haben **Vorrang** vor der Datei
  (CI/Tests). Preflight prüft Existenz (Datei oder Env), Dateirechte (nicht
  group/world-readable), Gültigkeit und **verschiedene** Logins.
- Event-Log und Worktrees außerhalb des Repos: ein Agent kann sie nicht
  versehentlich committen (`git add -A`-Hazard) und sieht sie nicht in seinem
  Arbeitsverzeichnis.

### 2.10 Prompt-Aufbau: Kontext wird injiziert, nicht erraten
Schwächere Agenten scheitern an implizitem Kontext. Deshalb rendert der Runner pro
Rollen-Lauf einen **vollständigen User-Prompt aus einem Template**; die statischen
Rollen-Regeln stehen im System-Prompt (`prompts/dev.md` / `prompts/reviewer.md`),
alle lauf-spezifischen Fakten im gerenderten User-Prompt:

- **Dev:** Issue-Nummer, Titel und **kompletter Issue-Body inline** (kein eigener
  `gh`-Roundtrip nötig), Branchname, `verify_command`, die projekt-eigenen
  Guidelines (`guidelines/dev.md`), und eine **explizite Schrittliste**:
  implementiere → verifiziere (`verify_command` muss Exit 0 liefern, erst dann
  ist `open-pr` erlaubt) → committe → pushe → rufe `golemic open-pr --title …
  --body …`.
- **Reviewer:** PR-Nummer, Issue inline, `verify_command`, Guidelines
  (`guidelines/reviewer.md`), Schrittliste: Diff selbst holen
  (`git diff origin/main...HEAD`, `gh pr view`) → `verify_command` ausführen →
  gegen Issue + Guidelines prüfen → genau ein `golemic submit-review …`.
  Diff wird bewusst **nicht** ins Prompt injiziert (Kontextfenster bei großen PRs).

**Read-only ist Konvention, nicht Sandbox.** Der Reviewer hat bash (er braucht es
für `verify_command` und `submit-review`) und könnte damit technisch schreiben.
Die Schranke steht in `guidelines/reviewer.md`; als Fail-safe prüft der Runner
nach dem Reviewer-Lauf `git status --porcelain` in dessen Worktree — dirty →
Fehlerpfad, kein stilles Weiterlaufen.

**Getrennte Worktrees.** Der Reviewer arbeitet **nicht** im Dev-Worktree, sondern
in einem frischen Worktree vom **gepushten PR-Branch** (`origin/<branch>`). So
reviewt er exakt das, was gemerged würde — uncommittete Artefakte des Dev bleiben
unsichtbar. Nebeneffekt: validiert implizit, dass der Push vollständig ankam.

### 2.11 Fehlerpfade und Outcomes
Geschlossenes Outcome-Enum für `run_finished`:
`success` | `dev_failed` | `review_failed` | `timeout` | `aborted`.

- **Timeout pro Rollen-Lauf** (Config-Feld, Default 30 min): der Runner killt den
  Prozessbaum und beendet mit `timeout`.
- **Fehlendes erwartetes Event** (`pr_opened` nach Dev, `review_submitted` nach
  Reviewer) = `dev_failed` / `review_failed` — **unabhängig vom Exit-Code**. Das
  Event ist die Wahrheit, der Exit-Code nur Log-Information.
- **Eskalation in Iteration 1** = Runner-Exit ≠ 0 mit klarer Meldung; das
  vollständige Event-Log und die Transkripte liegen unter `runs/<runId>/`. Der
  PR-Kommentar-Mechanismus kommt erst in Iteration 2.
- **Worktrees bleiben bei Fehlern stehen** (Debugging); Cleanup nur bei `success`.

**Wiederanlauf & Kollisionen (Iteration 1, bewusst stumpf):** Deterministischer
Branchname `golemic/issue-<N>`. Beim Start prüft der Runner: existiert bereits ein
Worktree, ein lokaler/Remote-Branch oder ein offener PR zu diesem Issue → **Abbruch
mit klarer Meldung** inkl. der konkreten Aufräum-Kommandos. Kein Auto-Cleanup, kein
Resume in Iteration 1 — lieber ehrlich abbrechen als halbschlau Arbeit wegwerfen.
Recovery aus dem Event-Log ist eine spätere Iteration.

### 2.12 CI-Gate und Dev-Retry-Loop

Zwischen `pr_opened` und der Erstellung des Reviewer-Worktrees wartet der Runner, bis alle PR-Checks abgeschlossen sind.

**Ablauf:**
1. **Check-Wait-Phase** (`internal/runner/ciwait.go`): `gh pr checks <prNumber> --json name,state` wird alle 10 Sekunden gepollt. Ergebnis: `green`, `red`, `timeout` (nach `ci_timeout_minutes`), oder `no_checks` (PR ohne CI).
2. **Kein CI konfiguriert:** sofortiger Pass-through mit `ci_wait_finished {result: no_checks}`.
3. **Grün:** Reviewer-Phase startet.
4. **Rot oder Timeout → Dev-Retry:** Der Dev-Agent wird erneut im existierenden Worktree gestartet, mit einem Retry-Prompt der die fehlgeschlagenen Check-Namen und Log-Ausschnitte (`gh run view --log-failed`) enthält. Der Dev fixiert, pushed — der CI-Wait-Zyklus startet neu.
5. **Maximal 2 Fix-Runden** (3 Dev-Runs gesamt). Danach: PR-Kommentar + `dev_failed` + Exit 1; Worktrees bleiben für Debugging.
6. **Fehler bei gh-Abfrage:** fail-closed — kein Reviewer, PR-Kommentar, `dev_failed`.

**Config:** `ci_timeout_minutes` in `.golemic/config.json` (Default 15, muss > 0 sein wenn gesetzt).

**Events:** Jede Wait-Phase schreibt `ci_wait_finished {result, round}` ins Event-Log (BR-006).

**Grenzen:** Loop-Steuerung ist deterministischer Runner-Code; Check-Interpretation findet nie innerhalb einer LLM-Rolle statt (§2.2).

### 2.13 Teststrategie
Zweistufig:
1. **Unit-Ebene (Hauptabsicherung):** `gh`, `git`, `pi` werden im Go-Code hinter
   schmalen Interfaces aufgerufen (injizierbare Command-Executors). Loop-Logik,
   Event-Parsing, Fail-closed-Pfade und Outcome-Bestimmung sind reine Go-Tests
   mit Fakes — keine Netzwerk-Calls.
2. **E2E-Ebene:** Ein dediziertes Sandbox-Repo **`golemic_e2e`** mit
   Wegwerf-Issues. Manuell getriggerter Smoke-Test (`golemic run --issue N`)
   pro Iteration — nicht Teil der normalen Testsuite, sondern Sache des
   Menschen am Iterationsende.

Implementierungs-Issues gelten als erfüllt, wenn die Unit-Tests grün sind.

## 3. Iterativer Aufbau

### Iteration 0 — Preflight-Setup (Startpunkt)
Ein **idempotentes, wiederholt ausführbares** Setup, das alle Voraussetzungen
prüft und mit `SUCCESS` endet, wenn alles steht — sonst klar meldet, was fehlt.
Prüfungen: `gh` installiert; `pi` installiert + lauffähig; `git` inkl.
Worktree-Support; Aufruf aus einem git-Repo mit **HTTPS-Remote**; Dev-Bot- und
Reviewer-Bot-Token vorhanden (Datei oder Env), gültig und **verschiedene** Logins;
`.golemic/config.json` vorhanden + valide. Legt fehlende `.golemic/`-Templates an.
Reine Vorprüfung, keine Loop-Logik.

### Iteration 1 — Loop-Kern (MVP)
Ziel: der Herzmuskel gegen echtes GitHub, minimal.

- **Trigger:** der Runner wird direkt mit einer Issue-Nummer aufgerufen
  (`golemic run --issue 42`) — kein Umweg über eine LLM-Session.
- **In Scope:** Runner legt Dev-Worktree von `origin/main` an → Dev implementiert
  + pusht Branch + öffnet PR → Runner legt Reviewer-Worktree vom PR-Branch an →
  Reviewer submittet ein GitHub-Review. Vorerst **eine** Runde.
- **Out of Scope (spätere Iterationen):** Installer, 60-Sekunden-Polling,
  3-Runden-Pingpong, Eskalation per PR-Kommentar, Human-Review-Pickup,
  Recovery/Resume.

Begründung: Der GitHub-**Schreibpfad** (Branch, PR, Review durch Agents) ist das
größte Risiko und das eigentlich Neue. Polling und Installer sind bekannte,
risikoarme Verpackung und kommen als eigene Iterationen oben drauf.

- **Modell/Config:** Agent-CLI im MVP fix `pi`. Modell pro Rolle in
  `.golemic/config.json` mit starkem Default. Tool-Allowlists fix:
  Dev = `read,bash,write,edit`; Reviewer = `read,bash` (read-only per Konvention,
  §2.10). Die golemic-Subcommands laufen über bash. Dev-Aktionen unter dem
  Dev-Bot-Token, Reviewer-Aktionen unter dem Reviewer-Bot-Token. Token-Setup
  (Erzeugen der Bots) ist Teil des späteren Installers; Iteration 0 prüft nur ihr
  Vorhandensein. Die Agent-CLI-Abstraktion bleibt Prinzip, wird aber erst
  implementiert, wenn ein zweites CLI gebraucht wird (YAGNI).

### Iteration 2 — Pingpong + Eskalation
3-Runden-Pingpong: bei `changes_requested` setzt der Runner die Dev-Session fort
(Findings wörtlich), lässt neu reviewen. Nach 3 Runden ohne `approved`:
**Eskalation** per PR-Kommentar an den Menschen, kein Merge, kein `done`.

### Iteration 3 — Autonomes Polling (`run.sh`)
`run.sh` fragt alle 60s GitHub nach Issues mit Label `ready-for-agent` ohne offene
Abhängigkeiten, wählt eins, ruft den Runner. Dauerbetrieb ohne Menschen.

### Iteration 4 — Human-in-the-Loop-Pickup
Runner erkennt PRs, die ein **Mensch** auf `changes_requested` gesetzt hat
(GitHub→Event-Brücke), und triggert den Dev automatisch erneut.

### Iteration 5 — Installer + Setup
Single-Line-Installer: fragt Zielpfad (default `tools/golemic`), erzeugt/hinterlegt
Bot-Tokens in `~/.golemic/<projekt>/credentials.json`, testet die Verbindung
(`gh auth status`), meldet Login-Fehler.

*(Reihenfolge korrektheitsgetrieben: robuster Loop vor Autonomie. Anpassbar.)*
