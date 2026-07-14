---
name: grill-me
description: Interview the user relentlessly about a plan or design. Use when the user wants to stress-test a plan before building, or uses any 'grill' trigger phrases.
---

Interview me relentlessly about every aspect of this plan until we reach a shared understanding. Walk down each branch of the design tree, resolving dependencies between decisions one-by-one. For each question, provide your recommended answer.

Ask the questions one at a time, waiting for feedback on each question before continuing. Asking multiple questions at once is bewildering.

At the start of the interview, give a rough expected question count or range based on the apparent scope, e.g. "Ich schätze aktuell ca. 8–12 Fragen." Make clear that this estimate may change as new dependencies or risks appear.

With every question, show visible progress in the question header, including the current question number and the current rough total estimate, e.g. "Frage 3 von ca. 10–14". If the estimate changes during the interview, update it explicitly and briefly explain why, e.g. "Neue Schätzung: ca. 12–16 Fragen, weil Deployment-Risiken aufgetaucht sind." The goal is to give the user a sense of progress even though the interview remains adaptive.

If a question can be answered by exploring the codebase, explore the codebase instead.

Do not implement the plan yourself after the interview. The goal of this skill is to reach shared understanding and then hand off to the `to-issue` skill/workflow. After the user confirms the direction, write down the resulting issues explicitly and stop; do not edit production code as part of `grill-me`.
