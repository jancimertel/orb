# Orb – High-Level Manifest
### v2.0 — pikoclaw/openclawd

---

## 1. Overview

Orb is a secure, modular, and extensible AI-powered bot platform written in **Go**, designed to orchestrate tasks across multiple environments and interfaces. It serves as a unified gateway to large language models, with primary focus on Anthropic's Claude models while remaining flexible for multi-model routing. Orb operates within a strongly secured, containerized ecosystem and supports automation, knowledge management, and infrastructure operations with a focus on **programming and IT/SysAdmin workflows**.

---

## 2. Core Objectives

- Provide a unified interface for interacting with Anthropic's Claude models.
- Support optional routing to multiple LLM providers.
- Enable secure task execution within dockerized environments.
- Maintain detailed metrics on token usage and operational performance.
- Facilitate communication through multiple interfaces, with primary focus on Telegram.
- Ensure full compatibility with standard Claude skills, rules, and behaviors.
- Offer extensibility for diverse real-world use cases such as project management, knowledge assistance, and infrastructure operations.
- Enforce strong security and safe handling of sensitive data, including API keys.
- Deliver an expressive **personality and response-quality system** with configurable presets and reasoning modes.

---

## 3. Implementation Language: Go

Orb is implemented entirely in **Go (Golang)** for the following reasons:

- **Concurrency model:** Goroutines and channels are a natural fit for concurrent LLM streaming, webhook processing, and background task execution.
- **Low memory footprint:** Critical for long-running containerized services.
- **Static binaries:** Single-binary deployments simplify Docker images and reduce attack surface.
- **Strong stdlib:** Native HTTP, TLS, crypto, and context-cancellation support without heavy dependencies.
- **gRPC and protobuf:** First-class support for inter-service communication.

### Go-Specific Architecture Decisions

- All services expose typed, context-aware interfaces (`context.Context` propagated everywhere).
- Streaming LLM responses handled via `io.Reader` pipelines, never buffered in full.
- Configuration uses structured Go structs with `envconfig` or `viper`, not raw maps.
- Error handling follows Go idioms — no panics in library code, explicit sentinel errors for known failure modes.
- All shared state is protected via `sync.RWMutex` or channel-based ownership.
- Dependency injection via constructors (no global singletons except logger).
- Module structure: `cmd/`, `internal/`, `pkg/` with clean separation of concerns.

---

## 4. Architecture Components

### 4.1 LLM Gateway

- Primary wrapper for Anthropic Claude API (streaming + non-streaming).
- Optional router supporting multiple LLM providers (OpenAI, Ollama, local models).
- Unified request/response schema — provider-agnostic internally.
- Model selection and fallback strategies.
- Rate limiting (token bucket), retry with exponential backoff, circuit breaker.
- SSE (Server-Sent Events) streaming support for real-time response delivery.

### 4.2 Metrics & Token Management

Persistent tracking of:

- Input and output tokens per request, session, and user.
- Cost estimation per request, with configurable pricing tables per model.
- Request latency (time-to-first-token, total stream duration).
- Per-user budget limits and alerts.
- Preset and thinking-mode impact on token consumption (see §6).

Exportable via Prometheus-compatible endpoints.

### 4.3 Interface Layer

**Telegram Interface (Primary)**
- Command-based and conversational interactions.
- Role-based access control per chat/user ID.
- File upload and download support.
- Task initiation and status monitoring.
- Inline keyboards for preset/thinking mode switching.

**Additional Interfaces**
- Standard I/O (CLI, useful for local development and scripting).
- REST API for external service integration.
- Webhook support.
- Future extensibility for other messaging platforms (Discord, Matrix).

### 4.4 Claude Compatibility Layer

- Full adherence to Anthropic Claude system prompts and behavior guidelines.
- Support for reusable **skills** (see §7) and rule sets.
- Configurable system instructions per channel, user, or session.
- Context window management with smart truncation strategies.
- Prompt templating engine for standardized interactions.

### 4.5 Execution Environment

- Strongly isolated **Docker-based sandbox** for all code execution and task automation.
- Controlled resource allocation (CPU, memory, disk) via cgroup limits.
- Network access restrictions per task type (allowlist-based).
- Ephemeral containers for sensitive or untrusted operations.
- Persistent volumes for managed project data and caches.

### 4.6 Security & Secret Management

- Secure storage of API keys and credentials via encrypted environment variables, Docker Secrets, or HashiCorp Vault.
- Role-Based Access Control (RBAC) enforced at the gateway and interface layers.
- Audit logging for all sensitive operations.
- Secure communication via TLS everywhere.
- Principle of least privilege for all components.

---

## 5. Session Memory System

Orb maintains **in-session memory** to preserve context across multi-turn interactions within a single conversation session. Memory operates at multiple granularities:

### 5.1 Short-Term Memory (In-Context)

- Full conversation history kept in context for the duration of a session.
- Configurable maximum context window (token-budgeted).
- Automatic truncation using a **sliding window + importance scoring** strategy:
  - Recent messages always retained.
  - Older messages evaluated for relevance via lightweight heuristic (keyword overlap, entity recurrence).
  - System/tool messages exempt from truncation.
- Context compaction: when approaching limits, Orb runs a summarization pass using a small Claude call to produce a rolling summary injected as a synthetic system message.

### 5.2 Working Memory (Structured State)

Per-session structured state stored in Go maps (in-process, no DB required for ephemeral sessions):

```go
type SessionState struct {
    UserID        string
    ConversationID string
    ActivePreset  Preset
    ThinkingMode  ThinkingMode
    Personality   PersonalityProfile
    TokensUsed    TokenBudget
    Context       []Message
    WorkingMemory map[string]any  // ad-hoc KV for tool results, file refs, task IDs
    CreatedAt     time.Time
    LastActiveAt  time.Time
}
```

### 5.3 Persistent Memory (Cross-Session)

Optional persistence layer for long-term user preferences and project state:

- Storage backend: SQLite (default, zero-infrastructure) or PostgreSQL for production.
- Stored: personality preferences, active preset, past project references, custom rules, pinned knowledge snippets.
- Loaded automatically on session start if available.
- Never includes raw message history unless user explicitly opts in.

### 5.4 Memory Injection

At session start, Orb assembles a memory block injected into the system prompt:

```
[Memory]
User preferences: prefers concise answers, Go developer, Linux sysadmin.
Active project: pikoclaw/openclawd — Go monorepo, Docker-based.
Last session summary: Reviewed containerization architecture, discussed Vault integration.
```

---

## 6. Personality & Preset System

Orb supports a fully configurable **personality layer** that shapes how Claude responds. This layer has three dimensions: **Persona**, **Preset** (response complexity), and **Thinking Mode** (reasoning depth).

### 6.1 Persona Configuration

Each Orb instance can be given a named personality profile with the following fields:

```go
type PersonalityProfile struct {
    Name        string   // e.g. "Orb", "Dev", "Sysadmin"
    Tone        string   // e.g. "dry and direct", "collegial", "formal"
    Expertise   []string // domains: "Go", "Linux", "DevOps", "security"
    Quirks      []string // subtle behavioral flavor: "prefers examples over theory"
    Language    string   // "en", "cs", etc.
}
```

Built-in presets: `default`, `devops`, `security-auditor`, `pair-programmer`, `architect`. User-defined personas can be added and saved.

### 6.2 Response Presets

Presets control **output complexity**, **response length**, and **verbosity of reasoning**. Each preset translates into specific prompt instructions and token budget adjustments.

| Preset | Description | Token Impact | Best For |
|---|---|---|---|
| `low` | Minimal, direct, no elaboration. One-liners preferred. | Baseline | Quick lookups, status checks, yes/no answers |
| `simple` | Clear and concise, short explanations, no deep dives. | +15% | Everyday tasks, familiar topics |
| `advanced` | Detailed, structured, covers edge cases and alternatives. | +50% | Code review, architecture decisions, debugging |
| `master` | Exhaustive, includes rationale, tradeoffs, examples, and follow-up suggestions. | +150% | Deep technical audits, design documents, learning complex topics |

Preset is toggled via:
- Telegram inline keyboard or command (`/preset advanced`)
- API parameter in every request
- Session-level default in user config

Token consumption increases are cumulative with thinking mode (§6.3).

### 6.3 Thinking Mode

Thinking mode controls whether Orb uses **extended reasoning** before responding. When enabled, the model spends additional compute "thinking through" the problem before producing a final answer — leading to more accurate and cautious responses, but higher latency and token cost.

| Mode | Description | Token Impact | Latency Impact |
|---|---|---|---|
| `off` | Standard inference, no extended thinking. | Baseline | Minimal |
| `light` | Brief internal scratchpad. Good for ambiguous or multi-step requests. | +30% | +2–5s |
| `deep` | Full chain-of-thought reasoning. Used for complex problems, debugging, and security analysis. | +100% | +10–30s |

Rules:
- Thinking mode is **independent of preset** and can be combined freely. A `low`+`deep` response produces a terse final answer backed by thorough reasoning.
- When thinking is active, Orb informs the user with a `🔍 Thinking...` status message in Telegram before streaming the answer.
- Token cost for thinking is tracked separately in metrics.
- `deep` mode is automatically suggested (but not forced) by Orb when it detects signals of problem complexity: multi-file diffs, security-sensitive topics, production incidents, or long unanswered error chains.

### 6.4 Preset + Thinking Interaction Matrix

```
           thinking=off   thinking=light   thinking=deep
low         ▒░░░           ▒▒░░             ▒▒▒░
simple      ▒▒░░           ▒▒▒░             ▒▒▒▒
advanced    ▒▒▒░           ▒▒▒▒             ▒▒▒▒▒
master      ▒▒▒▒           ▒▒▒▒▒            ▒▒▒▒▒▒
```

Token budget legend: each `▒` ≈ 25% of the baseline single-turn budget. Master + deep is the most expensive configuration and should be reserved for high-stakes, time-insensitive operations.

---

## 7. Skills & Rules System

Skills are modular, composable behaviors loaded into Claude's system context. Rules are hard constraints that operate independently of preset/thinking.

### 7.1 Skill Structure

```
skills/
  builtin/
    coding/        # Go, Python, shell scripting conventions
    sysadmin/      # log analysis, incident response, cron
    security/      # CVE lookups, code auditing, hardening
    devops/        # CI/CD, Docker, k8s, IaC
    knowledge/     # markdown, Obsidian, note management
  user/            # user-defined, loaded per session or globally
```

Each skill is a structured prompt fragment with metadata:

```go
type Skill struct {
    ID          string
    Name        string
    Description string
    Prompt      string   // injected into system message
    Tags        []string
    TokenCost   int      // estimated tokens added to context
}
```

Skills are activated explicitly (`/skill coding`) or auto-detected from session context (file extension seen, language detected in message).

### 7.2 Rules

Rules are unconditional constraints injected before skills and persona in the system message. They do not change with preset or thinking mode. Examples:

- Never suggest running code as root.
- Always prefer reversible operations in sysadmin tasks.
- Always add error handling in generated Go code.
- Never output secrets, tokens, or private keys in plaintext.

---

## 8. Functional Capabilities

### 8.1 Multi-Model Routing

- Default routing to Anthropic Claude (primary).
- Optional routing to OpenAI, Ollama (local), or other providers.
- Configurable routing policies: cost-optimized, latency-optimized, capability-matched.
- Automatic fallback on provider errors.

### 8.2 Knowledge & Memory

- Access to local and remote knowledge bases.
- Markdown-based repositories (Obsidian vaults, git-tracked wikis).
- Semantic search and contextual retrieval via embeddings.
- Persistent session memory with cross-session continuity (§5).

### 8.3 Task Orchestration

- Execution of automated workflows inside Docker containers.
- Scheduling and background job management.
- Integration with external services via connectors.
- Real-time task monitoring and reporting via Telegram.

---

## 9. Example Use Cases

### 9.1 Go Pair Programmer

- Analyze Go codebases cloned into isolated containers.
- Review PRs, suggest refactors, explain unfamiliar patterns.
- Run tests and return structured failure summaries.
- Use `advanced` or `master` preset with `deep` thinking for architectural reviews.
- Enforce project-specific rules (error handling style, naming conventions) via skill injection.

### 9.2 GitHub Project Manager

- Clone and manage GitHub repositories within Docker containers.
- Execute build, test, and deployment tasks.
- Receive and process task instructions via Telegram.
- Monitor repository state, open PRs, and provide status updates.

### 9.3 Knowledge-Based Virtual Assistant

- Access and manage Markdown files (Obsidian vaults, git wikis).
- Provide contextual answers and summaries.
- Assist with note organization and knowledge retrieval.
- Maintain persistent user-specific context across sessions (§5.3).

### 9.4 Virtual Infrastructure Employee (SysAdmin)

- Monitor system metrics (CPU, memory, disk, network) via connectors.
- Analyze logs and detect anomalies with `deep` thinking enabled.
- Execute maintenance and remediation tasks in containerized shells.
- Provide alerts and operational insights via Telegram.
- Security hardening suggestions via the `security` skill.

### 9.5 Security Auditor

- Static analysis of code and configurations.
- CVE and dependency vulnerability checks.
- Dockerfile and Kubernetes manifest hardening reviews.
- Produces audit reports in `master` preset format.
- Always runs with `deep` thinking enabled.

---

## 10. API Design

### 10.1 Core Endpoints

- `POST /v1/chat` — Send prompts to the LLM. Accepts `preset`, `thinking`, `persona`, `session_id`.
- `GET /v1/metrics` — Retrieve token and usage statistics.
- `POST /v1/tasks` — Create and manage execution tasks.
- `GET /v1/tasks/{id}` — Retrieve task status.
- `POST /v1/connectors` — Register external integrations.
- `GET /v1/health` — System health check.
- `GET /v1/sessions/{id}` — Retrieve session state including active preset and memory summary.
- `DELETE /v1/sessions/{id}` — Clear session state.

### 10.2 Chat Request Schema (v1)

```json
{
  "session_id": "uuid",
  "message": "Review this Dockerfile for security issues.",
  "preset": "master",
  "thinking": "deep",
  "persona": "security-auditor",
  "skills": ["security", "devops"],
  "attachments": []
}
```

### 10.3 Authentication

- Token-based authentication (JWT or API keys).
- Optional OAuth2 integration.
- Rate limiting and per-user access controls.

---

## 11. Deployment Model

### 11.1 Containerization

- All services packaged as minimal Docker images (distroless or scratch-based Go binaries).
- Docker Compose for local/single-node deployments.
- Kubernetes-ready with Helm chart.
- Environment-specific configuration via Docker Secrets or Vault agent injection.

### 11.2 Scalability

- Stateless API services for horizontal scaling.
- Session state externalizable to Redis for multi-instance deployments.
- Task queue backed by NATS or Redis Streams.
- Load balancing with health-check-aware routing.

### 11.3 Observability

- Prometheus metrics at `/metrics`.
- Structured JSON logging (zerolog or zap).
- Distributed tracing via OpenTelemetry (OTLP export).
- Health and readiness probes on `/v1/health`.
- Per-request trace IDs propagated to all downstream calls.

---

## 12. Security Principles

- **Isolation:** All code execution in ephemeral Docker sandboxes with no host mounts.
- **Confidentiality:** Strong encryption for data at rest (AES-256) and in transit (TLS 1.3).
- **Integrity:** Signed and verified container images (cosign).
- **Authentication:** JWT with short-lived tokens; API key rotation enforced.
- **Authorization:** Fine-grained RBAC for all operations.
- **Auditability:** Append-only audit log for all sensitive operations, including preset/thinking changes and secret access.
- **Secret Protection:** API keys stored in Vault or Docker Secrets, never in environment variables in production.
- **No Secret Leakage Rule:** A hard rule (§7.2) prevents Claude from ever outputting secrets in plaintext regardless of prompt or preset.

---

## 13. Data Management

### 13.1 Storage

- Encrypted storage for all persistent data.
- Separation of operational data and secrets.
- Versioned configuration via git-tracked files or Vault KV v2.
- SQLite for lightweight single-node persistence; PostgreSQL for production.
- Backup via scheduled snapshots to object storage (S3-compatible).

### 13.2 Logging & Auditing

- Centralized structured logging of all interactions and actions.
- Audit trails for administrative and security events.
- Configurable log retention policies.
- Integration with Loki, Elasticsearch, or compatible log aggregators.

---

## 14. Extensibility

- Plugin-based connector architecture (Go interface: `Connector`).
- Skills are plain text files — no compilation required, hot-reloadable.
- Event-driven design for workflow automation (webhooks, NATS subjects).
- SDK stub planned for external connector development.

---

## 15. Naming Conventions

- **Orb:** Individual bot instance or node.
- **Orbit:** Optional overarching orchestration layer managing multiple Orbs.
- **Connectors:** Modules enabling integration with external systems (GitHub, Vault, shell, etc.).
- **Skills:** Reusable Claude-compatible behaviors and rule sets.
- **Preset:** Response complexity tier (`low`, `simple`, `advanced`, `master`).
- **Thinking Mode:** Reasoning depth control (`off`, `light`, `deep`).
- **Persona:** Named personality profile shaping tone, expertise, and style.

---

## 16. Non-Functional Requirements

- High security and compliance-readiness.
- Sub-100ms overhead for non-LLM request handling.
- High availability with graceful degradation on LLM provider outages.
- Modular and maintainable Go codebase with >80% test coverage target.
- Comprehensive API documentation (OpenAPI 3.1).
- Backward compatibility with Claude-based workflows and skill formats.

---

## 17. Summary

Orb is a secure, extensible, Claude-compatible AI bot platform written in Go, designed for professional programming and IT operations. It integrates LLM capabilities with a sophisticated **personality and preset system** — including configurable response complexity (low → master) and reasoning depth (off → deep thinking) — alongside in-session and cross-session memory, modular skills, and containerized task execution. Its architecture emphasizes security, low overhead, and seamless fit within real-world developer and SysAdmin workflows.
