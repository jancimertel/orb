CREATE TABLE IF NOT EXISTS chat_state (
  chat_id INTEGER PRIMARY KEY,
  active_repo_alias TEXT,
  active_session_id TEXT,
  active_model TEXT,
  updated_at TEXT
);

CREATE TABLE IF NOT EXISTS repos (
  chat_id INTEGER,
  alias TEXT,
  url TEXT,
  path TEXT,
  default_branch TEXT,
  added_at TEXT,
  PRIMARY KEY(chat_id, alias)
);

CREATE TABLE IF NOT EXISTS usage (
  chat_id INTEGER,
  day DATE,
  input_tokens INTEGER DEFAULT 0,
  output_tokens INTEGER DEFAULT 0,
  cache_read_tokens INTEGER DEFAULT 0,
  cache_write_tokens INTEGER DEFAULT 0,
  cost_usd REAL DEFAULT 0,
  PRIMARY KEY(chat_id, day)
);
