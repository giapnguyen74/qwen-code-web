# qwen-code-web

A browser UI for [Qwen Code](https://github.com/QwenLM/qwen-code) TUI sessions.

Run one command, open a browser tab, and operate Qwen Code from a clean chat interface — streaming output, tool approval cards, session history, and resume support — without touching the terminal again.

```
┌─────────────────────────────────────────────────┐
│  qwen-code-web  /  my-project  •  ● running     │
├─────────────────────────────────────────────────┤
│                                                 │
│  You                                            │
│    Fix the failing auth tests                   │
│                                                 │
│  Qwen                                           │
│    I'll start by reading the test output...     │
│    ▊                                            │
│                                                 │
│  ┌─ Tool request ──────────────────────────┐   │
│  │  run_shell_command                       │   │
│  │  npm test                                │   │
│  │                          [Allow] [Deny]  │   │
│  └──────────────────────────────────────────┘   │
│                                                 │
├─────────────────────────────────────────────────┤
│  Message…                               [Send]  │
└─────────────────────────────────────────────────┘
```

## How it works

`qwen-code-web` spawns Qwen Code in your project folder using its [Dual Output](https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/dual-output.md) mode (`--json-file` + `--input-file`). A local HTTP + WebSocket server tails the structured event stream and forwards it to the browser. The browser renders the conversation directly from events — no ANSI parsing, no terminal emulator.

The Qwen Code process and all session files stay on your machine. Nothing is sent anywhere else.

## Prerequisites

- **Go** ≥ 1.21 — [go.dev/dl](https://go.dev/dl/) or `brew install go`
- **Qwen Code** installed and available as `qwen` in your PATH
- **git**

No C compiler, no npm, no node-gyp. The Go binary is fully self-contained.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/YOUR_USER/qwen-code-web/main/install.sh | bash
```

This clones the repo to `~/.local/share/qwen-code-web`, runs `go build`, and puts a single `qwen-code-web` binary in `~/.local/bin`. Running the same command again upgrades to the latest version.

### Override defaults

```bash
QWEN_WEB_REPO=https://github.com/fork/qwen-code-web.git \
QWEN_WEB_DIR=/opt/qwen-code-web \
  curl -fsSL .../install.sh | bash
```

## Usage

```bash
cd ~/my-project

# Start a fresh session
qwen-code-web

# Resume the last session (restores conversation history in browser)
qwen-code-web --resume

# Pass any qwen flags straight through — they reach qwen unchanged
qwen-code-web -c          # continue qwen's own last conversation
qwen-code-web -y          # auto-approve all tool calls
qwen-code-web --resume -c -y

# Custom port (default: 3000)
qwen-code-web --port 4000

# Explicit project directory (overrides cwd)
qwen-code-web --project-dir ~/other-project
```

`qwen-code-web` claims `--project-dir`, `--port`, and `--resume`. Every other
flag is forwarded to `qwen` verbatim. Run `qwen --help` to see qwen's own flags.

The project folder is created (with `git init`) if it does not exist.

On launch the browser opens automatically. If it doesn't, visit `http://localhost:3000`.

## Session files

Session data lives in `~/.qwen-code-web/`, outside your project — nothing is written into your working tree.

```
~/.qwen-code-web/
└── sessions/
    └── my-project_a1b2c3d4/   ← project name + 8-char path hash
        ├── events.jsonl        ← Qwen Code writes structured events here
        ├── input.jsonl         ← server writes your messages and approvals here
        └── last-session-id     ← written on session_start, read by --resume
```

Each project directory gets its own slot, keyed by its absolute path. Renaming or moving a project starts a new slot (old history stays in the old slot).

## Development

```bash
git clone https://github.com/YOUR_USER/qwen-code-web.git
cd qwen-code-web
go run . --project-dir ~/your-project
```

To build a release binary:

```bash
go build -ldflags="-s -w" -o qwen-code-web .
```

Cross-compile for other platforms:

```bash
GOOS=linux  GOARCH=amd64 go build -ldflags="-s -w" -o qwen-code-web-linux-amd64 .
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o qwen-code-web-darwin-arm64 .
```

### Project layout

```
main.go        CLI entry point — flags, git init, spawn, server start
session.go     PTY spawn, session files, qwen/node binary resolution
tailer.go      Byte-offset JSONL file tailer (50 ms poll)
server.go      HTTP + WebSocket server, event replay, hub broadcast

public/
  index.html   Single-page browser UI — embedded into binary at build time

install.sh     curl | bash installer (clones repo, go build, PATH setup)
```

## Notes

**Input latency.** Qwen Code polls `input.jsonl` every 500 ms. After you hit Send there is up to half a second before Qwen sees the message. The UI shows a "sending…" indicator during this window.

**Stopping the session.** Click the **stop** button in the browser header, or press `Ctrl-C` in the terminal. Either way the Qwen Code process is killed cleanly.

## License

MIT
