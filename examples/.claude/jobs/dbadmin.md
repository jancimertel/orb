---
name: dbadmin
description: Check postgres for slow queries and table bloat.
cron: "0 */6 * * *"
cwd: /data
notify_on: warn
bash_allow:
  - "psql -h db -U readonly -c .*"
---

Connect to postgres as the `readonly` user and check:

- queries in `pg_stat_activity` running > 30 s
- `pg_stat_user_tables` for rows with `dead_tup > 10000`

Summarize findings. End with the `JOB_RESULT` JSON block — see sysadmin.md
for the contract. `status=warn` is the right default once you find real
slow queries or meaningful bloat.
