# Backlog

Epics und Issues der jeweils aktuellen Bau-Stufe. Format und Arbeitsweise: siehe `docs/architecture/001_way_of_working.md`.

Struktur:

```text
docs/backlog/
  NNN_slug/
    epic.json
    issues/
      NNN_slug.json
```

Regeln:
- Nur die **aktuelle Stufe** wird zerlegt. Spätere Stufen erst, wenn die aktuelle real ist.
- Jedes Issue hat maschinell prüfbare `acceptanceCriteria` (= grüne Tests) und explizite `dependencies`.
- Ein Issue wird gebaut, verifiziert und committet, bevor das nächste startet.
