# Golemic — Agent-Schnittstelle (Zielarchitektur)

**Status:** Design-Skizze, fortgeschrieben (2026-07-22)
**Bezug:** verfeinert die Runner↔Rollen-Kommunikation aus [architecture.md](architecture.md) §2.1/§2.6.
**Auslöser:** Bug #167 — der Agent umgeht heute fehlende Umgebung, indem er
`zsh -c 'source ~/.zshrc; golemic …'` fährt. Das legte einen tieferen Smell offen
(siehe unten).

## 1. Kernentscheidung

Der **Runner besitzt** git, GitHub, Credentials, Worktrees, Verify-Gates und das
Eventlog. Die Rollen-Agenten **denken** und arbeiten über eine kleine, getippte
Toolfläche.

Die erste Implementierung dieser Toolfläche ist **pi-first**:

```text
pi.registerTool("gm_*") → Unix-Socket → Golemic-Broker im Runner → git/gh/config/CBM
```

Das ist aber nicht der fachliche Kernvertrag. Der stabile Vertrag ist
transport-neutral:

```text
AgentRoleContract
├── Kontext/Discovery
│   ├── SliceGet
│   ├── PRView
│   ├── RepoTree
│   └── Code Intelligence
├── Checks
│   └── ProjectCheck
└── terminale Ergebnisse
    ├── DevDone
    └── ReviewSubmit
```

Damit bleibt Golemic konzeptionell agent-agnostisch: andere Agent-Runtimes könnten
denselben Vertrag später über einen anderen Transport bedienen. Für das MVP ist
`gm_` der pi-Transport.

Wichtig ist die Abgrenzung zu [architecture.md](architecture.md) §2.1: Dort wurde
verworfen, den **Orchestrator** in eine Agent-Extension zu packen. Diese
Entscheidung bleibt. Die Tool-Extension ist keine LLM-Schicht über dem Runner,
sondern nur der typed Transport für die Rollen. Der Runner bleibt deterministisch.

## 2. Der Smell heute

Der Runner spawnt den Agenten (`pi -p`), und der Agent shellt für strukturierte
Operationen zurück ins `golemic`-CLI (`golemic slice`, `open-pr`, `submit-review`,
`review-comment`, `cbm`, …) — obwohl der Parent-Runner bereits läuft. Folgen:

- **Re-entrantes Bootstrapping:** jeder Leaf-Call lädt Config neu und löst
  Credentials neu aus den Env-Vars auf → deshalb der Token/PATH-Bug (#167).
- **Implizite Kopplung:** Parent und Leaf teilen keinen Zustand; das Ergebnis
  fließt nur indirekt über GitHub + Eventlog. Der Runner kann Agent-Aktionen nicht
  am Call-Site gaten.
- **Sicherheits-Nebeneffekt:** `source ~/.zshrc` prependet Homebrew → echtes `gh`
  schlägt den gh-Shim.
- **Zu große Reviewer-Macht:** Der Reviewer kann heute über `bash` nicht nur lesen,
  sondern auch mutieren (`npm run format`, `go generate`, `rm`, Redirects, …).

Die Zielarchitektur entfernt das Shelling als Agent-Vertrag. CLI-Subcommands können
als Operator-CLI bleiben, sind aber nicht mehr die Rollen-Schnittstelle.

## 3. Trennkriterium Runner vs. Tool

> **Braucht der Agent das Ergebnis, um seine nächste Entscheidung zu treffen?**
> Ja → Tool.
> Nein → Runner.

Mechanische Seiteneffekte, deren Ergebnis der Agent nicht zum Weiterdenken braucht,
macht der Runner:

- git commit
- git push
- PR öffnen
- final Review submitten
- Eventlog schreiben
- Loop-Entscheidungen treffen
- Runden zählen / eskalieren / mergen

Tools liefern dagegen Kontext, Check-Ergebnisse oder das terminale Rollen-Ergebnis.

```text
Runner (Parent)                         Agent (pi-Subprozess)
─────────────────                       ──────────────────────
• Config + Credentials          denkt  • Code lesen/schreiben (Dev)
• Worktrees                            • Code nur lesen (Reviewer)
• git commit / push                    • gm_slice_get
• PR öffnen                            • gm_pr_view
• Review submitten                     • gm_repo_tree
• Eventlog schreiben                   • gm_code_*
• Verify-Precheck Reviewer             • gm_project_check (nur Dev)
• gated: Round/Attempt/Schema          • gm_dev_done / gm_review_submit
```

## 4. Begriffe und Identität

Terminalität und Idempotenz gelten nicht global pro Issue, sondern pro einzelner
Agent-Invocation.

```text
runId
  gesamter Golemic-Lauf für ein Issue

round
  Pingpong-Runde, 1..3

role
  dev | reviewer

attempt / invocationId
  einzelner Agent-Prozess für diese Rolle in dieser Runde

callId
  einzelner Tool-Call innerhalb dieser Invocation
```

Beispiel:

```json
{
  "runId": "run-abc",
  "round": 2,
  "role": "reviewer",
  "attempt": 1,
  "invocationId": "run-abc/reviewer/round-2/attempt-1",
  "callId": "uuid-from-tool-transport",
  "tool": "gm_review_submit"
}
```

Idempotenz-Scope:

- Non-terminale Tools dürfen beliebig oft laufen.
- Pro `invocationId` ist genau ein terminales Ergebnis akzeptierbar.
- Identischer Retry desselben terminalen Calls mit gleicher `callId` ist
  idempotent OK.
- Ein anderer zweiter terminaler Call in derselben `invocationId` ist ein
  Protokollfehler.
- Die nächste Runde oder ein Retry bekommt eine neue `invocationId` und darf wieder
  genau ein terminales Ergebnis liefern.

## 5. Tool-Kategorien

Die Toolfläche unterscheidet fünf Kategorien:

| Kategorie | Semantik | Beispiele |
|---|---|---|
| `read` | Kontext lesen, keine Seiteneffekte | `gm_slice_get`, `gm_pr_view`, `gm_repo_tree` |
| `code-intel` | read-only Codebase-Intelligence | `gm_code_search`, `gm_code_trace_call_path` |
| `check` | Verify ausführen, darf bei Dev den Tree ändern | `gm_project_check` |
| `non-terminal write` | Seiteneffekt, aber Invocation läuft weiter | `gm_review_submit_comment` |
| `terminal` | strukturiertes Rollen-Ergebnis, beendet Invocation | `gm_dev_done`, `gm_review_submit` |

Der `gm_`-Namespace trennt Golemic-Tools von pi-Builtins (`read`, `edit`, `bash`,
`write`) und anderen Extensions (`subagent`). `gm_code_*` ist bewusst der Name für
Code-Intelligence-Tools; die interne Implementierung kann weiterhin CBM verwenden.
Der Agent soll nicht an den Produkt-/Backend-Namen `cbm` gekoppelt werden.

## 6. Rollen-Toolflächen

### 6.1 Dev

Dev darf mutieren und hat deshalb klassische Code-Tools plus Golemic-Tools:

```text
pi builtins:
  read
  edit
  write
  bash

Golemic tools:
  gm_slice_get
  gm_project_check
  gm_dev_done

optional read-only code intelligence:
  gm_code_search
  gm_code_search_graph
  gm_code_query_graph
  gm_code_trace_call_path
  gm_code_get_architecture
  gm_code_get_graph_schema
  gm_code_get_snippet
  gm_code_detect_changes
```

Dev macht keine mechanischen Abschluss-Seiteneffekte mehr:

- kein `git commit`
- kein `git push`
- kein `gh pr create`
- kein `golemic open-pr`

Dev liefert nur:

```json
{
  "summary": "Implemented ...",
  "commitMsg": "fix: ..."
}
```

über `gm_dev_done`.

### 6.2 Reviewer

Reviewer ist read-only. Er darf den PR-Branch nicht verändern.

Erlaubt:

```text
pi builtins:
  read

Golemic tools:
  gm_slice_get
  gm_pr_view
  gm_repo_tree
  gm_code_search
  gm_code_search_graph
  gm_code_query_graph
  gm_code_trace_call_path
  gm_code_get_architecture
  gm_code_get_graph_schema
  gm_code_get_snippet
  gm_code_detect_changes
  gm_review_submit_comment
  gm_review_submit
```

Nicht erlaubt:

```text
bash
edit
write
gm_project_check
```

Der Reviewer bekommt also kein generisches Shell-Discovery mehr. Das ist Absicht:
`bash` ist nicht verlässlich read-only. Discovery läuft über `gm_pr_view`,
`gm_repo_tree`, `read` und `gm_code_*`.

## 7. Code Intelligence (`gm_code_*`)

Die bestehenden CBM-Bausteine bleiben nützlich, werden aber nicht als
`golemic cbm …`-CLI über `bash` exponiert. Stattdessen stellt die Agent-Schnittstelle
read-only Tools bereit:

| Tool | Rolle |
|---|---|
| `gm_code_search` | Text-/Symbolsuche im Code |
| `gm_code_search_graph` | semantische Suche im Codegraphen |
| `gm_code_query_graph` | strukturierte Graph-Abfragen |
| `gm_code_trace_call_path` | Call-Path zwischen Funktionen/Modulen nachvollziehen |
| `gm_code_get_architecture` | Architekturzusammenfassung aus CBM |
| `gm_code_get_graph_schema` | Schema/Capabilities des Codegraphen |
| `gm_code_get_snippet` | gezielte Snippets aus indexierten Dateien |
| `gm_code_detect_changes` | Änderungs-/Impact-Kontext |

Intern kann das weiter über `internal/cbmbroker` laufen:

```text
gm_code_* Tool → Golemic-Broker → cbmbroker → codebase-memory MCP
```

Das vorhandene Muster bleibt damit wertvoll:

- langlebiger Child-Prozess
- Unix-Socket-JSON-RPC
- pro Run/Rolle gesetzte Umgebung
- Allowlist und Schema-Validierung

Für Reviewer ist der CBM/Code-Index besonders einfach: Der Runner checkt den
PR-Branch in einem sauberen Worktree aus, indexiert diesen Stand und startet danach
den Reviewer. Weil der Reviewer read-only ist, bleibt der Index während der
Invocation gültig.

Für Dev kann der Index stale werden, weil Dev editiert. Das ist akzeptabel: Dev hat
zusätzlich `read`/`edit`/`bash` und `gm_project_check`. Später kann optional ein
Dev-only Reindex-Tool ergänzt werden; es ist nicht Teil des MVP-Vertrags.

## 8. Working-Tree-Fingerprint

`gm_project_check` und Reviewer-Precheck binden ihr Ergebnis an einen konkreten
Working-Tree-Zustand.

Definition:

```text
workingTreeFingerprint = sha256(
  git status --porcelain=v1 --untracked-files=all
  + git diff --binary HEAD
  + contents of untracked, non-ignored files
)
```

Eigenschaften:

- `.git/` ist ausgeschlossen.
- Ignored Files sind ausgeschlossen.
- Untracked, nicht ignorierte Dateien sind eingeschlossen.
- Tracked Änderungen, Deletions und Staging-State sind sichtbar.
- Dateilisten werden deterministisch sortiert.

Die Gates prüfen später:

```text
currentFingerprint == check.workingTreeFingerprint
```

## 9. `gm_project_check` — Dev-Check

`gm_project_check` führt das kanonische `config.VerifyCommand` aus und gibt ein
strukturiertes Ergebnis zurück:

```json
{
  "ok": true,
  "command": "make test",
  "exitCode": 0,
  "stdout": "...",
  "stderr": "...",
  "summary": "all tests passed",
  "workingTreeFingerprint": "sha256:..."
}
```

Für Dev darf `gm_project_check` den Working Tree verändern. Das ist gewollt:
Formatter, Codegen, Snapshot-Updates oder automatische Fixer sollen ihre Änderungen
erzeugen dürfen. Der Fingerprint wird **nach** dem Verify-Command berechnet.

Beispiel:

```text
gm_project_check
  → formatter ändert Dateien
  → tests grün
  → ok=true, fingerprint=F

gm_dev_done
  → current fingerprint == F
  → akzeptiert
```

Wenn der Check Dateien verändert und danach rot ist, bleibt die Invocation normal
laufend. Dev kann weiterarbeiten und erneut `gm_project_check` ausführen.

Nach akzeptiertem `gm_dev_done` führt der Runner **keinen zusätzlichen Verify** aus.
Der letzte grüne `gm_project_check` ist bereits Runner-backed und durch den
Fingerprint an den aktuellen Tree gebunden. Ein weiterer Verify wäre teuer und
könnte selbst erneut mutieren.

## 10. Dev-Akzeptanzregel

`gm_dev_done` ist nur gültig, wenn in derselben Dev-Invocation gilt:

```text
last(gm_project_check before gm_dev_done).ok == true
&& currentFingerprint == lastCheck.workingTreeFingerprint
```

Ungültig sind insbesondere:

- kein vorheriger `gm_project_check`
- letzter Check rot
- letzter Check grün, danach weiterer roter Check
- letzter Check grün, danach Tree verändert

Wenn Dev ungültig fertigmeldet:

```text
attempt 1:
  gm_dev_done ohne gültigen letzten grünen Check
  → Runner rejected terminal result
  → Runner stoppt Dev
  → Runner startet Dev erneut, gleiche Runde / gleicher Branch / neuer attempt
  → Prompt erklärt konkret, dass gm_project_check grün und aktuell sein muss
```

Beispiel-Hinweis:

```text
Dein vorheriger Versuch wurde nicht akzeptiert:
Der letzte gm_project_check vor gm_dev_done war nicht grün bzw. passte nicht mehr
zum aktuellen Working Tree. Führe gm_project_check aus, behebe Fehler, und rufe
gm_dev_done erst danach auf.
```

## 11. Reviewer-Precheck statt Reviewer-Check-Tool

Reviewer erhält kein `gm_project_check`. Stattdessen führt der Runner vor jedem
Reviewer-Attempt einen Precheck aus.

Ablauf:

```text
Runner:
  checkout PR-Branch in clean/disposable reviewer worktree
  indexiere Code Intelligence für diesen Stand
  beforeFingerprint = current PR branch state
  run config.VerifyCommand
  afterFingerprint = current state after VerifyCommand
  start reviewer with precheck result in prompt
```

Reviewer-Precheck ist strenger als Dev-Check:

- Dev-Check darf mutieren, weil Dev die Änderungen committen kann.
- Reviewer-Precheck darf zwar technisch durch das VerifyCommand verändert werden,
  aber ein veränderter Tree ist kein approvbarer Zustand.

Approval-Gate für Reviewer:

```text
approved zulässig nur wenn:
  precheck.exitCode == 0
  && precheck.beforeFingerprint == precheck.afterFingerprint
  && currentFingerprint == precheck.afterFingerprint
```

Wenn Verify rot ist oder den Tree verändert, darf der Reviewer nicht approven. Er
muss `changes_requested` submitten und die Gründe erklären.

Das adressiert einen wichtigen realen Fall:

```text
Dev:      gm_project_check → grün im Dev-Worktree
Runner:   commit/push/open PR
Reviewer: frischer PR-Branch-Worktree → Verify rot oder Formatter würde ändern
```

Das kann passieren durch nicht-deterministische Checks, fehlende committete Artefakte,
Path-/Cache-/Env-Unterschiede, generierte Dateien oder PR-Branch-Drift. Deshalb zählt
für Approval der Reviewer-Precheck auf dem tatsächlich reviewten PR-Branch.

## 12. Reviewer-Akzeptanzregel

Reviewer beendet seine Invocation mit genau einem `gm_review_submit`.

### `approved`

`gm_review_submit({ verdict: "approved", ... })` ist nur gültig, wenn der
Runner-Precheck für diese Reviewer-Invocation grün und unverändert war:

```text
precheck.ok == true
&& precheck.beforeFingerprint == precheck.afterFingerprint
&& currentFingerprint == precheck.afterFingerprint
```

Wenn Reviewer trotzdem `approved` submitten will, obwohl der Precheck fehlt, rot ist
oder den Tree verändert hat:

```text
Reviewer attempt 1:
  gm_review_submit approved ungültig
  → Runner rejected terminal result
  → Runner stoppt Reviewer
  → Runner startet Reviewer erneut, gleicher PR / gleiche Runde / neuer attempt
  → Prompt weist explizit auf die Approval-Regel hin
```

Hinweistext:

```text
Dein vorheriges Approval wurde nicht akzeptiert.
Ein Approval ist nur zulässig, wenn der Runner-Precheck vor dieser Review-Invocation
grün war und den Working Tree nicht verändert hat.
Wenn der Precheck rot ist oder Änderungen erzeugt, submitte changes_requested und
erkläre die Gründe.
```

### `changes_requested`

`gm_review_submit({ verdict: "changes_requested", ... })` ist auch zulässig, wenn:

- der Precheck rot ist,
- der Precheck Dateien verändert hat,
- oder der Precheck aus technischen Gründen kein approvbares Ergebnis lieferte.

Der Review-Body muss dann die relevanten Gründe erklären.

## 13. Inline-Kommentare: `gm_review_submit_comment`

Inline-Kommentare bleiben ein eigenes, beliebig oft aufrufbares Tool:

```text
gm_review_submit_comment(...)
```

Es ist **non-terminal**, aber write-side-effecting. Der Runner erzeugt bzw. benutzt
dafür eine GitHub Pending Review und legt den Kommentar sofort an.

Beispiel-Payload:

```json
{
  "path": "internal/foo.go",
  "line": 42,
  "body": "This misses the error case.",
  "severity": "blocking"
}
```

Semantik:

```text
gm_review_submit_comment
  → Runner erzeugt/benutzt Pending Review für diese Reviewer-Invocation
  → Runner erstellt Inline-Thread/Kommentar auf GitHub
  → Runner merkt commentId/threadId
  → Tool gibt strukturiert Erfolg/Fehler zurück
```

Terminales Submit:

```text
gm_review_submit
  → Runner submitted die bestehende Pending Review mit verdict + body
  → Runner schreibt review_submitted Event
  → Runner stoppt Reviewer-Agent
```

Wenn ein Kommentar-Anker ungültig ist, gibt das Tool einen Fehler zurück. Der Runner
migriert den Kommentar nicht automatisch in den Body.

```json
{
  "ok": false,
  "code": "ANCHOR_INVALID",
  "message": "Line 42 is not commentable in the current PR diff.",
  "path": "internal/foo.go",
  "line": 42
}
```

Der Reviewer entscheidet dann selbst:

- anderen Anchor wählen,
- Finding im finalen Review-Body beschreiben,
- oder Finding verwerfen.

Approval darf Inline-Kommentare enthalten. Konvention: Bei `approved` sind sie
nicht-blockierend. Blocking-Kommentare müssen zu `changes_requested` führen. Der
Runner kann diese semantische Feinheit nicht zuverlässig erzwingen; sie gehört in den
Reviewer-Prompt.

Wenn ein Reviewer bereits Pending-Kommentare erzeugt und danach ein ungültiges
Approval submitten will, bleiben die Kommentare am Pending Review. Der Runner startet
den Reviewer erneut und weist ihn auf die ungültige Approval-Regel hin; der Reviewer
kann die Pending Review weiter berücksichtigen und final korrekt submitten.

## 14. Terminalität

Für jede Rollen-Invocation gilt:

```text
Agent process starts
  → beliebig viele non-terminale Tools
  → genau ein terminales Tool
  → Runner validiert Ergebnis
  → Runner stoppt Agent-Prozess
  → Runner macht mechanische Seiteneffekte
```

Dev:

```text
gm_dev_done({ summary, commitMsg })
  → Schema + Dev-Gate validieren
  → Agent stoppen
  → git commit
  → git push
  → PR öffnen
  → Events schreiben
```

Reviewer:

```text
gm_review_submit({ verdict, mergeConfidence, body })
  → Schema + Reviewer-Gate validieren
  → Agent stoppen
  → Pending Review submitten
  → review_submitted Event schreiben
```

Nach einem validen terminalen Tool-Call beendet der Runner den Agent-Prozess hart
bzw. stoppt ihn kontrolliert. Weitere LLM-Aktivität ist kein Teil des Protokolls.

Fehlerfälle:

| Fall | Verhalten |
|---|---|
| Kein terminaler Tool-Call | Invocation fehlgeschlagen |
| Terminaler Tool-Call schema-invalid | Invocation fehlgeschlagen bzw. Retry-Pfad |
| Terminaler Tool-Call gültig | Runner stoppt Agent |
| Zweiter anderer terminaler Call | Protokollfehler |
| Transport-Retry mit gleicher `callId` | idempotent OK |

## 15. Abläufe

### 15.1 Dev

```text
Runner ──"implementiere Issue N"──▶ Dev-Agent
                                      │
                 gm_slice_get ◀──────┤ read: Spec
                 read/edit/write/bash ┤ implementieren
                 gm_code_* ◀─────────┤ optional read-only Code Intelligence
                 gm_project_check ◀──┤ Verify, iterierbar, darf formatieren
                                      │
 {summary, commitMsg} ◀ gm_dev_done ─┘ terminal
          │
          ├─ Gate: letzter Check grün + Fingerprint aktuell
          ├─ Agent stoppen
          ├─ git commit + push        (Runner)
          ├─ open PR                  (Runner)
          └─ Eventlog schreiben       (Runner)
```

### 15.2 Reviewer

```text
Runner:
  checkout PR branch clean
  index code intelligence
  run reviewer precheck
  start Reviewer with precheck result

Runner ──"reviewe PR N"────────────▶ Reviewer-Agent
                                      │
                 gm_slice_get ◀──────┤ read: Spec
                 gm_pr_view ◀────────┤ read: PR/Diff/changed files
                 gm_repo_tree ◀──────┤ read-only Navigation
                 gm_code_* ◀─────────┤ read-only Code Intelligence
                 read ───────────────┤ konkrete Dateien lesen
                 gm_review_submit_comment ◀ beliebig oft, schreibt Pending Review
                                      │
 {verdict, body, mergeConfidence} ◀ gm_review_submit ─┘ terminal
          │
          ├─ Gate: approved nur bei grünem unverändertem Precheck
          ├─ Agent stoppen
          ├─ Pending Review submitten (Runner)
          ├─ review_submitted Event   (Runner)
          └─ Outcome / Merge          (Runner)
```

## 16. Eventlog-Verhältnis

Heute schreiben Agent-CLI-Subcommands Events direkt oder gekoppelt an GitHub-Aufrufe.
In der Zielarchitektur schreiben Agenten keine Events mehr direkt.

Neu:

```text
Agent Tool Call
  → Runner/Broker validiert am Call-Site
  → Runner führt mechanische Seiteneffekte aus
  → Runner schreibt Events
```

Beispiele:

```text
gm_dev_done
  → Runner commit/push/open PR
  → Runner schreibt pr_opened / dev_done / lifecycle events

gm_review_submit
  → Runner submitted Pending Review
  → Runner schreibt review_submitted
```

Das Eventlog bleibt Historie, Audit und spätere Recovery-Basis. Es ist weiterhin die
interne Wahrheit, aber es wird nicht mehr von Agent-Subcommands aus Leaf-Prozessen
befüllt, sondern vom Parent-Runner nach validierten Tool-Ergebnissen.

## 17. Technische Andockung

Bausteine existieren bereits:

```text
gm_-Tool im Agent ──unix socket──▶ Golemic-Broker im Runner ──▶ gh / git / config / CBM
   (pi.registerTool)                 (Muster: cbmbroker)
```

- **`pi.registerTool`** — Golemic registriert bereits Tools für pi-Extensions,
  z.B. das vorhandene `subagent`-Muster (`.pi/extensions/subagent/index.ts`).
- **`cbmbroker`** — langlebiger Child + Unix-Socket-JSON-RPC ist die Blaupause für
  das Backend (`internal/cbmbroker/broker.go`).
- **Agent-Dir-Seeding** existiert bereits (`preparePiAgentDir`). Dort können die
  `gm_`-Tools bereitgestellt werden.
- **Kein `gh`/kein Token im Agenten** — GitHub-Credentials bleiben beim Runner.
  Der gh-Shim-Bypass aus #167 entfällt strukturell.

Sicherheit:

- Socket pro Run/Invocation in einem `0700`-Verzeichnis.
- Optional zusätzlich Session-Nonce im Tool-Transport.
- Broker akzeptiert nur Calls für aktuelle `runId`/`invocationId`.
- Tool-Schemas werden am Broker validiert.
- Terminale Calls werden am Call-Site gegatet.

## 18. Toolfläche im Überblick

| Tool | Kategorie | Rolle(n) | Zweck |
|---|---|---|---|
| `gm_slice_get` | read | Dev, Reviewer | Autoritative Task-Spec laden |
| `gm_pr_view` | read | Reviewer | PR-Metadaten, Diff, changed files |
| `gm_repo_tree` | read | Reviewer | read-only Repo-Navigation |
| `gm_code_search` | code-intel | Dev?, Reviewer | Code suchen |
| `gm_code_search_graph` | code-intel | Dev?, Reviewer | Codegraph durchsuchen |
| `gm_code_query_graph` | code-intel | Dev?, Reviewer | strukturierte Graph-Abfrage |
| `gm_code_trace_call_path` | code-intel | Dev?, Reviewer | Call-Pfade verstehen |
| `gm_code_get_architecture` | code-intel | Dev?, Reviewer | Architekturkontext |
| `gm_code_get_graph_schema` | code-intel | Dev?, Reviewer | Graph-Schema/Capabilities |
| `gm_code_get_snippet` | code-intel | Dev?, Reviewer | indexierte Snippets holen |
| `gm_code_detect_changes` | code-intel | Dev?, Reviewer | Impact-/Change-Kontext |
| `gm_project_check` | check | Dev | VerifyCommand ausführen, Fingerprint liefern |
| `gm_review_submit_comment` | non-terminal write | Reviewer | Inline-Kommentar in Pending Review erstellen |
| `gm_review_submit` | terminal write | Reviewer | Review final submitten |
| `gm_dev_done` | terminal signal | Dev | Dev-Ergebnis liefern |

Bewusst nicht als Agent-Tool:

- `git commit`
- `git push`
- `gh pr create`
- `golemic open-pr`
- `golemic submit-review`
- `golemic review-comment`
- generisches `gh`

## 19. Migrationspfad

1. **#167 Bugfix behalten:** Tokens + Toolchain-PATH ins Subprocess injizieren,
   `open-pr` surfacet Verify-Output. Das stabilisiert den CLI-Weg bis zur Ablösung.
2. **Transport-Skeleton:** Runner-Broker + pi-Extension für `gm_` Tools, zunächst mit
   minimalem `gm_slice_get`/`gm_dev_done`/`gm_review_submit` Fake/Schema-Pfad.
3. **Reviewer-Slice zuerst:**
   - Runner-Precheck einführen.
   - Reviewer read-only starten.
   - `gm_pr_view`, `gm_repo_tree`, `gm_code_*` exponieren.
   - `gm_review_submit_comment` schreibt Pending Review.
   - `gm_review_submit` submitted final.
4. **Dev-Slice:**
   - `gm_project_check` für Dev.
   - `gm_dev_done` mit Fingerprint-Gate.
   - Runner committet/pusht/öffnet PR.
5. **Eventlog-Umstellung:** Agenten schreiben keine Events mehr direkt; Runner schreibt
   Events nach validierten Tool-Ergebnissen.
6. **CLI-Subcommands als Agent-Schnittstelle abschalten:** bleiben ggf. als Operator-CLI
   oder Debug-Hilfen, aber nicht mehr in Rollen-Prompts.

## 20. Offene Detailfragen

- Exakte JSON-Schemas für alle `gm_` Tools.
- Retry-Limit pro ungültigem Dev-/Reviewer-Attempt, damit ein Prompt-Fehler nicht
  endlos läuft.
- Verhalten bei Pending Review, wenn der Reviewer-Prozess hart crasht und kein
  `gm_review_submit` kommt.
- Ob `gm_code_*` für Dev im MVP freigeschaltet wird oder zunächst nur für Reviewer.
- Ob ein späteres Dev-only `gm_code_reindex` nötig ist.
- Ob Reviewer-Precheck-Output vollständig oder zusammengefasst in den Prompt kommt
  und wie große stdout/stderr-Ausgaben begrenzt werden.

## 21. Umsetzung — Issue-Checklist

Abgeleitet aus dem Migrationspfad (§19). #167 (Schritt 1, Übergangs-Stabilisierung)
existiert bereits separat.

- [ ] **1. Transport-Skeleton** ([#173](https://github.com/soller-work/golemic/issues/173)) — Golemic-Broker (Unix-Socket, JSON-RPC) + pi-Extension,
  die `gm_`-Tools registriert; erstes echtes Read-Tool `gm_slice_get`; Schema-Validierung;
  Security (Socket `0700`, runId/invocationId-Gating). Terminale Tools zunächst als
  Schema-Stub. (§17, §19.2)
- [ ] **2. Working-Tree-Fingerprint + `gm_project_check`** ([#175](https://github.com/soller-work/golemic/issues/175), blocked-by #173) — Fingerprint-Berechnung (§8)
  und Dev-Check-Tool (§9). Additiv wie #173: Tool registriert + schema-validiert +
  test-bewiesen, Fingerprint nach dem Verify-Lauf berechnet. Der Live-Dev-Loop führt
  Verify weiterhin über `bash` aus; Dev-Konsum + `gm_dev_done`-Gate kommen in Slice 3.
- [ ] **3. Dev-Slice** ([#177](https://github.com/soller-work/golemic/issues/177), blocked-by #173, #175) — `gm_dev_done` terminal + Dev-Akzeptanzregel/Gate (§10) + Runner
  macht commit/push/open-PR. Voller Dev-Cutover (initial + retry): Prompt ohne bash-Verify /
  commit / push / `golemic open-pr`; `gm_dev_done` trägt `{ summary, commitMsg, prTitle, prBody }`;
  ungültige Fertigmeldung → Neustart, gebounded auf 2 Retries; Runner schreibt `pr_opened` +
  bestehende Dev-Lifecycle-Events (breite Eventlog-Migration bleibt Issue 7). (§14, §15.1)
- [ ] **4. Reviewer read-only + Precheck** ([#179](https://github.com/soller-work/golemic/issues/179), blocked-by #173, #175) — Runner-Precheck (§11) vor jedem
  Reviewer-Attempt (before-/afterFingerprint aus #175, `config.VerifyCommand` im Reviewer-Worktree,
  `reviewer_precheck`-Event, gebounded in den Prompt) + Reviewer read-only starten (kein `edit`/`write`;
  `bash` bleibt vorerst nur für den heutigen CLI-Submit) + `gm_pr_view` + `gm_repo_tree` über den #173-Transport.
  Approval-Gate (§12) und `gm_review_submit(_comment)` bleiben Issue 5.
- [ ] **5. Reviewer-Submit** ([#181](https://github.com/soller-work/golemic/issues/181), blocked-by #179) — `gm_review_submit_comment`
  (non-terminal, Pending Review) + `gm_review_submit` terminal + Reviewer-Gate (§12) über den #173-Transport.
  Approval nur gültig bei grünem/unverändertem #179-Precheck; ungültiges Approval → Reviewer-Neustart,
  gebounded auf 2 Retries; `changes_requested` immer zulässig. Reviewer-Allowlist final read-only (kein
  `bash`/`edit`/`write`), Reviewer-Prompt ohne `golemic review-comment`/`submit-review`/`git diff`. Runner
  schreibt `review_submitted` (breite Eventlog-Migration bleibt Issue 7). (§12, §13, §14)
- [ ] **6. Code Intelligence `gm_code_*`** ([#182](https://github.com/soller-work/golemic/issues/182), blocked-by #173) — die acht read-only
  `gm_code_*`-Tools additiv auf dem #173-Transport registriert + schema-validiert (1:1 auf `cbmAllowedSubs`,
  Forwarding über den per-Rolle cbmbroker → codebase-memory MCP), für **Dev und Reviewer** freigeschaltet.
  Voller Prompt+Allowlist-Cutover beider Rollen weg von `golemic cbm`; die `cbm`-CLI selbst bleibt bis Issue 8.
  Read-only, keine Events. (§7, §18)
- [ ] **7. Eventlog-Umstellung** — Runner schreibt Events nach validierten Tool-Ergebnissen. (§16)
- [ ] **8. Legacy-Cleanup** — Agent-Shelling-Pfad, CLI-Event-Schreibpfade und die
  #167-Übergangs-Injektion entfernen; entscheiden, welche Subcommands als Operator-CLI
  überleben. (§19.6)
