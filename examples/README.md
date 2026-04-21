# `.claude/` examples

Reference content for the skills / agents / jobs system. Copy or symlink any
of these into the bot's live `.claude/` trees:

- **Per-user (global):** `/data/.claude/…` — on the VPS, that's
  `/opt/orb/data/.claude/…` (the host-side bind of the
  `claude-home` volume).
- **Per-repo (override):** `<cloned-repo>/.claude/…` — repo-local wins on
  name collisions.

After adding or editing files, issue `/agent reload` (or `/command reload`,
`/skill reload`, `/job reload`) in Telegram so the bot re-scans.

## Layout

```
.claude/
├── agents/              # chat-sticky system prompts (use via /agent)
│   ├── barbarian.md
│   ├── brief.md
│   ├── rude.md
│   └── objective.md
├── commands/            # one-shot preambles (use via /command <name>) — native Claude Code format
│   ├── review-pr.md
│   ├── write-tests.md
│   └── explain-code.md
├── skills/              # native Claude Code skills — auto-discovered behavior packs (see via /skill)
│   ├── commit/
│   │   └── SKILL.md
│   └── gh/
│       └── SKILL.md
└── jobs/                # scheduled background tasks (use via /job)
    ├── sysadmin.md
    └── dbadmin.md
```

`commands/` vs. `skills/`:
- **`commands/`** — short, named recipes invoked explicitly (aliases /
  one-shot preambles). The bot exposes these via `/command <name>` in
  Telegram.
- **`skills/<name>/SKILL.md`** — native Claude Code skill packs. Describe
  *correct behavior in a situation*; Claude auto-selects them based on the
  `description` frontmatter. The bot's `/skill` command lists installed
  skills for visibility but does not invoke them — Claude decides when.

See [../SKILLS_PERSONA_JOBS.md](../SKILLS_PERSONA_JOBS.md) for the full
design and the frontmatter schemas.
