# Skills, Agents & Jobs — Design Draft

> Status: **draft / manifest**. Not yet scheduled. Written to align terminology
> and data model before any Go code is touched.

This document proposes three orthogonal extensions to the Telegram Claude bot:

| Concept   | Lifetime           | Trigger                | Scope              |
|-----------|--------------------|------------------------|--------------------|
| **Skill** | One-shot           | User command / auto    | Current turn only  |
| **Agent** | Sticky (per chat)  | `/agent` command       | All future turns   |
| **Job**   | Long-running       | Scheduler / manual run | Background process |

They are independent but share one thing: **all three are just prompt material
stored on disk as markdown with YAML frontmatter**, loaded at spawn/turn time.
No new AI runtime. The Claude CLI subprocess already does all the work.

---

## 1. Shared foundation: the `.claude/` content store

Reuse Claude Code's native config directory layout instead of inventing one.
The bot's HOME is already `/data`, so the Claude CLI already reads/writes
`/data/.claude/`. Dropping our content in the same tree means:

- **Portability** — the same files work if someone runs `claude` directly
  in this environment, or copies them into a repo's local `.claude/`.
- **Zero new parsing surface for native types** — Claude Code already
  defines the schema for commands and subagents; we match it byte-for-byte.
- **Per-repo overrides for free** — if the active repo contains a
  `.claude/` of its own, we merge user-global (from `/data/.claude/`) with
  repo-local (from `<repo>/.claude/`), repo-local wins on name collision.

### Directory layout

```
/data/.claude/                    ← user-global, on the volume
├── commands/                     ← SKILLS (native Claude Code slash-command format)
│   ├── review-pr.md
│   ├── write-tests.md
│   └── explain-code.md
├── agents/                       ← PERSONAS (native subagent format, used as system prompt)
│   ├── brief.md
│   ├── rude.md
│   └── objective.md
├── jobs/                         ← JOBS (bot-specific; no native equivalent)
│   ├── sysadmin.md
│   └── dbadmin.md
└── projects/                     ← already used: session JSONL files (untouched)

<active-repo>/.claude/            ← optional per-repo overrides
├── commands/
├── agents/
└── jobs/
```

### Mapping to existing Claude Code conventions

| Our concept | Native Claude Code equivalent | Directory          | We honor native schema? |
|-------------|-------------------------------|--------------------|-------------------------|
| Skill       | Slash command (`/foo`)        | `.claude/commands/`| **Yes** — verbatim       |
| Agent       | Subagent definition           | `.claude/agents/`  | **Yes**, but we use only the body as system prompt (no sub-agent spawn) |
| Job         | *(none — bot extension)*      | `.claude/jobs/`    | Our schema               |

### Frontmatter schemas

**Skills** — native Claude Code command format:

```markdown
---
description: Review the diff on the current branch and flag issues.
argument-hint: "[path]"
allowed-tools: ["Bash(git diff:*)", "Read"]
---

Run `git diff` and review for correctness and clarity. Argument: $ARGUMENTS
```

Bot-specific extensions (optional, ignored by native Claude Code):

```yaml
x-triggers: [pr, review, "check diff"]   # for auto/suggest mode
x-requires-repo: true
```

Filename = command name (`review-pr.md` → invoked as `/review-pr` or
`/skill review-pr`).

**Agents** — native subagent format:

```markdown
---
name: brief
description: Ultra-terse. Code and file paths only. No preamble.
model: inherit
---

Respond in ≤3 sentences unless asked for detail...
```

We ignore the `tools:` field (agents don't spawn a sub-agent) and only use
the body as `--append-system-prompt` content.

**Jobs** — our own schema (no native counterpart):

```markdown
---
name: sysadmin
description: Scan last 15 min of container logs for errors.
cron: "*/15 * * * *"
cwd: /data
model: claude-haiku-4-5-20251001
notify_on: warn
bash_allow:
  - "docker logs --since 15m .*"
---

<job prompt body>
```

**Why markdown + frontmatter:** aligns with Claude Code's own format so
files are portable. Editable on disk, reloaded on command via
`/skill reload`, `/agent reload`, `/job reload`.

### Storage vs. state

- Content lives on disk (volume) — survives restarts, editable out-of-band.
- State lives in SQLite — which agent is active per chat, which jobs are
  enabled, job run history.

New tables:

```sql
-- agent binding per chat (nullable = default agent)
ALTER TABLE chat_state ADD COLUMN active_agent TEXT;

-- jobs: one row per (chat, job_name) enablement
CREATE TABLE jobs (
  chat_id        INTEGER,
  name           TEXT,
  enabled        INTEGER DEFAULT 1,
  cron_expr      TEXT,            -- e.g. "*/15 * * * *"
  last_run_at    TEXT,
  last_status    TEXT,            -- ok | error | running
  last_summary   TEXT,
  PRIMARY KEY(chat_id, name)
);

-- job run log (bounded; prune by age)
CREATE TABLE job_runs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id     INTEGER,
  name        TEXT,
  started_at  TEXT,
  ended_at    TEXT,
  status      TEXT,
  summary     TEXT,
  session_id  TEXT                 -- claude session the run used
);
```

---

## 2. Skills

### What

A **skill** is a short, named, reusable prompt recipe — "review the current
diff", "write tests for the function I pasted", "explain this error". The user
either invokes one explicitly or the bot suggests one based on triggers.

### Telegram UX

Skills are invoked **only** through the `/skill` command. We do *not*
auto-register `.claude/commands/*.md` as first-class Telegram slash
commands — keeps the command surface small and discoverable.

| Command                  | Effect                                                                 |
|--------------------------|------------------------------------------------------------------------|
| `/skill`                 | List installed skills (inline keyboard, one button per skill)          |
| `/skill <name>`          | Apply the skill to the **next** message or run it immediately if its body is self-contained |
| `/skill <name> <text>`   | One-shot: prepend the skill to `<text>` and send in this turn          |
| `/skill info <name>`     | Show description + full body                                           |
| `/skill reload`          | Re-scan `.claude/commands/` (global + repo-local) from disk            |

**One skill per turn.** No chaining in v1 — if the user picks `review-pr`
and then `write-tests` for the same message, the second selection
replaces the first.

### Auto-application (semi-automatic)

Two modes, both opt-in per chat (`chat_state.skill_auto = on|off|suggest`):

1. **Suggest mode** (default): before sending a turn, if any skill's
   `triggers` list matches keywords in the user's message, render an
   inline keyboard: `[🎯 Use: review-pr] [Continue plain]`. No magic, no
   surprise.
2. **Auto mode**: matched skill is applied silently; the receipt line
   (see below) tells the user which one fired.

### Receipt (required)

> "skill used should be printed when used" — mandatory.

When a skill is applied, the bot emits a single dimmed status line at the top
of the streaming reply, before Claude's output:

```
🎯 Skill: review-pr
```

Identical treatment to the existing tool-use status lines in
[stream_renderer.go](internal/telegram/stream_renderer.go).

### How it plugs in

- `internal/agent/` new package (shared by all three concepts):
  - `loader.go` — scans `.claude/commands/`, `.claude/agents/`, `.claude/jobs/` in both `/data/.claude/` and `<active-repo>/.claude/`; parses frontmatter; caches; repo-local wins on name collision.
  - `matcher.go` — trigger matching for suggest/auto modes.
- Turn pipeline ([turn.go](internal/telegram/turn.go)):
  1. Resolve active agent (system message candidate).
  2. Resolve skill (if any) — prepended to the user turn's text content.
  3. Send to `claude.Runner` as usual.
- The skill body is injected **as user-turn preamble**, not as a system
  prompt, so it doesn't fight with agent content.

### Example skill file — `.claude/commands/review-pr.md`

```markdown
---
description: Review the current diff, flag bugs, unclear naming, missing tests.
argument-hint: "[base-branch]"
allowed-tools: ["Bash(git diff:*)", "Bash(git log:*)", "Read"]
x-triggers: [review, "check diff", "look at my changes"]
x-requires-repo: true
---

Act as a staff engineer reviewing the diff against $ARGUMENTS (default: main).
Focus on:
- correctness and edge cases
- naming and clarity
- tests that should exist but don't
Be specific and cite file:line. Skip style nits.
```

---

## 3. Agents (chat-sticky system prompts)

### What

An **agent** shapes *how* Claude talks — terse vs. verbose, formal vs. blunt,
objective vs. opinionated. It is a **system-level instruction** that persists
across turns for the chat.

Each file lives in `.claude/agents/<name>.md` using Claude Code's native
subagent format; the bot uses only the body as `--append-system-prompt` and
does not spawn a sub-agent.

### Telegram UX

| Command              | Effect                                         |
|----------------------|------------------------------------------------|
| `/agent`             | List agents, mark active with ✓                |
| `/agent <name>`      | Switch active agent                            |
| `/agent info <name>` | Show description + full body                   |
| `/agent off`         | Clear active agent (no system-prompt append)   |
| `/agent reload`      | Re-scan `.claude/agents/` (global + repo-local) from disk |

### Persistence

Active agent = `chat_state.active_agent`. Empty → no append (or
`default.md` if a repo-scoped default is found — see below).

### How it plugs in

- Agent body is injected into the Claude subprocess as an **append to
  the system prompt**, via the CLI's `--append-system-prompt` flag
  (already supported by the claude CLI). No new protocol work.
- Switching agent triggers `registry.Reset` (same mechanism as
  `/model` today) so the next spawn picks up the new system prompt.

### Example agent files

`.claude/agents/brief.md`:

```markdown
---
name: brief
description: Ultra-terse. Code and file paths only. No preamble.
model: inherit
---

Respond in ≤3 sentences unless asked for detail. Never preface with
"Sure", "Let me", "I'll". Cite file:line for all references. Omit
summaries of what you just did.
```

`.claude/agents/rude.md`:

```markdown
---
name: rude
description: Blunt, dismissive of hand-wavy requests. Still correct.
model: inherit
---

If the user's request is vague, say so bluntly and demand specifics
before proceeding. Mock hand-waving. Do not sugarcoat problems in the
code. The technical content must remain accurate.
```

### Interaction with skills

- Agent = **how** (system prompt).
- Skill = **what for this turn** (user-turn preamble).
- They compose: `brief` agent + `review-pr` skill → terse PR review.

### Interaction with `CLAUDE.md`

If the active repo ships a `CLAUDE.md`, both are loaded. On direct conflict
(e.g. both specify a voice/tone), **agent wins** — it's the chat-level
intent the operator just selected. `CLAUDE.md` continues to own everything
else (project conventions, architecture notes).

### Repo-scoped default agent

If a selected repo contains `.claude/agents/default.md`, it auto-activates
as the agent for that chat when the repo is selected, unless the operator
has explicitly set an agent via `/agent <name>` in this chat. Clearing
with `/agent off` returns to the (possibly auto-activated) default.

---

## 4. Jobs

### What

A **job** is a recurring background task the bot runs on its own: tail system
logs for errors, scan postgres for slow queries, check disk space. It is
effectively *a skill plus a schedule plus a result-delivery channel*.

### Telegram UX

| Command                          | Effect                                                   |
|----------------------------------|----------------------------------------------------------|
| `/job`                           | List jobs with enabled/disabled + last run status        |
| `/job enable <name> [cron]`      | Enable; optional cron overrides file default             |
| `/job disable <name>`            | Pause (do not delete)                                    |
| `/job run <name>`                | Run once, now, inline                                    |
| `/job info <name>`               | Show definition, cron, last run, last summary            |
| `/job log <name> [N]`            | Last N runs with status + 1-line summary                 |
| `/job reload`                    | Re-scan `.claude/jobs/` (global + repo-local) from disk  |

### Delivery

- Default: post a **summary message** to the chat only when the job's
  result is flagged `notify: true` by the job prompt itself (Claude
  decides based on content — "errors found" → notify, "all clean" →
  silent). A "nothing to report" result goes to the log table only.
- `/job log <name>` surfaces silent runs on demand.
- Severity threshold (`notify_on: any | warn | error`) configurable in
  the job's frontmatter; enforced by the runner by parsing a required
  trailing JSON block from the job output (see [Output contract](#output-contract) below).

### Scheduling

- Cron parser: `github.com/robfig/cron/v3` (already battle-tested,
  pure-Go, MIT).
- Single global scheduler goroutine at startup reads the `jobs` table,
  registers one cron entry per enabled job.
- Adding/removing/enabling/disabling a job re-registers entries atomically.

### Execution model

**Jobs get their own Claude process**, isolated from interactive turns:

```
scheduler tick
   └─ job.Runner.Run(chat_id, job_name)
         ├─ Spawn claude with:
         │     HOME=/data, cwd = job.cwd (default: workspace root),
         │     model = job.model (default: configured default),
         │     --permission-mode acceptEdits (same gating applies —
         │     Bash is still approval-gated by the bot)
         ├─ Send job body as the single user turn
         ├─ Parse result event
         ├─ Write to job_runs
         └─ If notify: post summary to chat_id
```

**Approval during jobs:** if a job's Bash use hits the approval hook, it
times out and auto-denies after `JOB_APPROVAL_TIMEOUT_SEC` (shorter than
interactive default — default 10 s). An owner can optionally pre-approve
by listing allowed Bash patterns in the job frontmatter (`bash_allow:`).
This is the only expansion to the approval module.

### Concurrency

- At most one job per chat runs at a time. Collisions queue.
- Jobs never run while an interactive turn is active for the same chat —
  the scheduler defers.
- Global cap (`MAX_CONCURRENT_JOBS`, default 2) to protect the host.

### Output contract

The job prompt must instruct Claude to end with a machine-readable block:

```
<<<JOB_RESULT
{"status":"ok|warn|error","summary":"<one line>","notify":true|false}
JOB_RESULT>>>
```

If absent or unparseable: `status=error`, `notify=true`, summary =
"job did not return a JOB_RESULT block".

### Example job file — `.claude/jobs/sysadmin.md`

```markdown
---
name: sysadmin
description: Scan last 15 min of container logs for errors.
cron: "*/15 * * * *"
cwd: /data
model: claude-haiku-4-5-20251001
notify_on: warn
bash_allow:
  - "docker logs --since 15m .*"
  - "journalctl --since '15 minutes ago' .*"
---

You are sysadmin-on-duty. Look at the last 15 minutes of logs for the
containers running on this host. Summarize any warnings or errors.
Ignore known noisy patterns (DEBUG level, health-check 200s).

End your response with the required JOB_RESULT JSON block:
- status=error if anything looks like a real failure
- status=warn for elevated noise / retries
- status=ok otherwise
- notify=true unless ok
```

`.claude/jobs/dbadmin.md`:

```markdown
---
name: dbadmin
description: Check postgres for slow queries and bloat.
cron: "0 */6 * * *"
cwd: /data
bash_allow:
  - "psql -h db -U readonly -c .*"
---

Connect to postgres as the readonly user and check:
- queries in pg_stat_activity running > 30s
- pg_stat_user_tables for tables with dead_tup > 10_000

Summarize findings. End with the JOB_RESULT block.
```

---

## 5. Permissions / safety

- **Skills and agents are trusted content** — owner-authored, on the
  volume or inside the cloned repo. No user-submitted text executes as a
  skill without the owner dropping a file in `.claude/`.
- **Repo-local `.claude/` is auto-trusted** because only the allow-listed
  operator can add/select repos. If this ever becomes multi-user,
  repo-local skills must move behind an explicit "trust this repo" gate.
- **Jobs inherit the existing Bash approval gate.** `bash_allow` in the
  frontmatter is the only escape hatch and is regex-matched against the
  command string before the approval UI is skipped.
- **Rate limiting:** jobs posting to Telegram share the existing
  streaming renderer's edit-rate discipline.
- **Cost visibility:** job runs go into `usage` accounting like any
  other turn. `/usage` already surfaces today + MTD.

---

## 6. Phased delivery

| Phase | Scope                                                                         | Rough effort |
|-------|-------------------------------------------------------------------------------|--------------|
| A     | `internal/agent/` loader: scan `/data/.claude/{commands,agents,jobs}/` + `<repo>/.claude/…`, parse native frontmatter, cache, hot reload | S |
| B     | **Agent** end-to-end: `/agent` commands, `--append-system-prompt` wiring, `registry.Reset` | S |
| C     | **Skill** explicit mode: `/skill`, `/skill <name>`, one-shot preamble, receipt line | M |
| D     | Skill auto/suggest mode + trigger matcher                                     | S |
| E     | **Jobs** schema + scheduler + `/job` CRUD + manual run only                   | M |
| F     | Jobs cron activation + notification delivery + output contract parser         | M |
| G     | `bash_allow` pre-approval integration                                         | S |

Ship A→B first (smallest, highest-leverage — agent alone changes the
feel of the bot). Skills next. Jobs last — they are the most surface
area and need the others' plumbing.

---

## 7. Decisions

Resolutions to the open questions raised in earlier drafts (see the
relevant sections above for where each decision is applied):

| # | Question                                     | Decision                                                                 |
|---|----------------------------------------------|--------------------------------------------------------------------------|
| 1 | Agent vs. `CLAUDE.md` precedence             | Both load. Agent wins on conflict.                                       |
| 2 | Skill composition                            | One skill per turn. No chaining in v1.                                   |
| 3 | Job chat-scoping                             | Keep `chat_id` in schema. Single-operator default for now.               |
| 4 | Editing skills/agents/jobs from Telegram     | Deferred. Edit on disk; `/…reload` is the only v1 update path.           |
| 5 | Repo-scoped default agent                    | `.claude/agents/default.md` in a selected repo auto-activates as agent.  |
| 6 | Expose skills as first-class slash commands  | **No.** Only `/skill <name>` — do not auto-register `/review-pr` etc.    |
