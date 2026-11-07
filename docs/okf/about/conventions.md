---
type: Guide
title: How this documentation is written
description: The format and writing rules every page in this bundle follows (OKF v0.2 plus a plain-language style).
resource: https://github.com/pjerra/turtle-agent/tree/dev/docs/okf
tags: [docs, conventions]
sources:
  - id: okf
    resource: https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md
    title: Open Knowledge Format (OKF) specification, v0.2
generated:
  by: claude-opus-5-5/lead
  at: 2026-11-05T21:30:00+01:00
status: draft
---

# How this documentation is written

This folder is an **Open Knowledge Format (OKF) v0.2 bundle**[^okf]: a tree of markdown files, one topic per file, each starting with a small YAML header. You can read it on GitHub like any other docs folder; tools can also read the headers.

## The header on every page

Every page except `index.md` and `log.md` starts with a block like this:

```yaml
---
type: Guide            # required. One of: Overview, Tutorial, Guide, Concept, Reference, Troubleshooting
title: Run a dungeon   # the page title
description: One sentence saying what the page helps you do.
resource: https://github.com/pjerra/turtle-agent/blob/dev/<main source file or folder>
tags: [dungeon, commander]
sources:               # the code or docs the page was written from
  - id: dungeon-go
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/dungeon/dungeon.go
generated:
  by: <writer>/<version>
  at: 2026-11-05T21:30:00+01:00   # ISO 8601 with an explicit offset
status: draft          # draft until a person has checked it
---
```

- `type` is the only required key. The page types used here:
  - **Overview**: what an area is and how its parts fit.
  - **Tutorial**: a first, guided walk through one task, start to finish.
  - **Guide**: how to do one task (numbered steps).
  - **Concept**: how something works and why.
  - **Reference**: every command, flag, field or code, listed.
  - **Troubleshooting**: a symptom, its cause, and the fix.
- `sources` lists the files the page describes. A claim taken from one source can carry a footnote whose label is that source's `id`, like this[^okf].
- `status: draft` stays until a person reviews the page; then `verified: {by: "human:<name>", at: ...}` is added.

## Folders and links

- One folder per area. Each folder has an `index.md` with no header: a short intro line, then grouped bullet links, each with a one-line description.
- Links use paths relative to the page, for example `[raid-walk](../raids/raid-walk-reference.md)` from a page in another area folder; a link to a page that does not exist yet is fine.
- `log.md` at the root records changes, newest date first.

## Writing style

Follow the Google developer documentation style:
- Write to the reader as "you". Use the present tense and the active voice.
- Short sentences. One idea per paragraph. Explain a term the first time you use it.
- Lead with what the reader can do; put background after.
- Steps are numbered, one action each. Show the exact command in a code block.
- Every flag or field in a Reference page gets: name, what it does, default, an example.
- No internal shorthand, no ticket numbers, no walk numbers, no server names or addresses, no passwords or tokens. Use placeholders like `<server>` and `<slot>`.

[^okf]: Open Knowledge Format (OKF) specification, v0.2.
