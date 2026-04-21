---
name: gh
description: How to use the GitHub CLI (`gh`) for PRs, issues, reviews, checks, and releases from inside a chat turn. Use whenever the user asks to open / view / comment on a PR or issue, check CI status, request a review, merge, or otherwise interact with GitHub beyond plain `git push`.
---

# Using `gh`

`gh` is pre-installed and pre-authenticated — `$GH_TOKEN` is wired from
the bot's `GITHUB_PAT` at startup. **Do not** run `gh auth login`, print
the token, or try to log in interactively. If a `gh` command fails with
an auth error, stop and report it; don't try to re-authenticate.

Prefer `gh` over raw GitHub URLs or the REST API: it respects the active
repo's remote, handles pagination, and produces clean output for the
chat.

## When to reach for `gh`

| User asks… | Use |
|---|---|
| "open a PR / send this as a PR" | `gh pr create` |
| "what's the status of the PR" / "is CI green" | `gh pr status`, `gh pr checks` |
| "show me PR #N" / "review this PR" | `gh pr view N`, `gh pr diff N` |
| "comment on PR / issue" | `gh pr comment`, `gh issue comment` |
| "list / find issues" | `gh issue list` |
| "merge the PR" | `gh pr merge` (only on explicit user confirmation) |
| "what changed since the last release" | `gh release view`, `gh api` for commit ranges |

For anything else, check `gh <sub> --help` before inventing flags.

## Before creating a PR

1. `git status` and `git log <base>..HEAD` to see what's actually in the
   branch. Read **all** commits, not just the most recent one.
2. Confirm the branch is pushed (`gh` will error otherwise). If not, ask
   the user before pushing — pushing is externally visible.
3. Draft the title and body from the real diff, not from the user's
   one-liner. Keep the title under 70 chars; put detail in the body.

Create with a HEREDOC so multiline bodies render correctly:

```sh
gh pr create --title "short imperative title" --body "$(cat <<'EOF'
## Summary
- bullet one
- bullet two

## Test plan
- [ ] step one
EOF
)"
```

## Reading PRs and issues

- `gh pr view <n>` and `gh issue view <n>` give a human summary. Pass
  `--json <fields> -q <jq>` when you need to pipe into other commands
  rather than screen-scraping the pretty output.
- `gh pr diff <n>` is the fastest way to pull a PR's patch for review
  without checking it out.
- `gh api repos/<owner>/<repo>/pulls/<n>/comments` for inline review
  comments (they don't show up in `gh pr view`).

## Hard rules

- **Never** run `gh pr merge`, `gh pr close`, `gh issue close`, or
  `gh release create` without explicit user confirmation in this turn.
  These are visible to the whole team.
- **Never** force-push to the PR branch (`git push --force`) without
  being asked. `--force-with-lease` is the safer variant if you must.
- **Never** print `$GH_TOKEN` or echo it into a command that logs its
  env.
- If a `gh` command produces long output (e.g. `gh pr list` with many
  PRs), summarize — don't dump the full table back to the user.

## Done

Report the PR/issue URL or the final state (e.g. "merged", "commented").
Nothing else.
