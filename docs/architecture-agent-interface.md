# Golemic — Agent-Schnittstelle (Zielarchitektur)

**Status:** Design-Skizze (2026-07-22)
**Bezug:** verfeinert die Runner↔Rollen-Kommunikation aus [architecture.md](architecture.md) §2.1/§2.6.
**Auslöser:** Bug #167 — der Agent umgeht heute fehlende Umgebung, indem er `zsh -c 'source ~/.zshrc; golemic …'` fährt. Das legte einen tieferen Smell offen (siehe unten).

## 1. Kernidee

Der **Runner besitzt** git, GitHub und Credentials. Der **Agent denkt** und ruft nur
getippte `gm_`-Tools auf. Was der Agent nicht zum Weiterdenken braucht (commit, push,
PR öffnen, Review submitten), macht der Runner. Der **letzte Tool-Call ist zugleich das
strukturierte Ergebnis** an den Runner.

## 2. Der Smell heute

Der Runner spawnt den Agenten (`pi -p`), und der Agent shellt für strukturierte
Operationen zurück ins `golemic`-CLI (`golemic slice`, `open-pr`, `submit-review`, …) —
obwohl der Parent-Runner bereits läuft. Folgen:

- **Re-entrantes Bootstrapping:** jeder Leaf-Call lädt Config neu und löst Credentials
  neu aus den Env-Vars auf → deshalb der Token/PATH-Bug (#167).
- **Implizite Kopplung:** Parent und Leaf teilen keinen Zustand; das Ergebnis fließt nur
  indirekt über GitHub + Eventlog. Der Runner kann Agent-Aktionen nicht am Call-Site gaten.
- **Sicherheits-Nebeneffekt:** `source ~/.zshrc` prependet Homebrew → echtes `gh` schlägt
  den gh-Shim.

## 3. Trennkriterium (Runner vs. Tool)

> **Braucht der Agent das *Ergebnis*, um seine nächste Entscheidung zu treffen?**
> Ja → `gm_`-Tool (Agent ruft, golemic führt aus, gibt strukturiert zurück).
> Nein → der Runner macht es (mechanisch, kein Agent-Zutun).

```
Runner (Parent)                        Agent (pi-Subprozess)
─────────────────                      ──────────────────────
• Config + Credentials         denkt • Code lesen/schreiben (read/edit/bash)
• git commit / push                  • gm_project_check  ─┐ braucht Ergebnis
• PR öffnen / Review submitten       • gm_slice_get       │ zum Iterieren
• GitHub-Zugriff (gh)                • gm_pr_view         ┘
• gated: Turn-ID, Schema-Check       • liefert Ergebnis als terminalen Tool-Call
```

`git diff`, `go build`, `make lint` bleiben normale `bash`-Calls bzw. laufen hinter
`gm_project_check` — sie brauchen keinen golemic-Zustand, nur die korrekte Umgebung.

## 4. Toolfläche (`gm_`-Namespace)

Ein Präfix trennt golemic-Tools von pi-Builtins (`bash`/`read`/`edit`) und anderen
Extensions (`subagent`); die Verb-Gruppierung hilft dem Modell bei der Auswahl.

| Tool | Typ | Rolle |
|---|---|---|
| `gm_slice_get` | read | Task-Spec (dev + reviewer) |
| `gm_pr_view` | read | PR-Kontext / Diff (reviewer) |
| `gm_project_check` | read (Wirkung: Verify) | kanonisches Gate (`config.VerifyCommand`), strukturiertes Pass/Fail — beide Rollen |
| `gm_review_submit` | terminal write = Ergebnis | `{verdict, mergeConfidence, body, comments[]}` |
| `gm_dev_done` | terminal signal = Ergebnis | `{summary, commitMsg}` |

Bewusst **nicht** als Agent-Tool: `git commit/push`, `open-pr`, `review-comment`. Die macht
der Runner; Inline-Kommentare reisen als `comments[]` im terminalen `gm_review_submit` mit.

**Offen:** `gm_project_build` (nur schneller Build fürs Inner-Loop) — erst nachrüsten, wenn
der volle `gm_project_check` als Iterations-Schleife nachweislich zu teuer ist. Bis dahin
eine Wahrheit.

## 5. Abläufe

### Dev

```
Runner ──"implementiere Issue N, gib {summary} zurück"──▶ Dev-Agent
                                                             │
                                          gm_slice_get ◀─────┤ (read: Spec)
                                          read/edit/bash ────┤ (implementieren)
                                          gm_project_check ◀─┤ (grün/rot, iteriert)
                                                             │
        {summary, commitMsg} ◀──── gm_dev_done ─────────────┘
          │
          ├─ git commit + push        (Runner, mechanisch)
          └─ open-pr                  (Runner, gh + Credentials)
```

### Reviewer

```
Runner ──"reviewe PR, gib Verdikt zurück"──▶ Reviewer-Agent
                                                 │
                              gm_slice_get ◀─────┤ (read)
                              gm_pr_view   ◀─────┤ (read: Diff/PR)
                              gm_project_check ◀─┤ (verify)
                                                 │
   { verdict, mergeConfidence,                   │
     body, comments[] } ◀── gm_review_submit ────┘  terminal = Ergebnis
          │
          ├─ Schema validieren        (Runner, am Call-Site)
          ├─ Review + Inline-Comments (Runner, gh)
          └─ Outcome / Merge          (Runner, deterministisch)
```

## 6. Technische Andockung (Bausteine existieren schon)

```
gm_-Tool im Agent ──unix socket──▶ golemic-Broker im Runner ──▶ gh / git / config
   (pi.registerTool)                 (Muster: cbmbroker)
```

- **`pi.registerTool`** — golemic registriert das bereits für das `subagent`-Tool
  (`.pi/extensions/subagent/index.ts`).
- **`cbmbroker`** — langlebiger Child + Unix-Socket-JSON-RPC ist die Blaupause fürs Backend
  (`internal/cbmbroker/broker.go`); die Agent-Dir wird ohnehin schon geseedet
  (`preparePiAgentDir`).
- Kein `gh`/kein Token mehr im Agenten → gh-Shim-Bypass entfällt, #167-Root-Cause
  strukturell erledigt.

**Abgrenzung zu [architecture.md](architecture.md) §2.1 ("keine Agent-Extension"):** Dort
wurde verworfen, den *Orchestrator* in eine Extension zu packen (eine LLM-Schicht über den
deterministischen Runner). Hier passiert das Gegenteil: die Extension ersetzt das
CLI-Shelling der *Dev/Reviewer-Rolle* durch getippte, vom Runner backte Tools. Es kommt
**keine** LLM-Schicht hinzu; der Runner bleibt deterministisch.

## 7. Was der Runner heute schon zurückliest (Ist-Zustand)

1. **Exit-Code** des pi-Prozesses.
2. **Activity-Transcript** (`--mode json`) → nur Lauf-Gesundheit (`stopReason`, Fallback).
3. **Eventlog** — der eigentliche Ergebniskanal, aber *indirekt* von den CLI-Subcommands
   geschrieben (`pr_opened`, `review_submitted`) und wieder eingelesen.

Die Zielarchitektur macht (3) explizit: der terminale Tool-Call trägt das typisierte
Ergebnis direkt, am Call-Site validiert, statt aus GitHub/Eventlog rekonstruiert.

## 8. Migrationspfad

1. **#167 (Bugfix, jetzt):** Tokens + Toolchain-PATH ins Subprocess injizieren, `open-pr`
   surfacet Verify-Output. Hält den CLI-Weg am Leben, bis die Tools ihn ablösen — kein
   Wegwerf-Aufwand.
2. **Reviewer-Slice:** `gm_review_submit` statt `golemic submit-review`/`review-comment`;
   der Runner submitted. Kleinster, isolierter erster Schritt.
3. **Dev-Slice:** Runner committet/pusht/öffnet PR; Dev liefert nur `gm_dev_done`.
4. **Reads als Tools:** `gm_slice_get`, `gm_pr_view`.
5. **CLI-Subcommands als Agent-Schnittstelle abschalten** (bleiben ggf. als Operator-CLI).
