---
name: next-slice
description: Baut das nächste offene Backlog-Slice autonom. Schnappt das nächste Issue, delegiert an dev, lässt reviewer prüfen, Pingpong bis approved (max. 3 Runden), dann Commit oder Eskalation an den Menschen. Auslösen mit "next slice", "nächstes slice bauen", "weiterbauen".
---

# next-slice — autonome Slice-Entwicklung

Dieser Skill lässt **dich (den Orchestrator)** ein einzelnes Backlog-Slice bis zur Fertigstellung durchziehen: Slice auswählen → `dev` implementiert → `reviewer` prüft → Pingpong bis `APPROVED` → Commit. Bleibt es nach maximal 3 Runden ungelöst, wird der Mensch um Entscheidung gebeten.

Grundprinzip: **Zuverlässigkeit kommt aus dem Verifikationstor, nicht aus der Menge.** Ein Slice gilt nur als fertig, wenn seine `acceptanceCriteria` als grüne Tests belegt sind (siehe `docs/architecture/001_way_of_working.md`).

## Wichtige Regeln

- **Keine Modelle wählen.** Die Modellzuordnung für `dev` und `reviewer` regelt die Subagent-Extension über `.pi/agent-model-policy.json`. Delegiere nur namentlich (`agent: "dev"` / `agent: "reviewer"`), setze nie ein Modell.
- **Immer `agentScope: "both"`**, damit die projektlokalen Overrides von `dev`/`reviewer` (`.pi/agents/`) Vorrang haben.
- **Codebase-Exploration verbindlich über codebase-memory:** `dev` und `reviewer` erkunden vorhandenen Quellcode ausschließlich über die `codebase_memory_*`-Tools (Graph), nicht über blindes grep/find. Erinnere sie im Auftrag daran und weise an, bei nicht indexiertem Repo zuerst `codebase_memory_index_status`/`codebase_memory_index_repository` zu rufen.
- **Ein Slice pro Lauf**, außer der Mensch bittet ausdrücklich um eine Kette.
- **Fail-closed:** Niemals ein Slice als fertig markieren oder committen, solange Tests rot sind, `acceptanceCriteria` unerfüllt sind oder der ArchUnit-Build bricht.
- **Nicht selbst implementieren.** Deine Rolle ist Auswahl, Delegation, Prüfung der Verdikte und Integration.

## Schritt 1 — Nächstes Slice auswählen

1. Bestimme den aktiven Epic: das niedrigst-nummerierte Verzeichnis unter `docs/backlog/NNN_*` mit noch offenen Issues.
2. Lies dessen `epic.json` und alle referenzierten `issues/*.json`.
3. Wähle das Issue mit dem kleinsten `order`, dessen `status` **nicht** `done` ist und dessen sämtliche `dependencies` bereits `status: done` haben.
4. Gibt es kein solches Issue: melde, dass der Epic fertig ist (oder durch offene Abhängigkeiten blockiert), und stoppe.
5. Nenne dem Menschen kurz, welches Slice du jetzt baust (id + title).

## Schritt 2 — Kontext für dev schnüren

Gib `dev` nur den minimalen Arbeitskontext (Konzept §18.3):

- Das vollständige Issue-JSON (goal, scope, acceptanceCriteria, outOfScope, notes).
- `docs/architecture/000_backbone.md` und `docs/architecture/001_way_of_working.md`.
- Die bereits fertigen Nachbar-Slices, auf denen dieses aufbaut (nur relevante Dateien/Pakete).
- Klarer Auftrag: implementiere **genau** dieses Slice, halte Clean Architecture ein, schreibe die Tests, die die `acceptanceCriteria` maschinell belegen, und lass `./gradlew build` grün laufen. Nichts außerhalb von `scope`.
- Weise `dev` an, den bestehenden Code über die codebase-memory-Tools zu erkunden (siehe Regeln oben), nicht über blindes grep/read.

## Schritt 3 — Runde starten (dev implementiert)

Delegiere an `dev` (Modus single). Verlange am Ende einen strukturierten Bericht:
- welche Dateien geändert/erstellt,
- welche Tests die `acceptanceCriteria` abdecken,
- Ergebnis von `./gradlew build`/Tests (tatsächlich ausgeführt, nicht behauptet),
- verbleibende Risiken.

Merke dir die zurückgegebene `sessionId` — sie wird für die Pingpong-Fortsetzung gebraucht.

## Schritt 4 — Review

Delegiere an `reviewer` (read-only). Übergib:
- das Issue-JSON (besonders `acceptanceCriteria` und `outOfScope`),
- den aktuellen `git diff` (uncommittete Arbeit) bzw. die betroffenen Dateien,
- den dev-Bericht.

Auftrag an `reviewer`: prüfe Korrektheit, Testabdeckung der `acceptanceCriteria`, Einhaltung von Clean Architecture/ArchUnit und Scope-Treue. Read-only Checks (z.B. `./gradlew test`) sind erlaubt. Die Exploration des betroffenen Codes erfolgt über die codebase-memory-Tools (Graph, `trace_path` für Regressions-/Testpfade), nicht über blindes grep/read.

**Verdikt-Kontrakt (verbindlich):** Der Reviewer beendet seine Antwort mit genau einer Zeile:
- `VERDICT: APPROVED` — alle `acceptanceCriteria` belegt, keine blockierenden Findings.
- `VERDICT: CHANGES_REQUESTED` — gefolgt von einer nummerierten Liste **blockierender** Findings mit konkretem Fix.

 Nicht-blockierende Vorschläge dürfen genannt werden, verhindern aber kein `APPROVED`.

## Schritt 5 — Pingpong (max. 3 Runden)

Eine **Runde** = eine dev-Implementierung + ein reviewer-Verdikt.

- `VERDICT: APPROVED` → weiter zu Schritt 6.
- `VERDICT: CHANGES_REQUESTED` **und Runde < 3** → setze die **dev-Session per `sessionId` fort** und übergib die blockierenden Findings wörtlich. Danach erneut Schritt 4 (Review). Zähler +1.
- `VERDICT: CHANGES_REQUESTED` **und Runde = 3** → weiter zu Schritt 7 (Eskalation).

Nutze durchgehend `sessionId`-Fortsetzung für `dev` (und optional für `reviewer`), damit der Kontext zwischen den Runden erhalten bleibt.

## Schritt 6 — Fertigstellen (bei APPROVED)

1. Führe abschließend selbst `./gradlew build` aus und bestätige grün. Bei Rot: zurück zu Schritt 5 (falls Runden übrig) oder Schritt 7.
2. Setze im Issue-JSON `status` von `draft` auf `done`.
3. Committe Code + Statusänderung in **einem** Commit, dessen Message auf das Slice verweist, z.B.:
   `feat(001_formalkernel): <slice-id> — <title>` plus kurze Zusammenfassung und `Reviewed: approved in <n> round(s)`.
4. Melde dem Menschen: Slice fertig, was geändert wurde, wie verifiziert, und welches Slice als Nächstes ansteht.

## Schritt 7 — Eskalation an den Menschen

Nach 3 Runden ohne `APPROVED`:
- **Nicht** committen, **nicht** `done` setzen.
- Fasse zusammen: welches Slice, was `dev` versucht hat, die verbleibenden **blockierenden** Findings des Reviewers, und deine eigene Einschätzung der Ursache (z.B. unklarer Vertrag, fehlende Profilfähigkeit, Toolchain-Grenze — Konzept §29.1).
- Stelle dem Menschen eine konkrete Entscheidungsfrage mit Optionen (z.B. Scope anpassen, Issue splitten, selbst eingreifen, Ansatz ändern).
- Warte auf die Entscheidung. Setze nichts eigenmächtig fort.

## Nach Fertigstellung

Wenn der Mensch "weiter" / "nächstes" signalisiert, beginne erneut bei Schritt 1. Sonst stoppe nach einem Slice.
