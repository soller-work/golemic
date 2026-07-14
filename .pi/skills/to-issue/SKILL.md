---
name: to-issue
description: Turn the current conversation into one or more backlog issues, insert them into docs/backlog/backlog.md, and commit planning artifacts.
disable-model-invocation: true
---

# To Issue

Turn the current conversation context, typically the result of a `grill-me` interview, into one or more vertical backlog issues for this repository.

Do not restart the interview. Synthesize from what has already been discussed. Only ask follow-up questions when there is a real blocker that cannot be resolved by inspecting the repository.

## Repository conventions

- Issue backlog: `docs/backlog/backlog.md`
- Issue files: `docs/backlog/*.md`
- `docs/backlog/backlog.md` is the only source of open, processing, and completed state.
- Issue files contain stable scope details only.
- `docs/backlog/backlog.md` has separate `Bug Issue Order` and `Feature Issue Order` active lists plus `Completed Issue History`.
- Bugs are selected before features. Within the chosen list, the bottom-most claimable issue is selected first.
- New issues are inserted at the top of the matching `Bug Issue Order` or `Feature Issue Order` list (newest-first).
- If the prior conversation explicitly agreed on a different ordering, follow that ordering.
- If the new work must preempt existing open work but this was not explicitly agreed, ask for confirmation before inserting anywhere other than the top.
- Do not read from or write to `docs/todo.md` or `docs/slices/` for this workflow.

## Process

### 1. Gather context

Work from the existing conversation. Identify:

- the problem or opportunity,
- the user-visible behavior desired,
- key implementation decisions already made,
- explicit non-goals and constraints,
- any discussed dependencies or sequencing.

Explore the repository instead of asking questions when repository state can answer them. Read `docs/backlog/backlog.md` and relevant existing issue files before drafting. Do not fall back to `docs/todo.md` or `docs/slices/`.

### 2. Classify and propose the issue breakdown

Classify each issue as `bug` or `feature`:

- `bug` — a defect or incorrect behavior that needs fixing.
- `feature` — a new capability or enhancement.

Draft one or more thin vertical issues. An issue must deliver a narrow, complete, demoable or test-observable behavior. Avoid horizontal issues such as "build the API", "add the UI", or "refactor internals" unless the issue itself has a clear observable outcome.

Be strict about issue quality:

- each issue is demo- or test-observable on its own,
- each issue is vertical across the thinnest useful path,
- each issue is small enough for one focused implementation pass,
- prefactors are only separate issues when they are independently verifiable,
- each issue has explicit out-of-scope boundaries,
- dependencies are expressed as issue IDs in `depends_on`.

Present the proposed breakdown and wait for approval before writing files. For each proposed issue, show:

- issue ID,
- title,
- type: `bug` or `feature`,
- why it is a separate issue,
- blocked-by / order relationship,
- rough acceptance criteria.

Ask whether the split, types, and order are right. Iterate until approved.

### 3. Write issue files

After approval, write one Markdown file per issue under `docs/backlog/`.

Use kebab-case issue IDs derived from the title:

- short but unambiguous,
- no date prefixes,
- if an ID or filename would collide, choose a more precise ID instead of appending `-2`.

The filename must be `<issue-id>.md`. Use this template:

```md
---
id: <issue-id>
title: <Issue Title>
type: <bug | feature>
depends_on: []
---

# <Issue Title>

## Goal

<The narrow outcome this issue delivers.>

## User-visible behavior

<What a user or tester can observe after this issue is complete.>

## Scope

- <Included behavior or implementation boundary>

## Acceptance criteria

- [ ] <Observable/testable criterion>

## Out of scope

- <Explicit non-goal>
```

Rules:

- Frontmatter must contain `id`, `title`, `type`, and `depends_on` in that order.
- `type` must be either `bug` or `feature`.
- `id` must match the issue ID used in `docs/backlog/backlog.md`.
- `title` should match the H1.
- `depends_on` must be an array of issue IDs.
- Do not include `status`, `claimed_by`, `claimed_at`, or `completed_at` in issue files.
- Claim and completion state lives only in `docs/backlog/backlog.md`.

### 4. Update the backlog

Update `docs/backlog/backlog.md` under the matching active section (`Bug Issue Order` or `Feature Issue Order`) with open links to the new issue files.

Default behavior:

- insert new issues at the **top** of the matching active list,
- preserve the approved order among the new issues,
- use dependency-compatible ordering for newly created dependent issues,
- do not reorder existing issues unless the conversation explicitly approved doing so.

Each active backlog row must use this format:

```md
- [ ] `issue-id` [Issue Title](issue-id.md)
```

Completed rows remain in the backlog as `[x]` rows without links. Do not create or edit completed issue files except as explicitly requested by the human.

### 5. Commit the planning artifacts

After writing the issues and updating `docs/backlog/backlog.md`:

1. Run `git status --short`.
2. Ensure only planning artifacts are included: `docs/backlog/backlog.md`, `docs/backlog/*.md`, and, when this skill itself is being created or changed, `.pi/skills/to-issue/SKILL.md`. The `type` field in issue frontmatter is a valid part of the planning artifact.
3. Do not run product checks such as `npm run check`; no product code changed.
4. Commit the changes.

Use a concise commit message, for example:

```text
docs: add backlog issues for <topic>
```

If there are unrelated working-tree changes, do not stage or commit them. Stop and ask the user how to proceed.
