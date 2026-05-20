# qwen-code-web v1 Design

**Status:** Draft v1  
**Project:** `qwen-code-web`  
**Purpose:** A local CLI tool that exposes a browser UI for an active Qwen Code TUI session.

---

## 1. One-line Summary

`qwen-code-web` is a command you run in a project folder. It spawns Qwen Code in that folder, starts a local HTTP + WebSocket server, and opens a browser tab where you can chat with the running agent, approve tool requests, and see streaming output — all without touching the terminal.

---

## 2. Usage

```bash
# Start a fresh session
qwen-code-web --project-dir ./my-app

# Continue the last session
qwen-code-web --project-dir ./my-app --resume

# Custom port
qwen-code-web --project-dir ./my-app --port 4000
```

On launch:

1. Reads `.qwen-code-web/last-session-id` if `--resume` is passed.
2. Spawns `qwen` via node-pty with `--json-file` and `--input-file` pointed at session files.
3. Starts HTTP + WebSocket server (default port 3000).
4. Opens browser automatically.

---

## 3. What This Is Not

- Not a hosted platform. It runs on your machine, for you.
- Not a terminal emulator. The browser UI is a structured chat renderer, not xterm.js.
- Not a shell. The browser cannot issue arbitrary commands — only structured Qwen Code inputs.
- No database. No auth. No workspace resolver.

---

## 4. Architecture

```
qwen-code-web --project-dir ./my-app
│
├─ node-pty
│    └─ spawn qwen
│         cwd = ./my-app
│         --json-file  .qwen-code-web/events.jsonl
│         --input-file .qwen-code-web/input.jsonl
│
├─ Event tailer
│    └─ tail events.jsonl → parse JSONL → WebSocket broadcast
│
└─ HTTP server (Express or Hono)
     ├─ GET  /            → serves browser UI (single HTML file)
     ├─ POST /message     → appends submit command to input.jsonl
     ├─ POST /approve     → appends confirmation_response to input.jsonl
     ├─ POST /stop        → kills qwen process
     └─ WS  /events       → streams parsed events to browser
```

---

## 5. Session Files

All session state lives in the project folder under `.qwen-code-web/`:

```
.qwen-code-web/
├─ events.jsonl        ← Qwen Code writes events here
├─ input.jsonl         ← server writes commands here
└─ last-session-id     ← plain text, written on session_start
```

These files persist across server restarts. On `--resume`, the server passes the stored session ID to Qwen Code so it resumes context. The browser replays `events.jsonl` on connect to restore conversation history.

Permissions:

```
.qwen-code-web/  0700
events.jsonl     0600
input.jsonl      0600
```

---

## 6. Process Launch

```ts
import pty from "node-pty";
import path from "node:path";

const args = ["--json-file", eventsPath, "--input-file", inputPath];
if (sessionId) args.push("--resume", sessionId);

const child = pty.spawn("qwen", args, {
  cwd: projectDir,
  cols: 220,
  rows: 50,
  env: { ...process.env, TERM: "xterm-256color" },
});
```

The PTY is entirely server-side. The browser never touches it.

---

## 7. Event Flow (Server → Browser)

The server tails `events.jsonl` using byte-offset reads on a 50ms interval and forwards each parsed line over WebSocket as a typed message.

```
Qwen Code
  └─ writes JSONL to events.jsonl

Server (event tailer)
  └─ reads new bytes every 50ms
  └─ parses each line
  └─ broadcasts to all connected WebSocket clients

Browser
  └─ receives typed events
  └─ maps each event to a UI component
```

On WebSocket connect (or reconnect), the server replays all lines from `events.jsonl` from the beginning so the browser can reconstruct the full conversation.

---

## 8. Input Flow (Browser → Qwen Code)

### Send a message

```
Browser POST /message { text: "Fix the failing tests" }
  └─ server appends to input.jsonl:
       {"type":"submit","text":"Fix the failing tests"}
```

### Approve or deny a tool request

```
Browser POST /approve { requestId: "req_123", allowed: true }
  └─ server appends to input.jsonl:
       {"type":"confirmation_response","request_id":"req_123","allowed":true}
```

**Note:** Qwen Code polls `input.jsonl` at 500ms. After hitting Send, the user may wait up to half a second before Qwen Code picks up the message. The UI should show a brief "sending…" state to avoid confusion.

---

## 9. Dual Output Event Schema

Events from `events.jsonl` follow the Qwen Code Dual Output protocol. The events the UI cares about:

| Event | Meaning |
|---|---|
| `system / session_start` | Session started; contains `session_id` and `cwd` |
| `system / session_end` | Session ended cleanly |
| `stream_event / content_block_delta` | Streaming text chunk from assistant |
| `stream_event / message_start` | Assistant turn begins |
| `stream_event / message_stop` | Assistant turn ends |
| `assistant` | Completed assistant message (with tool_use blocks and token usage) |
| `user` | Completed user message (with tool_result blocks) |
| `control_request` | Qwen wants to use a tool — needs approval |
| `control_response` | Tool approval was resolved (either side) |

---

## 10. Browser UI

Single-page app. No framework required beyond a WebSocket connection and DOM updates, though React is fine if preferred.

### Layout

```
┌─────────────────────────────────────────────────┐
│  qwen-code-web  •  my-app  •  ● running         │
├─────────────────────────────────────────────────┤
│                                                 │
│  User                                           │
│    Fix the failing tests                        │
│                                                 │
│  Qwen                                           │
│    I'll start by reading the test output...     │
│    ▊                          ← streaming       │
│                                                 │
│  ┌─ Tool request ──────────────────────────┐   │
│  │  run_shell_command                       │   │
│  │  npm test                                │   │
│  │                    [Allow]  [Deny]       │   │
│  └──────────────────────────────────────────┘   │
│                                                 │
├─────────────────────────────────────────────────┤
│  Message...                             [Send]  │
└─────────────────────────────────────────────────┘
```

### Component Map

| Event | Component |
|---|---|
| `stream_event / text_delta` | Append to `<StreamingText>` buffer |
| `assistant` (complete) | `<AssistantMessage>` with markdown + tool_use cards |
| `user` (complete) | `<UserMessage>` |
| `control_request` | `<ApprovalCard>` with Allow / Deny |
| `control_response` | Dismiss `<ApprovalCard>` |
| `system / session_start` | Status bar → "running" |
| `system / session_end` | Status bar → "stopped" |

### Rendering Details

- **Markdown**: render assistant text with `react-markdown` + `remark-gfm`
- **Code blocks**: syntax highlight with `shiki` or `prism`
- **Tool use blocks**: collapsible card showing tool name and JSON input
- **Tool result blocks**: collapsible card showing stdout / stderr / exit code
- **Streaming**: text deltas append to a growing buffer; commit to final message on `message_stop`

---

## 11. Session Continuation

On `system / session_start`, the server writes `data.session_id` to `.qwen-code-web/last-session-id`.

On next launch with `--resume`:

```bash
qwen-code-web --project-dir ./my-app --resume
# reads .qwen-code-web/last-session-id
# passes --resume <session-id> to qwen
```

Qwen Code resumes its context. The browser replays `events.jsonl` from the beginning to restore the visible conversation.

---

## 12. Server API

```
POST /message
  Body: { text: string }
  → appends {"type":"submit","text":"..."} to input.jsonl
  → 200 { ok: true }

POST /approve
  Body: { requestId: string, allowed: boolean }
  → appends {"type":"confirmation_response",...} to input.jsonl
  → 200 { ok: true }

POST /stop
  → kills qwen process
  → 200 { ok: true }

GET /status
  → 200 { status: "running"|"stopped"|"starting", sessionId: string|null }

WS /events
  → streams all events from events.jsonl (replay from start, then live tail)
```

---

## 13. Tech Stack

| Layer | Choice | Reason |
|---|---|---|
| Runtime | Node.js + TypeScript | Natural fit for event I/O and PTY management |
| PTY | node-pty | Only reliable way to host an interactive TUI |
| HTTP server | Hono or Express | Lightweight; 4 routes |
| WebSocket | ws | Minimal, well-understood |
| Event tailer | Custom byte-offset reader | 50ms poll; simple and portable |
| Frontend | Vanilla TS or React | Single file; event → component map |
| Markdown | react-markdown + remark-gfm | Handles all assistant text |
| Syntax highlight | shiki | Good token-level highlighting |
| Persistence | Flat files in `.qwen-code-web/` | No DB needed for local tool |

---

## 14. Failure Modes

| Failure | Handling |
|---|---|
| Qwen process exits unexpectedly | Status bar → "stopped"; offer restart button |
| `events.jsonl` unreadable | Warn in UI; keep server alive |
| `input.jsonl` unwritable | Reject send; show error in UI |
| Browser disconnects | Server keeps session alive; reconnect replays events |
| Server restarts | Qwen process is killed; user must restart (v1 limitation) |

---

## 15. What's Out of Scope for v1

- Multi-user or shared sessions
- File tree / diff viewer
- Git integration
- Container isolation per session
- Approval policy engine
- Session replay UI
- Mobile layout

---

## 16. Phased Roadmap

### v1 — Local Tool (this doc)
- CLI entry point with `--project-dir` and `--resume`
- node-pty spawn + event tailer
- WebSocket event stream
- Chat UI with streaming renderer
- Tool approval cards
- Session file persistence

### v2 — Developer UX
- File tree panel
- Changed-files diff viewer
- Command risk labels on approval cards
- Session replay from `events.jsonl`
- Reconnect without losing history

### v3 — Isolation and Hosting
- Container-per-session runtime
- Workspace mount restrictions
- Multi-user auth
- Approval policy engine
- Audit dashboard
