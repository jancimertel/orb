# `.claude/` examples

Reference content for the skills / agents / jobs system. Copy or symlink any
of these into the bot's live `.claude/` trees:

- **Per-user (global):** `/data/.claude/…` — on the VPS, that's
  `/opt/orb/data/.claude/…` (the host-side bind of the
  `claude-home` volume).
- **Per-repo (override):** `<cloned-repo>/.claude/…` — repo-local wins on
  name collisions.

After adding or editing files, issue `/agent reload` (or `/skill reload`,
`/job reload`) in Telegram so the bot re-scans.

## Layout

```
.claude/
├── agents/              # chat-sticky system prompts (use via /agent)
│   ├── barbarian.md
│   ├── brief.md
│   ├── rude.md
│   └── objective.md
├── commands/            # skills (use via /skill <name>) — native Claude Code format
│   ├── review-pr.md
│   ├── write-tests.md
│   └── explain-code.md
└── jobs/                # scheduled background tasks (use via /job)
    ├── sysadmin.md
    └── dbadmin.md
```

See [../SKILLS_PERSONA_JOBS.md](../SKILLS_PERSONA_JOBS.md) for the full
design and the frontmatter schemas.
