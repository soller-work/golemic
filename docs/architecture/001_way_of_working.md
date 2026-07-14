# Arbeitsweise & Projektstrategie

**Status:** verbindlich
**Zweck:** Agent-agnostische Quelle der Wahrheit für *wie* dieses Projekt gebaut wird. Jedes Werkzeug/jeder Agent liest dies, bevor es arbeitet.

---

## 1. Was hier gebaut wird

Die **Software-Fabrik** aus `software-fabrik-konzept.md`: ein Compiler für Produktideen. Natürliche Sprache → versioniertes JSON-Produktmodell → ausführbare Verhaltensverträge → produktionsreife Full-Stack-Webanwendung. Ein deterministischer **Kernel** entscheidet, was gültig, vollständig, verifiziert und veröffentlichbar ist; ein LLM darf nur vorschlagen und innerhalb geschlossener Verträge implementieren (fail-closed).

Backend der Fabrik selbst: **Spring Boot / Java** (Nutzer versteht den Code — Verständlichkeit über technischen "Best Fit").

## 2. Warum diese Strategie

Frühere KI-Projekte scheiterten, weil sie zu groß und unstrukturiert waren. Die vier Fehlermechanismen:
1. **Drift** — spätere Arbeit widerspricht früheren Entscheidungen.
2. **Kein Verifikationstor** — es wird weitergebaut, bevor Vorheriges bewiesen ist.
3. **Akkumulierende Mehrdeutigkeit** — Ungeklärtes wird je Session anders still entschieden.
4. **Kontextverlust** — der Anfang wird vergessen.

Zuverlässigkeit kommt **nicht aus der Größe des Plans**, sondern aus der Verifikationsschleife: jede Einheit ist geschlossen und maschinell geprüft, bevor die nächste beginnt.

## 3. Vorgehen

1. **Rückgrat zuerst** (`000_backbone.md`): tragende technische Entscheidungen, Kernel-Kontrakte, Definitionen von "Slice" und "fertig".
2. **Atomare Zerlegung nur der aktuellen Stufe.** Stufe 1 (Formalkernel) wird in dependency-geordnete, einzeln testbare Slices zerlegt. Jeder Slice hat ein maschinell prüfbares Akzeptanzkriterium.
3. **Einzeln bauen, jeder Slice verifiziert und committet, bevor der nächste startet.** Das Projekt ist nie größer als ein Slice zur Zeit. Das Fundament wächst nur aus Grünem.
4. **Nächste Stufe erst planen, wenn die aktuelle real ist.** Alle 10 Stufen vorab atomar zu zerlegen ist bewusst verworfen — Over-Planning, das veraltet.

## 4. Backlog-Konvention

Epics und Issues liegen unter `docs/backlog/` im Format (übernommen aus `golemic_bootstrap/docs/backlog`):

- Ein Verzeichnis je Epic: `NNN_slug/` mit `epic.json` + `issues/NNN_slug.json`.
- **epic.json:** id, title, targetRepo, status, problem, desiredOutcome, keyDecisions[], nonGoals[], dependenciesAndOrdering[], risks[], humanDecisionsRecorded[], issues[].
- **issue.json:** id, title, type, status, epic, order, targetRepo, goal, visibleOutcome, scope[], acceptanceCriteria[], outOfScope[], dependencies[], notes[].

Tugend des Formats: `acceptanceCriteria` konkret + maschinell prüfbar, `dependencies` explizit, `nonGoals`/`outOfScope` gegen Scope-Creep.

## 5. Definition von "fertig"

Ein Slice ist fertig, wenn: alle `acceptanceCriteria` als grüne Tests belegt sind, kein bestehender Test bricht, die Änderung im `scope` bleibt und in einem eigenen Commit festgehalten ist. Kein Slice startet, bevor seine Abhängigkeiten fertig sind.

## 6. Dokumentenkarte

- `software-fabrik-konzept.md` — vollständiges Zielkonzept (§31 = 10-Stufen-Plan).
- `docs/architecture/000_backbone.md` — technische Grundentscheidungen + Kernel-Kontrakte für Stufe 1.
- `docs/architecture/001_way_of_working.md` — dieses Dokument.
- `docs/backlog/` — Epics + Issues der aktuellen Stufe.
