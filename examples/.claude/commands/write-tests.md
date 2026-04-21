---
description: Write tests for the function or file the user names.
argument-hint: "<function-or-file>"
allowed-tools: ["Read", "Edit", "Write", "Bash(go test:*)"]
x-triggers: ["write tests", "add tests", "test coverage"]
x-requires-repo: true
---

Add tests for $ARGUMENTS. Match the existing test style in the same
package. Cover:

- the happy path
- at least one error/edge case
- any non-obvious branch

Run the tests before declaring done. If they fail, fix the code or the
test — don't commit red tests.
