# qwen-code-web Design

**Status:** Draft 0.1  
**Project:** `qwen-code-web`  
**Purpose:** Browser-based alternative control channel for Qwen Code TUI sessions  
**Primary UX:** User selects a project folder, starts Qwen Code in that folder, and operates it from a webpage without direct shell access.

---

## 1. Summary

`qwen-code-web` is a web application that launches a Qwen Code TUI session on the server side inside a selected project folder, then exposes a controlled browser interface for user messages, assistant streaming output, tool approval, file/diff inspection, and session management.

The key architectural decision is:

> Qwen Code still runs as a real TUI in a server-side PTY, but the browser never receives raw shell access. The browser communicates only through structured channels.

Qwen Code Dual Output provides the needed transport:

- `stdout` remains the normal TUI rendering channel.
- `--json-file <path>` emits structured JSONL events from the running TUI.
- `--input-file <path>` accepts structured JSONL commands from the webapp.
- The browser receives events through WebSocket or Server-Sent Events.
- The browser sends user messages and approval decisions through backend HTTP/WebSocket APIs.

---

## 2. Reference

Qwen Code Dual Output documentation:

<https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/dual-output.md>

Important facts from the current design reference:

- Dual Output is a sidecar mode for the interactive TUI.
- The TUI continues rendering normally on `stdout`.
- Structured events can be emitted to `--json-file`.
- External programs can write JSONL commands to `--input-file`.
- Browser-based frontends are an intended integration model.
- With PTY hosting, `--json-file` is preferred over `--json-fd` because PTY wrappers generally cannot pass extra file descriptors.
- Input commands include:
  - `submit`
  - `confirmation_response`
- Output events include:
  - `system/session_start`
  - `stream_event`
  - `user`
  - `assistant`
  - `control_request`
  - `control_response`

---

## 3. Product Goals

### 3.1 Goals

1. Let users operate Qwen Code from a webpage.
2. Let users select an allowed project folder and launch Qwen Code there.
3. Stream Qwen Code output to the browser in near real time.
4. Send user prompts from the browser to the running Qwen Code session.
5. Render tool approval prompts in the browser.
6. Allow users to approve or deny tool requests without touching the terminal.
7. Preserve session history as structured JSONL.
8. Avoid ANSI parsing by using Qwen Code Dual Output events.
9. Prevent direct shell access from the browser.
10. Support multi-session operation eventually.

### 3.2 Non-goals for MVP

1. Full browser terminal emulation.
2. Direct PTY keystroke forwarding from browser.
3. Arbitrary host filesystem browsing.
4. Multi-agent orchestration.
5. Full IDE replacement.
6. Remote collaborative editing.
7. Automated pull request creation.
8. Fine-grained enterprise policy engine.

---

## 4. Core Architecture

```txt
Browser UI
  ├─ Workspace selector
  ├─ Chat input
  ├─ Streaming assistant output
  ├─ Tool approval cards
  ├─ Session status
  ├─ File tree
  └─ Diff viewer

        HTTPS / WebSocket

Backend API
  ├─ Auth/session middleware
  ├─ Workspace resolver
  ├─ Session manager
  ├─ PTY launcher
  ├─ JSONL event tailer
  ├─ JSONL input writer
  ├─ Approval controller
  ├─ Audit logger
  └─ Cleanup worker

        server-side only

Qwen Code runtime
  ├─ qwen process
  ├─ server-side PTY
  ├─ cwd = selected workspace
  ├─ --json-file events.jsonl
  └─ --input-file input.jsonl
```

---

## 5. Runtime Model

Each active Qwen Code session has:

```txt
Session
├─ session_id
├─ user_id
├─ workspace_id
├─ workspace_path
├─ runtime_dir
├─ events_jsonl_path
├─ input_jsonl_path
├─ pty_process
├─ websocket_clients[]
├─ status
├─ created_at
├─ last_activity_at
└─ exit_info
```

Example runtime directory:

```txt
$XDG_RUNTIME_DIR/qwen-code-web/sess_abc123/
├─ events.jsonl
├─ input.jsonl
├─ pty.log            optional, restricted
└─ metadata.json
```

Recommended permissions:

```txt
runtime_dir: 0700
input.jsonl: 0600
events.jsonl: 0600
```

---

## 6. Process Launch

The backend must not launch Qwen Code with a shell command like this:

```bash
cd /some/folder && qwen --json-file ... --input-file ...
```

Instead, it should spawn the process with `cwd` set directly:

```ts
import os from "node:os";
import path from "node:path";
import fs from "node:fs/promises";
import pty from "node-pty";

export async function startQwenSession(params: {
  sessionId: string;
  workspacePath: string;
}) {
  const runtimeDir = await fs.mkdtemp(
    path.join(os.tmpdir(), `qwen-code-web-${params.sessionId}-`)
  );

  await fs.chmod(runtimeDir, 0o700);

  const eventsPath = path.join(runtimeDir, "events.jsonl");
  const inputPath = path.join(runtimeDir, "input.jsonl");

  await fs.writeFile(eventsPath, "", { mode: 0o600 });
  await fs.writeFile(inputPath, "", { mode: 0o600 });

  const child = pty.spawn(
    "qwen",
    [
      "--json-file",
      eventsPath,
      "--input-file",
      inputPath
    ],
    {
      cwd: params.workspacePath,
      cols: 120,
      rows: 40,
      env: {
        ...process.env,
        TERM: "xterm-256color"
      }
    }
  );

  return {
    child,
    runtimeDir,
    eventsPath,
    inputPath
  };
}
```

Rationale:

- `cwd` gives the desired “cd into folder and launch Qwen Code there” behavior.
- No shell interpolation is required.
- No arbitrary command string is built.
- The PTY is hidden from the browser.
- The browser communicates through structured application-level APIs.

---

## 7. Workspace Model

The browser must not send raw filesystem paths.

Bad API:

```json
{
  "path": "/home/user/project"
}
```

Good API:

```json
{
  "workspaceId": "repo_123"
}
```

The backend resolves `workspaceId` to an allowlisted path:

```txt
repo_123 → /srv/qwen-workspaces/user_42/my-project
```

### 7.1 Workspace Record

```ts
export interface WorkspaceRecord {
  id: string;
  userId: string;
  name: string;
  absolutePath: string;
  displayPath: string;
  createdAt: string;
  updatedAt: string;
}
```

### 7.2 Path Validation

```ts
import path from "node:path";
import fs from "node:fs/promises";

const WORKSPACE_ROOT = "/srv/qwen-workspaces";

export async function resolveAllowedWorkspace(params: {
  userId: string;
  workspaceId: string;
}) {
  const record = await db.workspace.findFirst({
    where: {
      id: params.workspaceId,
      userId: params.userId
    }
  });

  if (!record) {
    throw new Error("Workspace not found");
  }

  const realRoot = await fs.realpath(WORKSPACE_ROOT);
  const realWorkspace = await fs.realpath(record.absolutePath);

  if (!realWorkspace.startsWith(realRoot + path.sep)) {
    throw new Error("Workspace escapes allowed root");
  }

  return realWorkspace;
}
```

---

## 8. Communication Channels

### 8.1 Channels Overview

```txt
Browser → Backend
  - HTTP: create session, stop session, submit message, approve tool
  - WebSocket: optional bidirectional control

Backend → Browser
  - WebSocket or SSE: qwen events, status, errors

Backend → Qwen Code
  - input.jsonl: submit and confirmation_response
  - PTY resize/control: server-side only

Qwen Code → Backend
  - events.jsonl: structured JSONL event stream
  - PTY stdout/stderr: internal, optional logging only
```

### 8.2 Why not expose PTY directly?

Exposing the PTY would turn the webapp into a browser shell. That violates the product requirement.

The browser should not be able to call:

```ts
pty.write(userControlledInput);
```

The browser should only be able to submit structured actions:

```json
{"type":"submit","text":"Explain this repo"}
```

or:

```json
{"type":"confirmation_response","request_id":"req_123","allowed":true}
```

---

## 9. Input Command Flow

### 9.1 User Message

Browser sends:

```json
{
  "type": "message.submit",
  "sessionId": "sess_abc123",
  "text": "Explain this repository and find the main entry point."
}
```

Backend writes to `input.jsonl`:

```json
{"type":"submit","text":"Explain this repository and find the main entry point."}
```

Helper:

```ts
import fs from "node:fs/promises";

export async function appendQwenInput(inputPath: string, value: unknown) {
  await fs.appendFile(inputPath, JSON.stringify(value) + "\n", "utf8");
}

export async function submitUserMessage(inputPath: string, text: string) {
  await appendQwenInput(inputPath, {
    type: "submit",
    text
  });
}
```

### 9.2 Tool Approval Response

Browser sends:

```json
{
  "type": "tool.approval",
  "sessionId": "sess_abc123",
  "requestId": "req_123",
  "allowed": true
}
```

Backend writes:

```json
{"type":"confirmation_response","request_id":"req_123","allowed":true}
```

Helper:

```ts
export async function respondToToolRequest(params: {
  inputPath: string;
  requestId: string;
  allowed: boolean;
}) {
  await appendQwenInput(params.inputPath, {
    type: "confirmation_response",
    request_id: params.requestId,
    allowed: params.allowed
  });
}
```

---

## 10. Output Event Flow

Qwen Code emits JSONL events to `events.jsonl`.

The backend tails the file and forwards parsed events to connected browser clients.

```ts
import fs from "node:fs";

export function tailJsonlFile(params: {
  path: string;
  onEvent: (event: unknown) => void;
  onError?: (error: Error) => void;
}) {
  let offset = 0;
  let buffer = "";
  let stopped = false;

  async function readNewBytes() {
    if (stopped) return;

    try {
      const stat = await fs.promises.stat(params.path);

      if (stat.size < offset) {
        offset = 0;
        buffer = "";
      }

      if (stat.size === offset) return;

      const stream = fs.createReadStream(params.path, {
        start: offset,
        end: stat.size - 1,
        encoding: "utf8"
      });

      offset = stat.size;

      stream.on("data", chunk => {
        buffer += chunk;

        let newlineIndex: number;
        while ((newlineIndex = buffer.indexOf("\n")) >= 0) {
          const line = buffer.slice(0, newlineIndex).trim();
          buffer = buffer.slice(newlineIndex + 1);

          if (!line) continue;

          try {
            params.onEvent(JSON.parse(line));
          } catch (error) {
            params.onError?.(error as Error);
          }
        }
      });
    } catch (error) {
      params.onError?.(error as Error);
    }
  }

  const timer = setInterval(readNewBytes, 50);

  return () => {
    stopped = true;
    clearInterval(timer);
  };
}
```

---

## 11. Event Normalization

The backend may forward raw Qwen events directly, but it is better to normalize them into product-level events.

### 11.1 Raw Event Forwarding

```json
{
  "type": "qwen.raw_event",
  "sessionId": "sess_abc123",
  "event": {
    "type": "stream_event",
    "event": {
      "type": "content_block_delta",
      "delta": {
        "type": "text_delta",
        "text": "Hello"
      }
    }
  }
}
```

### 11.2 Product-Level Event

```json
{
  "type": "assistant.delta",
  "sessionId": "sess_abc123",
  "text": "Hello"
}
```

Recommended approach:

- Store raw events permanently.
- Send normalized events to the UI.
- Keep an option to inspect raw events in development mode.

---

## 12. Browser UI Design

### 12.1 Layout

```txt
┌────────────────────────────────────────────────────────────┐
│ qwen-code-web                                              │
├───────────────────────┬────────────────────────────────────┤
│ Workspace             │ Conversation                       │
│                       │                                    │
│ Project: my-app       │ User                               │
│ Status: running       │   Fix the failing tests            │
│ CWD: /workspace       │                                    │
│                       │ Qwen                               │
│ Files                 │   I will inspect the test suite... │
│ ├─ src/               │                                    │
│ ├─ package.json       │ Tool request                       │
│ └─ tests/             │   npm test                         │
│                       │   [Allow once] [Deny]              │
│ Changed files         │                                    │
│ ├─ src/auth.ts        │                                    │
│ └─ tests/auth.test.ts │                                    │
├───────────────────────┴────────────────────────────────────┤
│ Message input...                                    [Send] │
└────────────────────────────────────────────────────────────┘
```

### 12.2 Main Panels

1. Workspace panel
   - Selected workspace
   - Session status
   - Current working directory
   - Start/stop/restart controls

2. Conversation panel
   - User messages
   - Streaming assistant messages
   - Tool calls
   - Tool results
   - Approval prompts

3. File panel
   - Project tree
   - Changed files
   - Diffs
   - Open file preview

4. Session panel
   - Token usage
   - Duration
   - Process status
   - Event log
   - Audit trail

---

## 13. Tool Approval UX

When Qwen Code emits a `control_request`, the browser should render an approval card.

Example request:

```json
{
  "type": "control_request",
  "request_id": "req_123",
  "request": {
    "subtype": "can_use_tool",
    "tool_name": "run_shell_command",
    "input": {
      "command": "npm test"
    }
  }
}
```

Browser card:

```txt
Qwen wants to run a shell command:

npm test

Workspace: my-app
Risk: Low

[Allow once] [Deny]
```

Approval response:

```json
{"type":"confirmation_response","request_id":"req_123","allowed":true}
```

Deny response:

```json
{"type":"confirmation_response","request_id":"req_123","allowed":false}
```

### 13.1 Approval Policy

The backend should apply policy before sending approval to Qwen.

Example policy levels:

```txt
Level 0: User must approve every tool call.
Level 1: Auto-approve read-only commands.
Level 2: Auto-approve test/build/lint commands.
Level 3: Auto-approve allowlisted write operations.
Level 4: Fully trusted workspace session.
```

MVP should use Level 0 or Level 1.

### 13.2 Dangerous Command Detection

Commands that should require explicit approval or be denied by policy:

```txt
rm -rf /
rm -rf ~
chmod -R 777
curl ... | sh
wget ... | sh
sudo ...
docker run --privileged
ssh ...
scp ...
mkfs
mount
umount
```

Important: command detection is a guardrail, not a sandbox. Real isolation must happen at the OS/container level.

---

## 14. Session State Machine

```txt
CREATED
  ↓
STARTING
  ↓
RUNNING_IDLE
  ↓
RUNNING_BUSY
  ↓
AWAITING_APPROVAL
  ↓
RUNNING_BUSY
  ↓
RUNNING_IDLE
  ↓
STOPPING
  ↓
STOPPED
```

Error states:

```txt
START_FAILED
PROCESS_EXITED
EVENT_CHANNEL_FAILED
INPUT_CHANNEL_FAILED
WORKSPACE_INVALID
PERMISSION_DENIED
TIMEOUT
```

### 14.1 State Mapping

```txt
system/session_start       → RUNNING_IDLE
stream_event/message_start → RUNNING_BUSY
stream_event/message_stop  → RUNNING_IDLE
control_request            → AWAITING_APPROVAL
control_response           → RUNNING_BUSY or RUNNING_IDLE
process exit               → STOPPED
backend kill               → STOPPED
```

---

## 15. Backend API

### 15.1 Workspaces

```txt
GET /api/workspaces
```

Response:

```json
{
  "workspaces": [
    {
      "id": "repo_123",
      "name": "my-app",
      "displayPath": "~/projects/my-app"
    }
  ]
}
```

### 15.2 Create Session

```txt
POST /api/sessions
```

Request:

```json
{
  "workspaceId": "repo_123"
}
```

Response:

```json
{
  "sessionId": "sess_abc123",
  "workspaceId": "repo_123",
  "status": "starting"
}
```

### 15.3 Get Session

```txt
GET /api/sessions/:sessionId
```

Response:

```json
{
  "sessionId": "sess_abc123",
  "workspaceId": "repo_123",
  "status": "running_idle",
  "createdAt": "2026-05-20T10:00:00.000Z",
  "lastActivityAt": "2026-05-20T10:03:00.000Z"
}
```

### 15.4 Stop Session

```txt
DELETE /api/sessions/:sessionId
```

Response:

```json
{
  "ok": true
}
```

### 15.5 Submit Message

```txt
POST /api/sessions/:sessionId/messages
```

Request:

```json
{
  "text": "Find the main entry point and explain how the app starts."
}
```

Response:

```json
{
  "ok": true
}
```

### 15.6 Tool Approval

```txt
POST /api/sessions/:sessionId/approvals
```

Request:

```json
{
  "requestId": "req_123",
  "allowed": true
}
```

Response:

```json
{
  "ok": true
}
```

### 15.7 Event Stream

```txt
GET /api/sessions/:sessionId/events
```

Options:

- WebSocket preferred for bidirectional future use.
- SSE acceptable for output-only stream.

WebSocket event example:

```json
{
  "type": "assistant.delta",
  "sessionId": "sess_abc123",
  "text": "I found the main entry point in "
}
```

---

## 16. Frontend Event Handling

```ts
type UiEvent =
  | { type: "session.status"; status: string }
  | { type: "assistant.delta"; text: string }
  | { type: "assistant.message"; message: unknown }
  | { type: "user.message"; message: unknown }
  | { type: "tool.approval_requested"; request: ToolApprovalRequest }
  | { type: "tool.approval_resolved"; requestId: string; allowed: boolean }
  | { type: "session.error"; message: string };

function handleUiEvent(event: UiEvent) {
  switch (event.type) {
    case "session.status":
      setStatus(event.status);
      break;

    case "assistant.delta":
      appendAssistantDelta(event.text);
      break;

    case "assistant.message":
      commitAssistantMessage(event.message);
      break;

    case "user.message":
      commitUserMessage(event.message);
      break;

    case "tool.approval_requested":
      showApprovalCard(event.request);
      break;

    case "tool.approval_resolved":
      closeApprovalCard(event.requestId);
      break;

    case "session.error":
      showError(event.message);
      break;
  }
}
```

---

## 17. Persistence

### 17.1 Store Raw Events

Every line from `events.jsonl` should be copied into durable storage.

Benefits:

- Replay sessions.
- Build audit logs.
- Debug UI rendering.
- Compute usage metrics.
- Reconstruct final transcript.

### 17.2 Suggested Tables

```sql
CREATE TABLE qwen_sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  status TEXT NOT NULL,
  runtime_dir TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  stopped_at TEXT,
  exit_code INTEGER,
  exit_signal TEXT
);

CREATE TABLE qwen_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  raw_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE qwen_tool_approvals (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  input_json TEXT NOT NULL,
  decision TEXT,
  decided_by TEXT,
  decided_at TEXT,
  created_at TEXT NOT NULL
);
```

---

## 18. Security Model

### 18.1 Threat Model

The browser user must not be able to:

1. Get an interactive shell.
2. Write arbitrary bytes to the PTY.
3. Launch Qwen Code in arbitrary host paths.
4. Escape the selected workspace.
5. Access other users' runtime files.
6. Approve tool requests for another user's session.
7. Read raw event logs for another user's session.
8. Mount host secrets accidentally.
9. Use Qwen Code to damage the host system.

### 18.2 Required Controls

1. Authenticate every request.
2. Authorize access to each `workspaceId` and `sessionId`.
3. Resolve workspaces server-side.
4. Validate real paths with `fs.realpath`.
5. Use per-session runtime directories with mode `0700`.
6. Never expose `input.jsonl` or `events.jsonl` paths to the browser.
7. Never forward raw browser input to the PTY.
8. Run Qwen as a non-root user.
9. Prefer container isolation per session.
10. Apply resource limits.
11. Audit every tool request and approval decision.
12. Kill idle sessions after a timeout.

### 18.3 Recommended Container Runtime

```txt
Container per session
├─ user: non-root
├─ workdir: /workspace
├─ mount: selected workspace → /workspace
├─ runtime dir: /run/qwen-code-web/session
├─ network: disabled or restricted by policy
├─ memory limit
├─ CPU limit
├─ process limit
└─ timeout
```

Example Docker launch concept:

```bash
docker run --rm \
  --user 1000:1000 \
  --workdir /workspace \
  --mount type=bind,src=/srv/qwen-workspaces/user_42/my-app,dst=/workspace \
  --mount type=bind,src=/run/qwen-code-web/sess_abc,dst=/run/qwen-session \
  --memory 2g \
  --cpus 2 \
  qwen-code-runtime \
  qwen --json-file /run/qwen-session/events.jsonl \
       --input-file /run/qwen-session/input.jsonl
```

---

## 19. Failure Modes

### 19.1 Qwen Process Exits

Backend should:

1. Mark session as stopped.
2. Send `session.status` to browser.
3. Store exit code/signal.
4. Preserve event log.
5. Allow user to start a new session.

### 19.2 Event File Cannot Be Read

Backend should:

1. Mark event channel degraded.
2. Notify browser.
3. Keep process alive if possible.
4. Offer restart.

### 19.3 Input File Cannot Be Written

Backend should:

1. Reject new messages.
2. Notify browser.
3. Mark session error.
4. Offer restart.

### 19.4 Browser Disconnects

Backend should:

1. Keep Qwen session alive for a grace period.
2. Continue storing events.
3. Allow browser reconnect.
4. Kill session after idle timeout.

### 19.5 Backend Restarts

MVP may kill all child sessions on backend restart.

Future version should:

1. Store session metadata durably.
2. Reattach to event logs where possible.
3. Mark orphaned PTYs as lost or stopped.
4. Clean up orphaned runtime directories.

---

## 20. MVP Scope

### 20.1 MVP Features

```txt
MVP
├─ Workspace list
├─ Start Qwen session in selected workspace
├─ Stop session
├─ WebSocket event stream
├─ JSONL event tailer
├─ Browser chat input
├─ input.jsonl submit writer
├─ Streaming assistant output
├─ Tool approval card
├─ confirmation_response writer
├─ Session status indicator
├─ Raw event log storage
└─ Basic auth/session ownership checks
```

### 20.2 MVP Exclusions

```txt
Excluded from MVP
├─ Full terminal view
├─ Raw PTY input
├─ Collaborative sessions
├─ File editing UI
├─ Git commit/PR automation
├─ Advanced command policy
├─ Multi-agent orchestration
├─ Enterprise audit dashboard
└─ Fine-grained RBAC
```

---

## 21. Roadmap

### Phase 0: Spike

- Launch Qwen Code from Node with `node-pty`.
- Pass `--json-file` and `--input-file`.
- Tail event JSONL.
- Append a `submit` command.
- Confirm streaming response appears.
- Confirm tool approval flow works.

### Phase 1: MVP

- Add authenticated workspace selector.
- Add session manager.
- Add WebSocket event forwarding.
- Add chat UI.
- Add approval UI.
- Add session stop/cleanup.
- Add event persistence.

### Phase 2: Developer UX

- Add file tree.
- Add changed-files panel.
- Add git diff viewer.
- Add command risk labels.
- Add session replay.
- Add reconnect support.

### Phase 3: Isolation and Policy

- Container-per-session runtime.
- Workspace mount restrictions.
- Network policy.
- Resource limits.
- Approval policy engine.
- Audit dashboard.

### Phase 4: Team Features

- Shared read-only session links.
- Multi-user approval roles.
- Pull request integration.
- Organization-level policy.
- Centralized session archive.

---

## 22. Open Questions

1. Should sessions run directly on the host in development, but in containers in production?
2. Should the browser receive raw Qwen events, normalized events, or both?
3. Should Qwen sessions survive backend restarts?
4. Should users be allowed to see an optional terminal mirror?
5. What commands are safe enough to auto-approve?
6. Should network access be disabled by default?
7. Should workspaces be copied into temporary sandboxes or mounted directly?
8. Should file changes be committed automatically or only shown as diffs?
9. How should long-running commands be interrupted?
10. Should session logs include sensitive file contents, or should logs be redacted?

---

## 23. Recommended Default Decisions

For the first production-oriented version:

```txt
Use node-pty: yes
Expose raw terminal to browser: no
Use --json-file: yes
Use --input-file: yes
Use --json-fd: no, because PTY hosting is required
Allow arbitrary paths: no
Use workspace IDs: yes
Run as root: no
Use container isolation: yes
Auto-approve shell commands: no
Store raw events: yes
Support reconnect: yes
Support session replay: later
```

---

## 24. Final Design Principle

The product boundary is:

```txt
Qwen Code TUI remains real.
PTY remains server-side.
JSONL becomes the integration protocol.
The browser becomes a controlled Qwen Code control surface.
The user never receives direct shell access.
```

That is the core of `qwen-code-web`.
