# Die Software-Fabrik

## Ein formales, nachvollziehbares System zur Übersetzung menschlicher Produktideen in funktionierende Webanwendungen

**Konzeptstand:** 1.0  
**Primärer Anwendungsbereich:** Full-Stack-Webanwendungen mit Frontend, Backend, API, Persistenz und Produktionsbetrieb

---

## Inhaltsverzeichnis

1. [Ausgangsproblem](#1-ausgangsproblem)
2. [Produktziel](#2-produktziel)
3. [Garantien und Nicht-Garantien](#3-garantien-und-nicht-garantien)
4. [Das Grundmodell: Ein Compiler für Produktideen](#4-das-grundmodell-ein-compiler-für-produktideen)
5. [Die Schichten der Software-Fabrik](#5-die-schichten-der-software-fabrik)
6. [Die kanonische Produktdefinition](#6-die-kanonische-produktdefinition)
7. [Referenzielle Integrität](#7-referenzielle-integrität)
8. [Das Web-Application-Profil](#8-das-web-application-profil)
9. [Projektprofile](#9-projektprofile)
10. [Die Rolle des LLM](#10-die-rolle-des-llm)
11. [Fachliche Werkzeuge für das LLM](#11-fachliche-werkzeuge-für-das-llm)
12. [Der Stakeholder-Workflow](#12-der-stakeholder-workflow)
13. [Die visuelle Oberfläche](#13-die-visuelle-oberfläche)
14. [Verhaltensverträge](#14-verhaltensverträge)
15. [End-to-End-Abläufe als sichtbare Arbeitseinheit](#15-end-to-end-abläufe-als-sichtbare-arbeitseinheit)
16. [Automatische Modellprüfung](#16-automatische-modellprüfung)
17. [Änderungen und Auswirkungsanalyse](#17-änderungen-und-auswirkungsanalyse)
18. [Codegenerierung und Implementierung](#18-codegenerierung-und-implementierung)
19. [Manueller Code und Erweiterungspunkte](#19-manueller-code-und-erweiterungspunkte)
20. [Projektlokale Spracherweiterungen](#20-projektlokale-spracherweiterungen)
21. [Die automatische Build- und Release-Pipeline](#21-die-automatische-build--und-release-pipeline)
22. [Preview, Produktabnahme und Release](#22-preview-produktabnahme-und-release)
23. [Produktionsfähigkeit](#23-produktionsfähigkeit)
24. [Skalierung](#24-skalierung)
25. [Versionierung von Profilen, Compiler und Toolchain](#25-versionierung-von-profilen-compiler-und-toolchain)
26. [Referenzprodukt: Projekt- und Aufgabenverwaltung](#26-referenzprodukt-projekt--und-aufgabenverwaltung)
27. [Technische Zielarchitektur der Fabrik](#27-technische-zielarchitektur-der-fabrik)
28. [Teststrategie](#28-teststrategie)
29. [Fail-closed-Verhalten](#29-fail-closed-verhalten)
30. [Mandanten- und Produktgrenzen](#30-mandanten--und-produktgrenzen)
31. [Vollständiger Konstruktionsplan](#31-vollständiger-konstruktionsplan)
32. [Offene Forschungsprobleme](#32-offene-forschungsprobleme)
33. [Schlussfolgerung](#33-schlussfolgerung)
34. [Anhang A: Beispielstruktur eines Produkt-Repositories](#anhang-a-beispielstruktur-eines-produkt-repositories)
35. [Anhang B: Beispiel eines kanonischen Verhaltensvertrags](#anhang-b-beispiel-eines-kanonischen-verhaltensvertrags)
36. [Anhang C: Zustände und Statusbegriffe](#anhang-c-zustände-und-statusbegriffe)
37. [Anhang D: Release-Manifest](#anhang-d-release-manifest)

---

# 1. Ausgangsproblem

Eine menschliche Produktidee ist kein ausführbares Artefakt. Sie besteht typischerweise aus unvollständigen Aussagen, impliziten Annahmen, wechselnden Begriffen, widersprüchlichen Erwartungen und noch nicht erkannten Entscheidungen.

Ein Mensch kann beispielsweise sagen:

> Teams sollen Projekte anlegen, Mitglieder einladen und Aufgaben durch einen Workflow bewegen können.

Diese Aussage enthält bereits mehrere ungeklärte Punkte:

- Was ist ein Team?
- Ist ein Team die Mandantengrenze oder nur eine Benutzergruppe?
- Wem gehören Projekte?
- Wer darf Mitglieder einladen?
- Welche Aufgabenstatus existieren?
- Welche Übergänge sind erlaubt?
- Was sehen Benutzer ohne Projektzugriff?
- Was bedeutet Löschen?
- Welche Anforderungen gelten für Betrieb, Sicherheit und Wiederherstellung?

Ein Coding-Agent kann aus solchen Aussagen Code erzeugen. Er muss dabei jedoch fehlende Entscheidungen ergänzen. Diese Ergänzungen können plausibel wirken, sind aber nicht autorisiert. Das Problem ist deshalb nicht nur, dass ein LLM halluzinieren kann. Das tiefere Problem ist, dass ein unvollständiger Produktwunsch ohne kontrollierten Übersetzungsprozess zwangsläufig Interpretationsarbeit erzeugt.

Die Software-Fabrik muss daher verhindern, dass unbelegte Annahmen unbemerkt zu verbindlichem Produktverhalten werden.

---

# 2. Produktziel

Die Software-Fabrik ist ein dauerhaftes Entwicklungssystem, das eine menschliche Produktidee schrittweise in ein formales, ausführbares und überprüfbares Produktmodell überführt und daraus eine produktionsfähige Webanwendung erzeugt.

Die zentrale Produktgarantie lautet:

> Die Software-Fabrik erzeugt ausschließlich Verhalten, das durch ein akzeptiertes Modell autorisiert ist. Jede relevante Eigenschaft ist bis zu ihrer menschlichen oder formal abgeleiteten Herkunft nachvollziehbar. Ungeklärte Entscheidungen blockieren nur die betroffenen Teile. Änderungen werden deterministisch verfolgt, und die Software konvergiert kontrolliert auf den jeweils akzeptierten Produktzustand.

Die Fabrik ist kein einmaliger Prompt-zu-Code-Generator. Sie bleibt während des gesamten Lebenszyklus des Produkts aktiv:

```text
Idee
→ Klärung
→ formales Produktmodell
→ ausführbare Verträge
→ Implementierung
→ Verifikation
→ Preview
→ Produktabnahme
→ Release
→ Änderung
→ erneute Auswirkungsanalyse und Verifikation
```

Der primäre Fokus liegt auf Full-Stack-Webanwendungen:

- Frontend
- Backend
- explizite API
- persistente Datenhaltung
- Authentifizierung
- Autorisierung
- Hintergrundjobs
- externe HTTP-Integrationen
- Deployment
- Beobachtbarkeit
- Backup und Wiederherstellung

Die Architektur bleibt durch Profile erweiterbar, aber das erste vollständige Domänenprofil beschreibt Webanwendungen.

---

# 3. Garantien und Nicht-Garantien

## 3.1 Garantierbare Eigenschaften

Die Fabrik kann folgende Eigenschaften technisch erzwingen:

1. Kein LLM-Vorschlag wird ohne formale Prüfung Teil des kanonischen Produktmodells.
2. Keine neue beobachtbare Produktsemantik wird ohne die erforderliche menschliche Freigabe veröffentlicht.
3. Jede Referenz im gültigen Modell zeigt auf genau ein existierendes Objekt des erwarteten Typs.
4. Jede semantisch relevante Änderung erzeugt eine maschinell nachvollziehbare Auswirkungsanalyse.
5. Veraltete Evidenz kann keinen Release freigeben.
6. Jede veröffentlichte Version ist auf Modell, Code, Toolchain, Entscheidungen und Prüfergebnisse zurückführbar.
7. Nicht ausdrückbare oder nicht implementierbare Anforderungen blockieren den betroffenen Ablauf, statt still interpretiert zu werden.
8. Gleicher Modellstand, gleiche Profilversion und gleiche Toolchain erzeugen denselben formalen Modellzustand und dieselben deterministisch generierten Artefakte.
9. Direkte Änderungen am Modell durch Menschen oder Werkzeuge durchlaufen dieselbe unvermeidbare Prüf-Pipeline.
10. Ein Release kann nur aus einer vollständig verifizierten Kombination aus Modellversion, Codeversion, Toolchain und Evidenz entstehen.

## 3.2 Nicht garantierbare Eigenschaften

Die Fabrik darf nicht behaupten:

- die ursprüngliche menschliche Idee vollständig verstanden zu haben,
- wirtschaftlichen Produkterfolg zu garantieren,
- objektiv optimale UX zu erzeugen,
- die Abwesenheit aller Softwarefehler zu beweisen,
- dass ein LLM niemals falsche Vorschläge erzeugt,
- dass jede denkbare Webanwendung bereits mit dem aktuellen Profil ausdrückbar ist,
- dass Tests alle möglichen Laufzeitzustände erfassen,
- dass frei geschriebener Code automatisch vollständig semantisch analysierbar ist.

Die präzise Aussage ist:

> Die Fabrik verhindert nicht, dass Menschen oder LLMs falsche Annahmen formulieren. Sie verhindert, dass unbelegte Annahmen unbemerkt zu autorisiertem Produktverhalten werden.

---

# 4. Das Grundmodell: Ein Compiler für Produktideen

Die passende Analogie ist kein Chatbot, sondern ein Compiler.

```text
Natürliche Sprache         = Quellsprache mit Mehrdeutigkeiten
Formales Produktmodell     = Intermediate Representation
Verhaltensverträge         = ausführbare Semantik
Profile                    = Sprachdefinition und Standardbibliothek
Generatoren                = Codegeneratoren und Bindings
Build-Pipeline             = Compiler-, Prüf- und Release-Pipeline
Produktionscode            = kompiliertes Ergebnis
```

Der Prozess besteht aus vier unterschiedlichen Operationen:

1. **Verstehen:** Aussagen, Begriffe, Ziele und offene Punkte aus Sprache extrahieren.
2. **Autorisieren:** Produktentscheidungen mit dem Stakeholder klären und verbindlich machen.
3. **Kompilieren:** Aus dem akzeptierten Modell Verträge, Tests, Implementierungsaufgaben und technische Artefakte erzeugen.
4. **Verifizieren:** Nachweisen, dass die Implementierung den akzeptierten Verträgen entspricht.

Diese Operationen dürfen nicht vermischt werden. Insbesondere darf ein LLM keine Interpretation automatisch in eine autorisierte Produktentscheidung umwandeln.

---

# 5. Die Schichten der Software-Fabrik

Die Fabrik besteht aus fünf klar getrennten Schichten.

## 5.1 Fabrik-Kernel

Der Kernel enthält profilunabhängige Mechanismen:

- Parser für kanonisches JSON
- JSON-Schema-Prüfung
- Symboltabelle
- Referenzauflösung
- Typensystem
- Namespaces und Module
- Versionsverwaltung
- Abhängigkeitsgraph
- Reverse-Dependency-Index
- Constraint-Auswertung
- atomare Modelltransaktionen
- semantische Hashes
- inkrementelle Kompilierung
- Provenienz
- Evidenzverwaltung
- Build- und Release-Sperren
- Plugin- und Profil-Schnittstellen

Der Kernel kennt noch keine konkreten Webbegriffe wie Seite, Formular oder REST-Endpunkt.

## 5.2 Domänenprofil

Das Domänenprofil definiert eine Softwareklasse. Das erste Profil ist das **Web-Application-Profil**.

Es definiert:

- erlaubte Modellobjekte,
- erlaubte Beziehungen,
- Vollständigkeitsregeln,
- formale Ableitungen,
- Interviewregeln,
- Generatoren,
- Verifikatoren,
- Qualitätsanforderungen.

## 5.3 Projektprofil

Ein Projektprofil legt wiederverwendbare technische und organisatorische Policies fest.

Beispiele:

- Standard SaaS Web App
- Internal Business Application
- Public Consumer Web App
- Regulated Web Application

Projektprofile dürfen Defaults und technische Regeln liefern, aber keine verdeckten Geschäftsanforderungen.

## 5.4 Produktmodell

Das Produktmodell beschreibt das konkrete Produkt:

- Akteure
- Organisationen
- Projekte
- Aufgaben
- Rollen
- Sichtbarkeitsregeln
- Benutzerabläufe
- Geschäftsregeln
- UI-Verhalten
- Betriebsanforderungen

Es ist die Instanz des Webprofils unter einem gewählten Projektprofil.

## 5.5 Artefakt- und Evidenzebene

Aus dem Produktmodell entstehen:

- API-Verträge
- Datenbankschemas
- Frontend-Modelle
- Tests
- Code
- Build-Artefakte
- Deployments
- Testresultate
- Sicherheitsberichte
- Release-Manifeste

Diese Ebene liefert den Nachweis, dass eine konkrete Implementierung zu einer konkreten Modellversion gehört.

---

# 6. Die kanonische Produktdefinition

Die verbindliche Produktdefinition besteht aus versionierten JSON-Dateien.

## 6.1 Eine Datei pro Modellobjekt

Nicht das gesamte Produkt liegt in einer Datei. Jedes Modellobjekt liegt in einer eigenen Datei:

```text
/product
  /actors
    organization-member.json
    organization-admin.json
  /domain
    organization.json
    project.json
    task.json
  /behaviors
    create-organization.json
    create-project.json
    create-task.json
    move-task.json
  /policies
    project-visibility.json
    authorization.json
  /ui
    project-board.json
  /operations
    deployment.json
    backup.json
```

## 6.2 Eigenschaften jedes Modellobjekts

Jedes Objekt besitzt mindestens:

```json
{
  "$schema": "https://factory.example/schemas/entity/v1.json",
  "kind": "entity",
  "id": "01JPROJECT00000000000000001",
  "qualifiedName": "project.Project",
  "displayName": "Projekt",
  "modelVersion": 4,
  "profile": "web-application@1.0"
}
```

Die drei Identitätsbegriffe sind getrennt:

- `id`: unveränderliche interne Identität,
- `qualifiedName`: menschenlesbarer, eindeutiger technischer Name,
- `displayName`: frei änderbare und lokalisierbare Anzeige.

Referenzen verwenden die stabile ID. Umbenennungen verändern dadurch nicht die Identität.

## 6.3 Warum JSON

JSON ist geeignet, weil es:

- eindeutig parsebar ist,
- keine impliziten Typkonvertierungen wie viele YAML-Parser besitzt,
- direkt durch JSON Schema prüfbar ist,
- als Tool-Eingabe und Tool-Ausgabe verwendet werden kann,
- deterministisch kanonisiert werden kann,
- gut gehasht und versioniert werden kann,
- in praktisch allen Programmiersprachen unterstützt wird.

Der Stakeholder arbeitet primär mit verständlichen UI-Projektionen. Das Roh-JSON bleibt für Entwickler, Reviews, Git und Automatisierung zugänglich.

---

# 7. Referenzielle Integrität

JSON selbst gewährleistet keine referenzielle Integrität. Diese wird vom Repository und dem semantischen Compiler erzwungen.

## 7.1 Typisierte Referenzen

Eine Referenz ist kein unstrukturierter String:

```json
{
  "entity": {
    "$ref": "01JPROJECT00000000000000001",
    "expectedKind": "entity"
  }
}
```

Der Compiler prüft:

- Existiert das Ziel?
- Ist das Ziel eindeutig?
- Besitzt es den erwarteten Typ?
- Ist es im Modul sichtbar?
- Ist die referenzierte Version kompatibel?
- Ist die Beziehung nach dem Profil erlaubt?
- Entsteht ein unzulässiger Zyklus?

## 7.2 Mehrphasige Modellkompilierung

```text
Phase 1: JSON-Dateien syntaktisch laden
Phase 2: JSON Schemas prüfen
Phase 3: alle Definitionen in der Symboltabelle registrieren
Phase 4: alle Referenzen auflösen
Phase 5: Typen und erlaubte Beziehungen prüfen
Phase 6: globale Constraints und Invarianten prüfen
Phase 7: Abhängigkeitsgraph und Reverse-Index erzeugen
Phase 8: Modellstatus berechnen
```

Ein ungelöster Verweis verhindert einen gültigen Modellstand.

## 7.3 Modulgrenzen

Module exportieren definierte Symbole:

```json
{
  "kind": "module",
  "id": "01JMODULETASK000000000000001",
  "qualifiedName": "task",
  "exports": [
    "01JTASKID000000000000000001",
    "01JTASKSTATUS00000000000001",
    "01JTASKCREATE00000000000001"
  ],
  "imports": [
    {
      "module": "project",
      "symbols": ["01JPROJECTID000000000000001"]
    }
  ]
}
```

Interne Objekte anderer Module dürfen nicht direkt referenziert werden.

## 7.4 Atomare Modelltransaktionen

Mehrere JSON-Dateien können gemeinsam geändert werden. Die Änderung wird als Transaktion behandelt:

```text
alter Modellstand
+ vollständiger Änderungssatz
→ temporärer neuer Modellstand
→ vollständige automatische Prüfung
→ atomarer Commit oder vollständiger Abbruch
```

Es gibt keinen kanonischen Zwischenzustand mit gebrochenen Referenzen.

## 7.5 Löschen und Ersetzen

Ein referenziertes Objekt kann nicht einfach gelöscht werden. Das System zeigt alle eingehenden Referenzen und erlaubt nur:

1. Löschen blockieren,
2. alle Referenzen im selben Commit ersetzen,
3. das Objekt als veraltet markieren,
4. eine Migration zu einem Ersatzobjekt definieren.

---

# 8. Das Web-Application-Profil

Das Webprofil ist eine formale Sprache für Full-Stack-Webanwendungen. Es besteht nicht nur aus Templates, sondern aus Metamodell, Regeln, Generatoren und Verifikatoren.

## 8.1 Systemstruktur

Modellobjekte:

- Application
- Module
- Frontend
- Backend
- API
- Database
- Worker
- ExternalSystem
- DeploymentUnit

Beziehungen:

```text
Frontend ruft API auf
Backend besitzt Persistenz
Worker verarbeitet Job
Modul importiert exportierten Vertrag eines anderen Moduls
Anwendung integriert externes System
```

## 8.2 Domänenmodell

Modellobjekte:

- Entity
- ValueObject
- Aggregate
- Relation
- Invariant
- Lifecycle
- DomainService
- Policy

Unterstützte semantische Eigenschaften:

- Nullability
- Eindeutigkeit
- Kardinalität
- Ownership
- Löschverhalten
- Validierung
- Status und Lebenszyklus
- Transaktionsgrenzen

## 8.3 Backend-Verhalten

Modellobjekte:

- Behavior
- Command
- Query
- Workflow
- State
- Transition
- Policy
- Invariant
- SideEffect
- ScheduledAction

Grundregeln:

```text
Command darf Zustand verändern.
Query darf keinen fachlichen Zustand verändern.
Policy begrenzt oder entscheidet Verhalten.
Workflow koordiniert mehrere Schritte.
Invariant muss an definierten Zustandsgrenzen gelten.
```

## 8.4 API-Modell

Modellobjekte:

- ApiOperation
- Request
- Response
- Error
- AuthenticationRequirement
- AuthorizationRequirement
- Pagination
- Filter
- Sort
- RateLimit
- Idempotency

Das Profil definiert vollständige Fehlersemantik. Ein API-Fehler darf nicht existieren, ohne dass aufrufende Benutzeroberflächen oder Integrationen ein Verhalten dafür besitzen.

## 8.5 Frontend-Modell

Modellobjekte:

- Page
- Route
- Component
- Form
- ViewState
- UserAction
- Navigation
- LoadingState
- EmptyState
- ErrorState
- PermissionState
- ResponsiveRule

Für datenabhängige Ansichten sind mindestens zu behandeln:

- Laden
- leerer Zustand
- erfolgreicher Zustand
- Teilfehler
- vollständiger Fehler
- nicht authentifiziert
- nicht autorisiert
- veraltete Daten

## 8.6 Frontend-Backend-Verknüpfung

Das Profil kennt explizite Beziehungen:

```text
Form sendet Command
Page lädt Query
API-Fehler führt zu UI-Zustand
Berechtigung beeinflusst sichtbare und ausführbare Aktion
Erfolg führt zu Navigation oder sichtbarer Zustandsänderung
```

Dadurch kann der Compiler prüfen, ob jeder relevante Backend-Ausgang ein definiertes Frontend-Verhalten besitzt.

## 8.7 Persistenz

Modellobjekte:

- PersistenceModel
- TableBinding
- ColumnBinding
- IndexRequirement
- ConstraintRequirement
- RelationBinding
- Migration
- TransactionBoundary
- RetentionRule
- DeletionRule

Das Webprofil verlangt Fähigkeiten, keine konkrete Datenbank. PostgreSQL ist eine mögliche Technologiebindung, aber nicht Teil der fachlichen Semantik.

## 8.8 Sicherheit

Modellobjekte:

- Identity
- Role
- Permission
- Resource
- AuthenticationFlow
- AuthorizationPolicy
- DataScope
- Secret
- Threat
- SecurityControl

Pflichtprüfungen umfassen:

- serverseitige Autorisierung,
- Mandantentrennung,
- Eingabevalidierung,
- Ausgabecodierung,
- Session-Verhalten,
- CSRF und CORS,
- Secret-Verwaltung,
- Auditierung.

## 8.9 Externe Integrationen

Modellobjekte:

- ExternalApi
- Webhook
- Message
- Job
- RetryPolicy
- TimeoutPolicy
- CircuitBreaker
- IdempotencyRule
- Fallback

Jede Integration muss Ausfall-, Wiederholungs- und Idempotenzverhalten definieren.

## 8.10 Qualitätsanforderungen

Modellobjekte:

- PerformanceRequirement
- AvailabilityRequirement
- AccessibilityRequirement
- BrowserSupport
- ScalabilityRequirement
- ObservabilityRequirement
- RecoveryRequirement

Qualitätsanforderungen müssen messbar formuliert sein.

## 8.11 Betrieb

Modellobjekte:

- Environment
- DeploymentUnit
- Configuration
- Secret
- DatabaseMigration
- HealthCheck
- Log
- Metric
- Trace
- Alert
- Backup
- Restore
- Rollback

Eine Anwendung gilt nicht als vollständig, wenn nur ihr Quellcode erzeugt wurde.

---

# 9. Projektprofile

Projektprofile kombinieren Architektur- und Technologie-Policies.

Jede Regel besitzt eine Bindungsstärke:

- `required`: muss erfüllt werden,
- `preferred`: soll verwendet werden, sofern keine Anforderung dagegen spricht,
- `default`: wird gewählt, wenn keine Entscheidung existiert,
- `forbidden`: darf nicht verwendet werden.

Beispiel:

```json
{
  "kind": "projectProfile",
  "id": "01JPROFILESAAS0000000000001",
  "qualifiedName": "profiles.StandardSaaSWebApp",
  "required": {
    "controls": [
      "server_side_authorization",
      "tenant_isolation",
      "versioned_migrations",
      "automated_backups",
      "structured_logging"
    ]
  },
  "preferred": {
    "architecture": "modular_monolith",
    "persistenceModel": "relational",
    "frontendBackendBoundary": "explicit_api"
  },
  "default": {
    "apiStyle": "rest",
    "databaseBinding": "postgresql",
    "deployment": "containerized"
  },
  "forbidden": {
    "patterns": [
      "business_rules_only_in_frontend",
      "secrets_in_repository",
      "unversioned_database_changes"
    ]
  }
}
```

Eine Abweichung von `preferred` oder `default` ist möglich, wenn die Alternative alle benötigten Fähigkeiten erfüllt. Eine Abweichung von `required` benötigt einen bewussten Profilwechsel oder eine formale Ausnahme. `forbidden` blockiert Build oder Release.

---

# 10. Die Rolle des LLM

Das LLM ist nicht die formale Instanz. Es übernimmt nur Arbeiten, für die natürliche Sprache, flexible Interpretation oder Codeerzeugung benötigt werden.

## 10.1 Erlaubte Aufgaben

- Aussagen aus Gesprächen extrahieren,
- mögliche Interpretationen vorschlagen,
- offene Fragen verständlich formulieren,
- Gegenbeispiele erzeugen,
- formale Kandidaten über fachliche Tools vorschlagen,
- Code innerhalb eines geschlossenen Vertrags implementieren,
- fehlgeschlagene Prüfungen erklären,
- technische Reparaturen innerhalb der autorisierten Semantik versuchen.

## 10.2 Verbotene Aufgaben

Das LLM darf nicht:

- Modellobjekte direkt als akzeptiert markieren,
- Graphstatus berechnen,
- Referenzen ungeprüft anlegen,
- Konflikte selbst als gelöst erklären,
- Tests als bestanden behaupten,
- Release-Evidenz erzeugen,
- Anforderungen still abschwächen,
- Produktentscheidungen ohne Freigabe treffen,
- die Build-Pipeline umgehen,
- direkten Schreibzugriff auf kanonische Dateien oder Statusfelder erhalten.

## 10.3 Minimierung des LLM-Aufwands

Kein LLM wird benötigt für:

- JSON-Schema-Prüfung,
- Referenzauflösung,
- Typprüfung,
- Abhängigkeitsanalyse,
- Statusberechnung,
- Impact-Propagation,
- deterministische Generatoren,
- Testausführung,
- Buildplanung,
- Cache-Auswertung,
- Release-Freigabe.

Das LLM erhält nur den minimalen Kontext eines aktuellen Ablaufs oder Implementierungsauftrags.

---

# 11. Fachliche Werkzeuge für das LLM

Das LLM arbeitet nicht mit technischen Graphoperationen wie `create_node` oder `create_edge`. Es verwendet eng typisierte fachliche Commands.

## 11.1 Gesprächswerkzeuge

- `record_statement`
- `propose_interpretation`
- `answer_open_question`
- `reject_interpretation`
- `request_clarification`

## 11.2 Modellierungswerkzeuge

- `propose_actor`
- `propose_entity`
- `propose_behavior`
- `propose_rule`
- `propose_user_flow`
- `propose_quality_requirement`
- `propose_profile_exception`

Beispiel:

```json
{
  "intent": "propose_rule",
  "ruleType": "authorization",
  "actor": "organization_admin",
  "action": "view",
  "resource": "project",
  "condition": "project_belongs_to_same_organization"
}
```

Der Kernel verarbeitet automatisch:

```text
fachliche Absicht
→ Schema-Prüfung
→ Referenzauflösung
→ Modellkandidat
→ Typprüfung
→ Konfliktprüfung
→ Auswirkungsanalyse
→ notwendige Entscheidung oder gültiger Commit
```

Es gibt kein LLM-Tool `validate_model_patch`. Validierung ist kein optionaler Schritt, sondern eine unvermeidbare Eigenschaft jeder Mutation.

## 11.3 Ausführungswerkzeuge

- `compile_user_flow`
- `plan_implementation`
- `implement_task`
- `run_verification`
- `deploy_preview`
- `prepare_release`

Diese Tools besitzen formale Vorbedingungen. Ein nicht geschlossener Ablauf kann nicht kompiliert werden. Ein nicht verifizierter Stand kann nicht veröffentlicht werden.

---

# 12. Der Stakeholder-Workflow

## 12.1 Freie Ideenbeschreibung

Der Stakeholder beginnt in natürlicher Sprache. Er muss nicht vorab formal denken oder ein Formular ausfüllen.

## 12.2 Produktkarte

Das System zeigt eine erste strukturierte Arbeitskarte:

```text
Verstandenes Ziel
Erkannte Akteure
Erkannte Fähigkeiten
Explizit Gesagtes
Vermutungen, nicht autorisiert
Offene Entscheidungen
Mögliche Widersprüche
```

Lose Aussagen bleiben zunächst in einem Proposal Workspace. Sie beeinflussen weder Code noch Tests.

## 12.3 Priorisierte Rückfrage

Die nächste Frage wird nicht frei durch das LLM gewählt. Das System berechnet ihre Priorität anhand von:

- Zahl blockierter Produktbereiche,
- Zahl abhängiger Entscheidungen,
- Kosten einer späteren Änderung,
- Einfluss auf sichtbares Verhalten,
- Unsicherheit,
- Sicherheits- oder Datenrisiko.

Das LLM formuliert die berechnete Frage verständlich.

## 12.4 Konsequenzdarstellung

Der Stakeholder bestätigt nicht nur einen Satz. Das System zeigt:

- die Interpretation,
- konkrete Konsequenzen,
- Grenzfälle,
- betroffene Abläufe,
- mögliche Gegenbeispiele,
- neu entstehende offene Fragen.

## 12.5 Entscheidungs-Commit

Zusammengehörige Entscheidungen werden atomar übernommen. Neue Produktsemantik benötigt ausdrückliche Freigabe. Rein deterministische Verfeinerungen innerhalb profildefinierter Regeln werden automatisch übernommen und protokolliert.

## 12.6 Auswahl des nächsten Benutzerablaufs

Das System berechnet aktuell machbare End-to-End-Abläufe. Der Stakeholder priorisiert nach Produktwert.

```text
System entscheidet: Was ist formal und technisch möglich?
Stakeholder entscheidet: Was ist als Nächstes wichtig?
```

## 12.7 Preview und Feedback

Sobald ein Ablauf vollständig geklärt ist, erzeugt die Fabrik eine lauffähige Preview auf derselben Codebasis wie das spätere Produkt.

Feedback wird an konkretem beobachtbarem Verhalten verankert. Das System ermittelt den zugehörigen Vertrag, berechnet Auswirkungen und erzeugt einen kontrollierten Änderungsvorschlag.

---

# 13. Die visuelle Oberfläche

Ein vollständiger Graph ist keine brauchbare Hauptansicht. Bei realen Produkten würde er schnell zu einem unlesbaren Netz.

Die Oberfläche verwendet mehrere Projektionen desselben kanonischen Modells.

## 13.1 Produktkarte

Zeigt Produktbereiche und ihren Reifezustand:

```text
Identity
Organizations
Projects
Tasks
Notifications
Billing
Operations
```

## 13.2 Inbox für lose Aussagen

Kategorien:

- nicht zugeordnet,
- benötigt Entscheidung,
- mögliches Duplikat,
- möglicher Widerspruch,
- außerhalb des Scopes,
- nicht verständlich.

Lose Knoten werden nicht auf der primären Produktkarte angezeigt.

## 13.3 Entscheidungsqueue

Zeigt nur Fragen, die menschliche Autorität benötigen, sortiert nach berechneter Auswirkung.

## 13.4 Ablauf-Board

Zeigt End-to-End-Abläufe und ihren Zustand:

| Ablauf | Modell | Implementierung | Verifikation | Abnahme |
|---|---|---|---|---|
| Organisation erstellen | vollständig | implementiert | bestanden | akzeptiert |
| Projekt erstellen | vollständig | implementiert | bestanden | offen |
| Aufgabe zuweisen | blockiert | – | – | – |

## 13.5 Ablaufkarte

Eine Ablaufkarte zeigt getrennt:

- Produktverhalten,
- Frontend,
- Backend,
- Persistenz,
- Sicherheit,
- Verifikation,
- Produktabnahme,
- blockierende Entscheidungen.

## 13.6 Impact-Ansicht

Bei einer Änderung zeigt sie nur den Wirkungskorridor:

```text
geänderte Sichtbarkeitsregel
→ Projektliste
→ Projektseite
→ API-Queries
→ Autorisierungstests
→ zwei End-to-End-Abläufe
```

## 13.7 Traceability-Ansicht

Sie beantwortet:

> Warum verhält sich das Produkt so?

```text
sichtbares Verhalten
← UI-Vertrag
← API-Vertrag
← Verhaltensvertrag
← akzeptierte Entscheidung
← Stakeholder-Aussage
```

## 13.8 Graphansicht

Der Graph ist eine Diagnoseansicht. Er wird gefiltert nach:

- Ursprungsknoten,
- Kantentyp,
- maximaler Tiefe,
- betroffenen Modulen,
- Änderungen seit einer Version.

---

# 14. Verhaltensverträge

Der verbindliche Kern zwischen Produktentscheidung und Implementierung ist ein formaler Verhaltensvertrag.

Er beschreibt:

- Akteur,
- Ausgangszustand,
- Eingabe,
- Vorbedingungen,
- Berechtigungen,
- Regeln,
- Zustandsänderungen,
- sichtbare Ergebnisse,
- Fehlerfälle,
- Invarianten,
- externe Effekte.

Das kanonische Format ist JSON. Der Stakeholder sieht eine daraus erzeugte Szenarioansicht.

## 14.1 Zwei Ansichten, eine Semantik

```text
kanonischer Vertrag
├── verständliche Ablaufbeschreibung
├── API-Vertrag
├── Backend-Regeln
├── Frontend-Zustände
├── Persistenzanforderungen
├── Tests
└── Implementierungsauftrag
```

Es gibt keine getrennte Spezifikation für Menschen und Maschinen. Beide Ansichten werden aus derselben Quelle erzeugt.

## 14.2 Deterministische Ableitungen

Eine automatische Ableitung ist nur erlaubt, wenn eine versionierte Profilregel existiert.

Beispiel:

```text
Command verändert geschützte Ressource
+ Autorisierungsregel referenziert handelnden Benutzer
→ Command benötigt ActorIdentity
```

LLM-Vermutungen erhalten den Status `inferred` oder `proposed`, beeinflussen aber weder Code noch Release.

---

# 15. End-to-End-Abläufe als sichtbare Arbeitseinheit

Das globale Produktmodell bleibt kanonisch. Implementiert und verifiziert wird über geschlossene End-to-End-Abläufe.

Ein Ablauf ist eine Projektion des globalen Modells, keine unabhängige Kopie.

Beispiel:

```text
Benutzer erstellt Organisation
→ erstellt Projekt
→ öffnet Projekt
→ erstellt Aufgabe
→ sieht Aufgabe im Backlog
```

Intern kann dieser Ablauf mehrere Build-Einheiten enthalten:

- organization.create
- project.create
- task.create
- project-board.query
- task-card.render

Ein Ablauf ist geschlossen, wenn alle für ihn notwendigen Entscheidungen, Verträge, Fehlerfälle, UI-Zustände und Sicherheitsregeln vorhanden sind.

Das Gesamtprodukt darf weiterhin offene Bereiche besitzen. Das ist keine Reduktion auf ein MVP. Das vollständige Ziel bleibt im Modell erhalten; nur formal geschlossene Ausschnitte werden ausführbar gemacht.

---

# 16. Automatische Modellprüfung

Jede Modelländerung wird automatisch geprüft. Es gibt keinen optionalen Validierungsschritt.

## 16.1 Prüfklassen

1. JSON-Syntax
2. JSON-Schema
3. stabile IDs und eindeutige qualifizierte Namen
4. Referenzauflösung
5. Typkompatibilität
6. erlaubte Beziehungen
7. Modulgrenzen
8. Vollständigkeit profildefinierter Objekte
9. Konflikte und Invarianten
10. Autorisierungs- und Datenbereichsregeln
11. Frontend-Backend-Konsistenz
12. Betriebs- und Release-Anforderungen

## 16.2 Automatisch erzeugte offene Punkte

Ein neues Modellobjekt kann notwendige Fragen erzeugen.

Beispiel: Eine löschbare Entity erzeugt Fragen zu:

- physischer Löschung,
- Soft Delete,
- Aufbewahrungszeit,
- abhängigen Daten,
- Auditierung,
- Wiederherstellung.

Diese Fragen sind profildefiniert. Sie werden nicht frei vom LLM erfunden.

## 16.3 Vollständigkeit

Ein Ablauf gilt als vollständig, wenn:

- keine blockierende offene Entscheidung existiert,
- keine Referenz ungelöst ist,
- kein Konflikt offen ist,
- alle beobachtbaren Ausgänge beschrieben sind,
- alle erforderlichen UI-Zustände existieren,
- alle Sicherheits- und Betriebsanforderungen für den Ablauf erfüllt sind.

---

# 17. Änderungen und Auswirkungsanalyse

Die Fabrik funktioniert wie ein semantisches inkrementelles Build-System.

## 17.1 Semantische Hashes

Jedes Modellobjekt erhält einen Hash aus seinem kanonisierten semantischen Inhalt.

Änderungen werden klassifiziert:

- redaktionell,
- technisch,
- vertraglich,
- produktsemantisch.

Nur semantisch relevante Änderungen invalidieren abhängige Artefakte.

## 17.2 Typisierte Abhängigkeiten

Beziehungen besitzen definierte Auswirkung:

| Beziehung | Wirkung einer Quelländerung |
|---|---|
| `generatedFrom` | Zielartefakt wird veraltet |
| `verifiedBy` | Evidenz wird ungültig |
| `dependsOn` | Ziel muss erneut geprüft werden |
| `implementedBy` | Implementierung wird überprüfungsbedürftig |
| `constrainedBy` | Constraint-Prüfung wird erneut ausgeführt |
| `exposedBy` | Schnittstellenvertrag wird erneut kompiliert |

## 17.3 Reverse-Dependency-Index

Der Compiler kennt nicht nur Abhängigkeiten, sondern auch alle eingehenden Referenzen. Dadurch kann er den transitiven Wirkungskorridor einer Änderung berechnen.

## 17.4 Knotenzustände werden berechnet

Status wird nicht manuell gesetzt.

```text
verified =
  alle benötigten Evidenzen gültig
  ∧ keine offene Blockierung
  ∧ keine veraltete Abhängigkeit
  ∧ keine verletzte Invariante
```

Mögliche Zustände:

- proposed
- accepted
- blocked
- stale
- invalid
- generated
- implemented
- verified
- rejected
- deferred

---

# 18. Codegenerierung und Implementierung

Nicht jeder Teil muss durch ein LLM implementiert werden.

## 18.1 Deterministisch generierbare Artefakte

- JSON-Schemas
- API-Schemas
- Typdefinitionen
- Standardvalidierung
- Datenbankmigrationen für einfache Strukturen
- Autorisierungsgerüste
- CRUD-Basisoperationen
- Testgerüste
- Contract-Tests
- Standard-UI-Zustände
- Deployment-Grundkonfiguration

## 18.2 LLM-Implementierung

Das LLM wird eingesetzt für:

- komplexere Geschäftslogik innerhalb formaler Verträge,
- individuelle UI-Komponenten,
- Integrationsadapter,
- Refactorings,
- Reparaturen nach konkreten Prüfungsfehlern,
- optimierte technische Implementierungen.

## 18.3 Minimaler Arbeitskontext

Ein Coding-Agent erhält nur:

- den aktuellen Vertrag,
- relevante Nachbarverträge,
- Projektprofil,
- betroffene Dateien,
- erlaubte Erweiterungspunkte,
- verbotene Änderungen,
- auszuführende Prüfungen.

Er erhält nicht das gesamte Produktmodell und nicht den gesamten Gesprächsverlauf.

---

# 19. Manueller Code und Erweiterungspunkte

Die Anwendung besteht aus:

```text
modellverwaltetem Kern
+ vertraglich definierten Erweiterungspunkten
+ frei implementierbaren technischen Komponenten
```

Produktsemantik muss im kanonischen Modell stehen. Technische Implementierung darf innerhalb akzeptierter Verträge frei sein.

## 19.1 Erweiterungspunkte

- Hook
- Plugin
- Adapter
- Custom Query
- Custom Component
- Domain Service
- Notification Provider
- Storage Provider

Jeder Erweiterungspunkt besitzt einen ausführbaren Vertrag.

## 19.2 Direkte Bearbeitung

Entwickler dürfen JSON-Modell und Code direkt bearbeiten. Die Änderung erhält jedoch erst dann einen gültigen Status, wenn die automatische Pipeline alle erforderlichen Prüfungen und Freigaben bestätigt.

## 19.3 Verhaltensänderung durch manuellen Code

Eine manuelle Änderung kann nicht zuverlässig nur durch LLM-Diff-Analyse klassifiziert werden. Deshalb werden betroffene Verträge erneut verifiziert.

Technische Optimierung:

```text
andere Implementierung
+ identische Vertragsresultate
→ zulässig
```

Neue Produktregel:

```text
beobachtbares Verhalten ändert sich
+ kein Modellvertrag vorhanden
→ Release blockiert
```

---

# 20. Projektlokale Spracherweiterungen

Das Webprofil wird nicht von Anfang an jedes gewünschte Verhalten ausdrücken können.

Der Umgang ist dreistufig:

1. bestehende Bausteine kombinieren,
2. projektlokalen formalen Baustein definieren,
3. wiederverwendbare Bausteine später in das Webprofil übernehmen.

Ein Custom Contract benötigt mindestens:

- stabile ID,
- qualifizierten Namen,
- typisierte Ein- und Ausgaben,
- definierte Fehler,
- Determinismus oder explizite Unsicherheitssemantik,
- ausführbare Beispiele,
- Contract- oder Property-Tests,
- Implementierungsschnittstelle,
- Versions- und Migrationsregeln,
- Provenienz.

Nicht ausdrückbare Anforderungen blockieren den betroffenen Ablauf. Sie dürfen nicht still in freien Anwendungscode ausweichen.

---

# 21. Die automatische Build- und Release-Pipeline

Jeder Commit durchläuft automatisch dieselbe Pipeline. Kein Entwickler und kein LLM kann Prüfschritte überspringen.

## 21.1 Modellphase

1. JSON-Syntax prüfen
2. JSON Schemas prüfen
3. IDs und Namen prüfen
4. Symboltabelle erzeugen
5. Referenzen auflösen
6. Typen und Modulgrenzen prüfen
7. globale Invarianten prüfen
8. neue Produktsemantik erkennen
9. erforderliche Freigaben prüfen
10. Impact-Analyse berechnen

## 21.2 Generierungsphase

11. betroffene Verträge neu kompilieren
12. betroffene Schemas und Artefakte erzeugen
13. Implementierungsplan aktualisieren
14. veraltete Evidenz invalidieren

## 21.3 Implementierungsphase

15. deterministische Generatoren ausführen
16. notwendige Coding-Agent-Aufgaben ausführen
17. statische Analyse
18. Typecheck
19. Architekturregeln
20. Build

## 21.4 Verifikationsphase

21. Unit-Tests
22. Contract-Tests
23. Property-Tests
24. Integrationstests
25. betroffene End-to-End-Abläufe
26. Autorisierungs- und Mandantentests
27. Sicherheitsprüfungen
28. Browser- und UI-Prüfungen
29. Performanceprüfungen, wenn betroffen
30. Deployment- und Migrationstests
31. Backup- und Restore-Prüfung, wenn release-relevant

## 21.5 Evidenzphase

32. unveränderlichen Evidenzbericht erzeugen
33. Evidenz an exakte Modell-, Code- und Toolchain-Version binden
34. Preview- oder Release-Status berechnen

## 21.6 Fail-closed-Regeln

```text
ungelöste Referenz
→ Build blockiert

neue Produktsemantik ohne Freigabe
→ Release blockiert

verletzter Vertrag
→ Build und Release blockiert

veraltete Evidenz
→ Release blockiert

nicht unterstütztes Verhalten
→ betroffener Ablauf blockiert
```

---

# 22. Preview, Produktabnahme und Release

## 22.1 Eine Codebasis

Preview und Produktion verwenden dieselbe Codebasis und dieselbe Modellsemantik. Es gibt keinen separaten Wegwerfprototyp.

Preview unterscheidet sich durch:

- Testdaten,
- schnelle Deployments,
- diagnostische Anzeigen,
- Kennzeichnung unfertiger Bereiche,
- nicht produktive Konfiguration.

## 22.2 Zwei getrennte Wahrheiten

```text
technische Verifikation:
Erfüllt die Software den Vertrag?

Produktabnahme:
Entspricht das beobachtbare Ergebnis der aktuellen Absicht?
```

Eine Version kann technisch verifiziert, aber noch nicht vom Stakeholder akzeptiert sein.

## 22.3 Feedback

Feedback wird an konkretem Verhalten verankert:

- Seite
- Aktion
- Ergebnis
- Fehlerzustand
- Ablauf

Die Fabrik findet den zugehörigen Vertrag, erzeugt einen Änderungsvorschlag und zeigt die Auswirkungen vor der Übernahme.

## 22.4 Release-Manifest

Nur ein reproduzierbares, signierbares Release-Manifest darf veröffentlicht werden. Es bindet:

- Produktmodell-Commit,
- Code-Commit,
- Toolchain-Lock,
- Zielumgebung,
- enthaltene Abläufe,
- benötigte Evidenz,
- Stakeholder-Akzeptanz.

---

# 23. Produktionsfähigkeit

Eine Anwendung gilt erst als funktionierend, wenn die akzeptierten Benutzerabläufe in einer reproduzierbar deploybaren Umgebung verifiziert wurden und vereinbarte Sicherheits-, Betriebs- und Datenanforderungen erfüllt sind.

## 23.1 Build

- reproduzierbar,
- Abhängigkeiten fixiert,
- Frontend und Backend gebaut,
- Artefakte versioniert.

## 23.2 Deployment

- definierte Zielumgebung,
- automatisiertes Deployment,
- kontrollierte Migrationen,
- Rollback,
- Health Checks.

## 23.3 Sicherheit

- Secrets außerhalb des Repositories,
- TLS,
- Authentifizierung,
- serverseitige Autorisierung,
- Abhängigkeitsscans,
- Mandantentrennung.

## 23.4 Betrieb

- strukturierte Logs,
- Fehlerüberwachung,
- Metriken,
- Traces,
- Alerts.

## 23.5 Daten

- Backup,
- getestete Wiederherstellung,
- Aufbewahrungsregeln,
- kontrollierte Schemaänderungen.

Der dauerhaft verwaltete Betrieb kann optional von der Fabrik übernommen oder an eine externe Plattform übergeben werden.

---

# 24. Skalierung

Die Kosten einer Änderung sollen sich nach ihrem semantischen Wirkungskorridor richten, nicht nach der Gesamtgröße des Produkts.

## 24.1 Partitionierung

Das Produkt wird in Module mit stabilen Namespaces zerlegt:

```text
identity
organization
project
task
notification
billing
operations
```

Module veröffentlichen nur definierte Verträge.

## 24.2 Content-addressed Cache

Compiler- und Generatorergebnisse werden anhand folgender Eingaben gecacht:

```text
semantischer Modell-Hash
+ Profilversion
+ Projektprofil-Hash
+ Compiler-Version
+ Binding-Version
```

Identische Eingaben erzeugen identische Ergebnisse.

## 24.3 Inkrementelle Testauswahl

- lokale Modell- und Unit-Prüfungen,
- betroffene Komponenten,
- betroffene End-to-End-Abläufe,
- vollständige Regression vor Release oder bei globalen Änderungen.

## 24.4 Parallele Jobs

Compiler-, Implementierungs-, Test- und Deployment-Aufgaben sind Jobs mit expliziten Eingaben und Outputs. Unabhängige Jobs können horizontal verteilt werden.

## 24.5 Gleichzeitige Modelländerungen

Fachliche Commands enthalten eine erwartete Modellversion. Bei Konflikten wird nicht blind geschrieben. Das System berechnet Rebase-Möglichkeiten oder fordert eine Entscheidung an.

## 24.6 Größenordnung

Die Architektur soll ein großes Produkt mit:

- einigen Tausend Modellobjekten,
- Hunderten End-to-End-Abläufen,
- mehreren Teams,
- mehreren parallelen Workern

unterstützen.

Zusätzlich verwaltet eine Fabrik-Installation mehrere isolierte Produkte pro Workspace.

---

# 25. Versionierung von Profilen, Compiler und Toolchain

Jeder Produktstand bindet konkrete Versionen:

```json
{
  "modelFormat": "1.0",
  "kernel": "2.3.1",
  "profiles": {
    "webApplication": "1.8.0",
    "projectProfile": "standard-saas@3.2.0"
  },
  "bindings": {
    "backend": "typescript@4.1.2",
    "frontend": "react@3.7.0",
    "relationalPersistence": "postgresql@2.4.1"
  }
}
```

## 25.1 Updateklassen

### Patch ohne Semantikänderung

Kann nach automatischer Neuverifikation übernommen werden.

### Kompatible Erweiterung

Fügt optionale Fähigkeiten hinzu, ohne bestehende Bedeutung zu ändern.

### Semantische Profiländerung

Benötigt eine explizite Migration und gegebenenfalls neue Stakeholder-Entscheidungen.

## 25.2 Reproduzierbarkeit alter Releases

Alte Produktversionen bleiben mit ihrer ursprünglichen Toolchain rekonstruierbar.

## 25.3 Sicherheitsupdates

Sicherheitsupdates ohne Verhaltensänderung können technisch aktualisiert und neu verifiziert werden. Ändert sich beobachtbares Verhalten, ist eine Produktentscheidung erforderlich.

---

# 26. Referenzprodukt: Projekt- und Aufgabenverwaltung

Das Referenzprodukt dient als durchgängiger Prüfstein für die Fabrik.

## 26.1 Grundmodell

- Eine Organisation ist Besitz-, Mandanten- und Sicherheitsgrenze.
- Ein Benutzer kann Mitglied mehrerer Organisationen sein.
- Organisationen besitzen Projekte.
- Projekte besitzen Aufgaben.
- Teams können als optionale Gruppen innerhalb einer Organisation existieren.

## 26.2 Rollen

Organisationsrollen:

- Owner
- Admin
- Member

Projektzugriff:

- kein Zugriff
- Teilnehmer
- Projektverwalter

## 26.3 Projektsichtbarkeit

- `organization`: alle Organisationsmitglieder können das Projekt sehen,
- `restricted`: nur Projektmitglieder sowie Owner und Admins sehen das Projekt.

Sichtbarkeit und Bearbeitungsrecht sind getrennt.

## 26.4 Aufgabenworkflow

```text
backlog
→ ready
→ in_progress
→ in_review
→ done
```

Zusätzliche Übergänge:

```text
in_progress → blocked
blocked → ready | in_progress
in_review → in_progress
done → in_progress
```

Eine blockierte Aufgabe benötigt einen Grund. Statuswechsel werden mit Benutzer und Zeitpunkt protokolliert.

## 26.5 Erster vollständiger Ablauf

```text
Benutzer erstellt Organisation
→ erstellt Projekt
→ öffnet Projekt
→ erstellt Aufgabe
→ sieht Aufgabe im Backlog
```

Dieser Ablauf prüft:

- Gespräch,
- Modellierung,
- Besitzgrenzen,
- Berechtigungen,
- Frontend,
- Backend,
- Persistenz,
- Tests,
- Preview,
- Stakeholder-Feedback,
- Release.

## 26.6 Beispieländerung

Der Stakeholder stellt in der Preview fest, dass Aufgaben nicht über eine separate Seite, sondern inline im Board angelegt werden sollen.

Die Fabrik zeigt:

- neues sichtbares Verhalten,
- Auswirkungen auf UI-Vertrag,
- Auswirkungen auf CreateTask,
- Fehlerzustände,
- betroffene Tests,
- nicht betroffene Rollen- und Workflowregeln.

Erst nach Bestätigung werden Modell und Code geändert.

---

# 27. Technische Zielarchitektur der Fabrik

```text
Conversation Interface
        ↓
Statement and Proposal Workspace
        ↓
Typed Domain Command API
        ↓
Canonical Model Repository
        ↓
JSON Schema and Semantic Compiler
        ↓
Symbol Table and Reference Resolver
        ↓
Dependency and Provenance Graph
        ↓
Constraint and Impact Engine
        ↓
Flow Compiler
        ↓
Artifact Generators and Technology Bindings
        ↓
Implementation Planner and Agent Runtime
        ↓
Verification Engine
        ↓
Evidence Store
        ↓
Preview and Release Manager
```

## 27.1 Persistenz

Empfohlene Trennung:

### Kanonisches Produktmodell

- JSON-Dateien
- Git oder vergleichbares versioniertes Repository
- immutable Modell-Commits

### Operative Datenbank

- Gesprächsverläufe
- Proposal Workspace
- Symbolindex
- Reverse-Reference-Index
- Graphprojektionen
- Buildstatus
- Testresultate
- Deploymenthistorie
- Suchindizes

### Artefaktspeicher

- content-addressed generierte Artefakte
- Buildoutputs
- Evidenzberichte
- Releasepakete

## 27.2 Kein Event Sourcing als Kern

Vollständiges Event Sourcing ist nicht notwendig. Ein unveränderliches Audit-Log für Modell- und Releaseereignisse kann sinnvoll sein, ist aber nicht die Quelle der Semantik.

Die Wahrheit liegt in versionierten JSON-Modellständen. Der Graph ist eine reproduzierbare Projektion.

---

# 28. Teststrategie

## 28.1 Kerneltests

- Parser
- Kanonisierung
- stabile Hashes
- Symbolauflösung
- Referenzfehler
- atomare Transaktionen
- Modulgrenzen
- deterministische Kompilierung

## 28.2 Profiltests

- erlaubte und verbotene Beziehungen
- Vollständigkeitsregeln
- Ableitungsregeln
- Interviewfragen
- Generatoren
- Profilmigrationen

## 28.3 Vertragstests

- Erfolgsfälle
- Ablehnungen
- Vorbedingungen
- Nachbedingungen
- Invarianten
- Autorisierung
- Fehlercodes

## 28.4 Property-Tests

Beispiele:

```text
Kein Benutzer sieht ein restricted Projekt ohne zulässigen Zugriff.

Kein Statusübergang verlässt den definierten Workflow.

Kein verifizierter Ablauf referenziert veraltete Evidenz.

Jedes veröffentlichte Verhalten besitzt einen Provenienzpfad
zu einer akzeptierten Entscheidung.
```

## 28.5 Integrationstests

- Frontend-Backend-Verträge
- Persistenz
- externe Systeme
- Jobs
- Migrationen

## 28.6 End-to-End-Tests

Die sichtbaren Benutzerabläufe werden in realer Preview- oder release-naher Umgebung geprüft.

## 28.7 Mutationstests

Für kritische Regeln wie Autorisierung oder Mandantentrennung werden gezielte Implementierungsfehler eingeführt. Die Testsuite muss sie erkennen.

## 28.8 Formale Prüfung

Für ausgewählte kritische Regeln können SMT-Solver, Model Checking oder spezialisierte Zustandsprüfer eingesetzt werden.

---

# 29. Fail-closed-Verhalten

Die Fabrik muss bei Unklarheit, Widerspruch oder fehlender Evidenz blockieren.

Sie darf niemals:

- Anforderungen still ergänzen,
- Tests abschwächen,
- Verträge umdeuten,
- fehlgeschlagene Implementierung als akzeptable Einschränkung deklarieren,
- unbekannte Semantik in freien Code verschieben.

## 29.1 Eskalationsleiter bei Implementierungsfehlern

1. lokale Reparatur,
2. alternative technische Implementierung,
3. Ursachenklassifikation,
4. automatische technische Neuplanung innerhalb akzeptierter Policies,
5. Stakeholder-Entscheidung nur bei echtem Produkt- oder Architektur-Trade-off.

Mögliche Fehlerklassen:

- implementation_error
- toolchain_limitation
- incompatible_binding
- inconsistent_contract
- non_executable_contract
- missing_profile_capability
- external_dependency_failure

Akzeptierte Verträge werden niemals automatisch abgeschwächt.

---

# 30. Mandanten- und Produktgrenzen

Eine Fabrik-Installation verwaltet mehrere Workspaces. Ein Workspace verwaltet mehrere isolierte Produkte.

```text
Factory Installation
└── Workspace
    ├── gemeinsame Profile und Policies
    ├── Produkt A
    ├── Produkt B
    └── Produkt C
```

Produktisolierung umfasst:

- Modell
- Code
- Secrets
- Build-Artefakte
- Deployments
- Evidenz
- Agentenkontext

Eine spätere SaaS-Betriebsform ergänzt Organisationsisolierung, Quoten, Abrechnung, Datenstandorte und sichere Ausführung fremden Codes.

---

# 31. Vollständiger Konstruktionsplan

Dieser Plan ist kein MVP, das das Ziel reduziert. Er ist eine Reihenfolge, in der die vollständige Architektur aufgebaut wird, ohne die spätere Zielstruktur zu verbauen.

## Stufe 1: Formalkernel

- kanonisches JSON
- JSON Schemas
- stabile IDs
- Symboltabelle
- Referenzauflösung
- atomare Modelltransaktionen
- semantische Hashes
- Abhängigkeitsgraph
- Reverse-Index
- Git-Integration

Ergebnis: Ein reproduzierbares, referenziell integres Model-as-Code-System.

## Stufe 2: Webprofil-Grundsprache

- Entity
- ValueObject
- Relation
- Actor
- Role
- Permission
- Behavior
- Command
- Query
- Invariant
- State Transition
- Error
- Page
- Form
- ViewState

Ergebnis: Funktionales Verhalten einfacher Full-Stack-Webanwendungen ist formal ausdrückbar.

## Stufe 3: Stakeholder-Interaktion

- freie Ideenbeschreibung
- Aussageextraktion
- Proposal Workspace
- Produktkarte
- priorisierte Fragen
- Konsequenzansicht
- Entscheidungs-Commits

Ergebnis: Menschliche Aussagen können kontrolliert in das formale Modell überführt werden.

## Stufe 4: Flow-Compiler

- End-to-End-Abläufe
- Ablaufvollständigkeit
- Ablaufkarten
- Impact-Ansicht
- Traceability

Ergebnis: Das Produkt wird in verständlichen, geschlossenen Benutzerabläufen sichtbar.

## Stufe 5: Referenzbindings

- Backend-Binding
- Frontend-Binding
- REST-Binding
- relationale Persistenzbindung
- Deployment-Binding

Technologien sind austauschbare Bindings. Eine erste Referenzimplementierung kann TypeScript, React und PostgreSQL verwenden, ohne diese Technologien in die Profilsemantik einzubauen.

## Stufe 6: Implementierungsagenten

- minimaler Agentenkontext
- verwaltete und freie Codebereiche
- Erweiterungspunkte
- Reparaturschleifen
- Sandbox-Ausführung

## Stufe 7: vollständige Prüf-Pipeline

- Contract-Tests
- Property-Tests
- Integrationstests
- E2E
- Sicherheitsprüfungen
- Migrationen
- Deployment
- Backup und Restore
- Evidenzberichte

## Stufe 8: Preview und Release

- gleiche Codebasis
- Preview-Deployment
- verhaltensgebundenes Feedback
- Stakeholder-Abnahme
- reproduzierbares Release-Manifest

## Stufe 9: Skalierung und Zusammenarbeit

- Module
- Caching
- verteilte Jobs
- parallele Modelländerungen
- Freigaberegeln nach Verantwortungsbereich
- mehrere Produkte pro Workspace

## Stufe 10: Erweiterbarkeit

- projektlokale Custom Contracts
- Profilplugins
- Profilmigrationen
- zusätzliche Projektprofile
- weitere Domänenprofile

Jede Stufe implementiert einen Teil der endgültigen Zielarchitektur. Keine Stufe definiert ein anderes, kleineres Produktziel.

---

# 32. Offene Forschungsprobleme

Folgende Probleme sind nicht vollständig gelöst und müssen explizit als Forschungs- oder Entwicklungsrisiken geführt werden:

1. Zuverlässige Erkennung semantisch gleicher Aussagen.
2. Zuverlässige Erkennung versteckter Widersprüche in natürlichsprachlichen Anforderungen.
3. Abgrenzung zwischen technischer Änderung und neuer Produktsemantik.
4. Vollständigkeit automatisch erzeugter Verhaltensverträge.
5. Konformitätsprüfung frei implementierten Codes jenseits testbarer Verträge.
6. Formale Modellierung qualitativer UI- und UX-Eigenschaften.
7. Skalierung projektlokaler Spracherweiterungen ohne Fragmentierung.
8. Automatische Architekturänderungen mit kontrollierter Semantikerhaltung.
9. Generierung ausreichend starker Testorakel ohne dieselbe Fehlinterpretation wie der Implementierungsagent.
10. Beweisbare Sicherheit von Mandantentrennung in komplexen erzeugten Systemen.
11. Sichere Ausführung von durch Agenten erzeugtem oder fremdem Code.
12. Kostenkontrolle bei großen Wirkungskorridoren und globalen Änderungen.

Diese Probleme verhindern nicht die Implementierung der Fabrik. Sie begrenzen aber die Stärke ihrer Garantien.

---

# 33. Schlussfolgerung

Die Software-Fabrik ist kein autonomes LLM, das eine Idee errät und Code schreibt. Sie ist ein formales Entwicklungssystem mit einem LLM an klar begrenzten sprachlichen und implementierenden Schnittstellen.

Ihr Kern besteht aus:

```text
versioniertem JSON-Produktmodell
+ referenzieller Integrität
+ Web-Application-Profil
+ Projektprofilen
+ ausführbaren Verhaltensverträgen
+ End-to-End-Abläufen
+ inkrementellem Abhängigkeitsgraph
+ automatischer Build- und Prüf-Pipeline
+ kontrollierter Codegenerierung
+ Stakeholder-Abnahme
+ reproduzierbaren Release-Manifesten
```

Das entscheidende Prinzip lautet:

> Das LLM darf Vorschläge machen und Code erzeugen. Der formale Kernel entscheidet, was gültig, vollständig, betroffen, verifiziert und veröffentlichbar ist.

Damit entsteht keine magische, garantiert fehlerfreie Übersetzung menschlicher Gedanken. Es entsteht jedoch eine belastbare Maschine, die menschliche Produktabsicht schrittweise explizit macht, nicht autorisierte Annahmen isoliert, Änderungen nachvollziehbar propagiert und nur nachweisbar autorisierte Software veröffentlicht.

---

# Anhang A: Beispielstruktur eines Produkt-Repositories

```text
/product
  manifest.json

  /modules
    /identity
      module.json
      /actors
      /value-types
      /behaviors
      /policies

    /organization
      module.json
      /entities
      /behaviors
      /policies
      /flows

    /project
      module.json
      /entities
      /behaviors
      /queries
      /policies
      /ui
      /flows

    /task
      module.json
      /entities
      /states
      /behaviors
      /queries
      /ui
      /flows

  /quality
    performance.json
    accessibility.json
    availability.json

  /operations
    environments.json
    deployment.json
    backup.json
    restore.json
    observability.json

  /profiles
    project-profile-lock.json
    toolchain-lock.json

/generated
  /contracts
  /schemas
  /tests
  /implementation-plans

/src
  /factory-managed
  /application
  /extensions

/evidence
  /builds
  /tests
  /security
  /deployments
  /releases
```

---

# Anhang B: Beispiel eines kanonischen Verhaltensvertrags

```json
{
  "$schema": "https://factory.example/schemas/behavior/v1.json",
  "kind": "behavior",
  "id": "01JPROJECTCREATE00000000001",
  "qualifiedName": "project.create",
  "displayName": "Projekt erstellen",
  "modelVersion": 3,
  "profile": "web-application@1.0",
  "actor": {
    "type": {
      "$ref": "01JORGMEMBER00000000000001",
      "expectedKind": "actor"
    },
    "requiresPermission": {
      "$ref": "01JPERMISSIONPROJECTCREATE01",
      "expectedKind": "permission"
    }
  },
  "input": {
    "organizationId": {
      "type": {
        "$ref": "01JORGANIZATIONID000000001",
        "expectedKind": "valueType"
      }
    },
    "name": {
      "type": {
        "$ref": "01JPROJECTNAME000000000001",
        "expectedKind": "valueType"
      }
    }
  },
  "preconditions": [
    {
      "rule": "organization.exists",
      "arguments": ["input.organizationId"]
    },
    {
      "rule": "actor.memberOf",
      "arguments": ["input.organizationId"]
    }
  ],
  "rules": [
    {
      "kind": "unique",
      "field": "normalized(input.name)",
      "partitionBy": "input.organizationId",
      "among": {
        "entity": {
          "$ref": "01JPROJECT00000000000000001",
          "expectedKind": "entity"
        },
        "where": {
          "status": "active"
        }
      },
      "violation": "PROJECT_NAME_ALREADY_EXISTS"
    }
  ],
  "effects": [
    {
      "operation": "create",
      "entity": {
        "$ref": "01JPROJECT00000000000000001",
        "expectedKind": "entity"
      },
      "values": {
        "organizationId": "input.organizationId",
        "name": "input.name",
        "status": "active"
      }
    },
    {
      "operation": "grantAccess",
      "actor": "currentActor",
      "resource": "createdEntity",
      "access": "project_manager"
    }
  ],
  "success": {
    "result": "createdEntity",
    "visibleOutcomes": [
      "projectAppearsInProjectList",
      "actorCanOpenProject"
    ]
  },
  "rejections": [
    {
      "when": "actor.unauthenticated",
      "error": "UNAUTHENTICATED"
    },
    {
      "when": "actor.permissionMissing",
      "error": "FORBIDDEN"
    },
    {
      "when": "input.name.invalid",
      "error": "INVALID_NAME"
    },
    {
      "when": "rule.unique.violated",
      "error": "PROJECT_NAME_ALREADY_EXISTS"
    }
  ]
}
```

---

# Anhang C: Zustände und Statusbegriffe

## Gespräch und Modellierung

- `captured`: aus einer Aussage extrahiert
- `proposed`: als mögliche Modelländerung vorgeschlagen
- `inferred`: LLM-Vermutung ohne Autorität
- `accepted`: vom Stakeholder akzeptierte Produktsemantik
- `derived`: durch versionierte formale Regel abgeleitet
- `rejected`: ausdrücklich verworfen
- `deferred`: bewusst vertagt

## Gültigkeit

- `valid`: strukturell und semantisch gültig
- `blocked`: durch offene Entscheidung oder Konflikt blockiert
- `invalid`: formale Regel verletzt
- `stale`: Abhängigkeit hat sich geändert

## Implementierung

- `specified`: implementierbarer Vertrag vorhanden
- `implementation_pending`: Implementierung fehlt
- `implemented`: Code vorhanden
- `verification_pending`: Prüfungen fehlen
- `verified`: alle erforderlichen Evidenzen gültig
- `accepted_by_stakeholder`: Produktabnahme erfolgt
- `release_ready`: Release-Manifest kann erzeugt werden

---

# Anhang D: Release-Manifest

```json
{
  "kind": "release",
  "id": "01JRELEASE0000000000000001",
  "productModelCommit": "model-a81f",
  "codeCommit": "code-c72e",
  "toolchainLock": "toolchain-4f19",
  "targetEnvironment": "production",
  "includedFlows": [
    "organization.create",
    "project.create",
    "task.create",
    "task.move"
  ],
  "requiredEvidence": {
    "modelCompilation": "passed",
    "contractVerification": "passed",
    "securityVerification": "passed",
    "deploymentVerification": "passed",
    "restoreVerification": "passed"
  },
  "stakeholderAcceptance": {
    "required": true,
    "acceptedModelCommit": "model-a81f",
    "acceptedPreviewDeployment": "preview-912"
  },
  "signatures": [
    {
      "role": "release-system",
      "algorithm": "example-signature-algorithm",
      "value": "..."
    }
  ]
}
```
