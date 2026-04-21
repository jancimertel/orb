# syntax=docker/dockerfile:1

# ---- build stage ---------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads separately from source so dep changes don't bust the
# source-compile layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/bot ./cmd/bot \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/approvalhook ./cmd/approvalhook

# ---- runtime stage -------------------------------------------------------
# The runtime needs Node because the Claude Code CLI is a Node package.
FROM node:22-slim

# git: cloning + committing. openssh-client: not used today but harmless
# and useful if we ever add ssh-based remotes. tini: PID 1 reaper so the
# Claude subprocess and its children get cleaned up on container stop.
RUN apt-get update && apt-get install -y --no-install-recommends \
      git openssh-client ca-certificates tini golang \
  && rm -rf /var/lib/apt/lists/* \
  && npm install -g @anthropic-ai/claude-code \
  # node:22-slim already ships a `node` user at uid 1000; drop it so our
  # `bot` user can take that UID (the conventional operator UID on Linux
  # hosts, which keeps bind-mount ownership predictable).
  && userdel --remove node \
  && useradd -m -u 1000 bot \
  && mkdir -p /data/.claude /data/workspaces /data/state /data/secrets \
  && chown -R bot:bot /data

COPY --from=build /out/bot /usr/local/bin/bot
COPY --from=build /out/approvalhook /usr/local/bin/orb-approvalhook

# Example .claude/ tree baked into the image. On startup the bot seeds any
# empty subdir under $HOME_DIR/.claude/ from here (see internal/agent/seed.go).
# Bind-mount or extend by adding files under <repo>/examples/.claude/ before
# building. Runtime reads are owned by `bot` since seeding runs as that UID.
COPY --chown=bot:bot examples/.claude/ /opt/bot/examples/.claude/
ENV EXAMPLES_DIR=/opt/bot/examples/.claude

USER bot
# HOME must point at a persistent volume so Claude Code's session JSONL
# files survive restarts. Our config's HOME_DIR defaults to /data to match.
ENV HOME=/data
WORKDIR /data

# The bot rewrites /tmp/alive every 30s. `find -mmin -2` prints the path
# only if it was modified in the last two minutes; piping to grep turns any
# output into exit 0 and silence into exit 1.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD find /tmp/alive -mmin -2 2>/dev/null | grep -q . || exit 1

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/bot"]
