---
description: Review the current diff, flag bugs, unclear naming, missing tests.
argument-hint: "[base-branch]"
allowed-tools: ["Bash(git diff:*)", "Bash(git log:*)", "Read"]
x-triggers: [review, "check diff", "look at my changes"]
x-requires-repo: true
---

Act as a staff engineer reviewing the diff against $ARGUMENTS (default:
`main`). Run `git diff $ARGUMENTS...HEAD` if you haven't already. Focus on:

- correctness and edge cases
- naming and clarity
- tests that should exist but don't

Be specific and cite `file:line`. Skip style nits.
