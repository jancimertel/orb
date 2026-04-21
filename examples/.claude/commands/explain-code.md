---
description: Explain the function or file the user names, with citations.
argument-hint: "<function-or-file>"
allowed-tools: ["Read", "Grep", "Glob"]
x-triggers: [explain, "how does", "walk me through"]
---

Explain $ARGUMENTS. Structure:

1. **What it does** — one sentence.
2. **Inputs / outputs** — types and meaning.
3. **Flow** — the interesting branches, with `file:line` citations.
4. **Gotchas** — anything that would trip up a new reader.

Skip restating obvious code. Assume the reader can read Go; highlight what
isn't obvious from the signature.
