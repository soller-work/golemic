# Rückgrat — Technische Grundentscheidungen der Fabrik

**Status:** verbindlich
**Bezug:** `software-fabrik-konzept.md`, insbesondere §31 Stufe 1 (Formalkernel)

Dieses Dokument hält die Entscheidungen fest, die *später teuer zu ändern* sind und alles Weitere tragen. Es beschreibt **nicht** das ganze Produkt — nur das Fundament und die Regeln, nach denen wir arbeiten. Stufe 2–10 werden erst geplant, wenn Stufe 1 real ist.

---

## 1. Was wir zuerst bauen

**Stufe 1: Formalkernel.** Ein deterministisches System, das ein Produktmodell aus versionierten JSON-Dateien einliest, referenzielle Integrität und Typkorrektheit prüft und einen Modellstatus (`valid` / `blocked` / `invalid`) berechnet. Kein LLM, keine UI, kein Codegenerator, keine HTTP-API in Stufe 1.

**Beweis der Tragfähigkeit:** Der erste Referenzablauf (`organization.create → project.create → task.create`, §26) wird von Hand als JSON-Modell geschrieben und muss vom Kernel als `valid` kompiliert werden — inklusive der Fail-closed-Fälle (gebrochene Referenz → `blocked`, falscher Zieltyp → `invalid`, atomare Transaktion → kein kaputter Zwischenzustand).

---

## 2. Technische Entscheidungen

| Thema | Entscheidung | Begründung |
|---|---|---|
| Sprache | **Java 25 (LTS)** | Records, sealed interfaces, Pattern Matching passen exakt zum Modellieren der Modellobjekte/AST. |
| Backend-Framework | **Spring Boot 3.5.x** (Java-25-kompatible Version beim Setup verifizieren) | Vom Nutzer gewählt (versteht den Code). Kernel bleibt Spring-frei; Spring nur als äußerer Ring. |
| Build-Tool | **Gradle** | Vom Nutzer gewählt. |
| Kernel-Reinheit | Kernel (domain/application/adapter) ist **reines Java ohne Spring** | Determinismus + Testbarkeit; Spring nur im äußeren CLI/App-Ring. |
| Architektur | **Clean Architecture**, erzwungen durch **ArchUnit** | Abhängigkeiten zeigen nur nach innen. |
| JSON | **Jackson** | Für kanonisches JSON eigener Kanonisierer (sortierte Keys, kein Whitespace). |
| JSON-Schema | **networknt/json-schema-validator** (Draft 2020-12) | Etablierte Java-Lib. |
| Hashing | **SHA-256 über kanonisches JSON** | Deterministische semantische Hashes (§17.1). |
| IDs | **UUIDv7** | Zeit-sortierbar, stabil; Standard-Format statt ULID. |
| Tests | **JUnit 5 + AssertJ + Mockito** | Akzeptanzkriterien = grüne Tests. |
| Einstieg | **CLI via `CommandLineRunner`**: `compile <model-dir>` | Beweist den Kernel ohne HTTP-Oberfläche. |

---

## 3. Architektur & Repo-Layout (Stufe 1)

Der **Kernel bleibt ein zusammenhängendes Modul** mit Clean Architecture *innen*. Vertical-Slice-Schnitt gilt erst auf Feature-Ebene späterer Stufen, nicht innerhalb der Kernel-Pipeline.

Clean-Architecture-Ringe (Abhängigkeit nur nach innen, per ArchUnit erzwungen):

```text
domain  ←  application  ←  adapter  ←  framework (Spring CLI/App)
```

- **domain** — reine Modellobjekte + Wertobjekte (Id, QualifiedName, Diagnostic). Keine Phasenlogik, keine I/O.
- **application** — die Kompilierungs-Use-Cases: die 8 Phasen, Symboltabelle, Referenzauflösung, Graph, Statusberechnung. Reines Java.
- **adapter** — I/O: JSON-Parsing/Kanonisierung (Jackson), Schema-Prüfung (networknt), Dateisystem.
- **framework** — Spring Boot Entrypoint + `CommandLineRunner`.

```text
/build.gradle(.kts)
/settings.gradle(.kts)
/src/main/java/com/golemic/factory/kernel
  /domain
    /model              Entity, Actor, Behavior, ValueType, Module, Reference
    /value              Id (UUIDv7), QualifiedName, Diagnostic, ModelStatus
  /application
    /parse              JSON laden + kanonisieren (Ports)
    /schema             JSON-Schema-Prüfung (Port)
    /symbols            Symboltabelle, Registrierung
    /resolve            Referenzauflösung, Typprüfung, Modulgrenzen
    /graph              Abhängigkeitsgraph, Reverse-Index
    /hash               semantische Hashes
    /compile            Pipeline (8 Phasen) → Modellstatus
  /adapter
    /json               Jackson-Kanonisierer, Loader (Port-Impl)
    /schema             networknt-Adapter (Port-Impl)
    /fs                 Dateisystem-Zugriff (Port-Impl)
/src/main/java/com/golemic/factory/app
  /cli                  CommandLineRunner: compile <dir>
  Application.java       Spring Boot Entrypoint
/src/test/java/...       Spiegelstruktur; ArchUnit-Regeltests + Akzeptanztests je Slice
/schemas                 JSON-Schemas der Modellobjekttypen (Stufe-1-Teilmenge)
/examples/reference-flow Referenzmodell als JSON (org/project/task)
/docs/architecture       dieses Dokument
/docs/backlog            Epics + Issues
```

---

## 4. Die 8 Kompilierungsphasen (Kernel-Kontrakt, §7.2)

Stabiler Vertrag — spätere Stufen bauen darauf, Reihenfolge ändert sich nicht:

```text
Phase 1  JSON-Dateien syntaktisch laden
Phase 2  JSON-Schemas prüfen
Phase 3  alle Definitionen in Symboltabelle registrieren
Phase 4  alle Referenzen auflösen
Phase 5  Typen und erlaubte Beziehungen prüfen
Phase 6  globale Constraints und Invarianten prüfen
Phase 7  Abhängigkeitsgraph und Reverse-Index erzeugen
Phase 8  Modellstatus berechnen
```

Ein Fehler in einer Phase blockiert die betroffenen Objekte; die Kompilierung sammelt **alle** Diagnosen, statt beim ersten Fehler abzubrechen (Fail-closed, aber diagnosevollständig).

---

## 5. Definition: "Slice"

Ein Slice ist die kleinste Arbeitseinheit mit:
- **einem** klaren Zweck,
- **expliziten Abhängigkeiten** (welche Slices vorher grün sein müssen),
- **mindestens einem maschinell prüfbaren Akzeptanzkriterium** (ein konkreter, ausführbarer Test),
- Umfang für **eine fokussierte Arbeitssession**.

Slices werden im Backlog-Format (`epic.json` + `issues/NNN_*.json`) aus `golemic_bootstrap` geführt.

---

## 6. Definition: "fertig" (Done)

Ein Slice ist fertig, wenn **alle** zutreffen:
1. Alle `acceptanceCriteria` als grüne Tests belegt.
2. Kein bestehender Test bricht (keine Regression).
3. Änderung bleibt im vereinbarten Scope (kein Feature-Zuwachs außerhalb `scope`).
4. In einem eigenen Commit festgehalten, der auf den Slice verweist.

Das Fundament wächst ausschließlich aus Grünem: kein Slice startet, bevor seine Abhängigkeiten fertig sind.

---

## 7. Was Stufe 1 bewusst NICHT enthält

- Kein LLM, keine Gesprächsverarbeitung (Stufe 3).
- Keine Codegenerierung, keine Technologiebindings (Stufe 5).
- Keine HTTP-API, keine UI (später).
- Nicht alle 60+ Modellobjekttypen des Webprofils — nur die Teilmenge für den ersten Referenzablauf: `Entity`, `Actor`, `Behavior`, `ValueType`, `Module`, `Reference` (+ minimal nötige Felder).
- Keine Persistenzdatenbank; die Wahrheit sind JSON-Dateien + git.
```
