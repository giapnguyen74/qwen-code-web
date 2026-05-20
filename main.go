package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// cliOpts holds our own flags; everything else is forwarded to qwen.
type cliOpts struct {
	projectDir string // defaults to cwd
	host       string
	port       int
	qwenArgs   []string // passed through verbatim to qwen
}

// parseArgs splits os.Args into our flags and qwen's flags.
// We claim: --project-dir, --port, --host (and their -short forms).
// Everything else (e.g. -c, -y, --model) is forwarded to qwen unchanged.
func parseArgs() cliOpts {
	opts := cliOpts{host: "0.0.0.0", port: 4000}
	args := os.Args[1:]

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// --flag=value forms
		if v, ok := cutPrefix(arg, "--port="); ok {
			opts.port, _ = strconv.Atoi(v)
			continue
		}
		if v, ok := cutPrefix(arg, "--project-dir="); ok {
			opts.projectDir = v
			continue
		}
		if v, ok := cutPrefix(arg, "--host="); ok {
			opts.host = v
			continue
		}

		switch arg {
		case "--port", "-port":
			if i+1 < len(args) {
				i++
				opts.port, _ = strconv.Atoi(args[i])
			}
		case "--project-dir", "-project-dir":
			if i+1 < len(args) {
				i++
				opts.projectDir = args[i]
			}
		case "--host", "-host":
			if i+1 < len(args) {
				i++
				opts.host = args[i]
			}
		case "--help", "-h", "-help":
			printHelp()
			os.Exit(0)
		default:
			opts.qwenArgs = append(opts.qwenArgs, arg)
		}
	}

	return opts
}

func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

func printHelp() {
	fmt.Print(`Usage: qwen-code-web [OUR FLAGS] -- [QWEN FLAGS...]

Our flags (consumed by qwen-code-web):
  --project-dir <path>   Project directory  (default: current directory)
  --port <n>             HTTP server port   (default: 4000)
  --host <addr>          Listen address     (default: 0.0.0.0 — all interfaces)

Everything behind -- is forwarded to qwen:
  -c, -y, --model, ...   See: qwen --help

Examples:
  cd ~/my-project && qwen-code-web
  qwen-code-web --project-dir ~/my-project
  qwen-code-web -- -c
  qwen-code-web --port 8080 --host 0.0.0.0 -- -y
`)
}

func main() {
	opts := parseArgs()

	// ── Resolve project directory ────────────────────────────────────────
	var err error
	if opts.projectDir == "" {
		opts.projectDir, err = os.Getwd()
		if err != nil {
			fatalf("getwd: %v", err)
		}
	}
	projectDir, err := filepath.Abs(opts.projectDir)
	if err != nil {
		fatalf("resolving project dir: %v", err)
	}

	if err := ensureProjectDir(projectDir); err != nil {
		fatalf("%v", err)
	}

	// ── Session files in ~/.qwen-code-web/sessions/<name>/ ───────────────
	sf, err := ensureSessionFiles(projectDir)
	if err != nil {
		fatalf("session files: %v", err)
	}

	os.WriteFile(sf.eventsPath, nil, 0o600) //nolint:errcheck
	os.WriteFile(sf.inputPath, nil, 0o600)  //nolint:errcheck

	// ── Print web UI address now — qwen will claim the terminal next ─────────
	displayHost := opts.host
	if displayHost == "0.0.0.0" {
		displayHost = "localhost"
	}
	url := fmt.Sprintf("http://%s:%d", displayHost, opts.port)
	fmt.Fprintf(os.Stderr, "\n  Web UI → %s\n\n", url)

	// ── Spawn qwen (claims stdin/stdout/stderr) ───────────────────────────
	fmt.Fprintf(os.Stderr, "Starting Qwen Code in: %s\n", projectDir)
	proc, err := spawnQwen(spawnOptions{
		projectDir: projectDir,
		eventsPath: sf.eventsPath,
		inputPath:  sf.inputPath,
		extraArgs:  opts.qwenArgs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nFailed to spawn qwen: %v\n", err)
		fmt.Fprintln(os.Stderr, `Make sure "qwen" is installed and available in PATH.`)
		os.Exit(1)
	}

	state := newState()
	state.setProcess(proc)

	// exitCh is closed when qwen exits (naturally or via signal).
	exitCh := make(chan struct{})
	go func() {
		proc.cmd.Wait() //nolint:errcheck
		state.setStopped()
		close(exitCh)
	}()

	// ── Server (runs silently in the background, no TTY needed) ──────────
	srv := newServer(serverConfig{
		host:       opts.host,
		port:       opts.port,
		projectDir: projectDir,
		eventsPath: sf.eventsPath,
		inputPath:  sf.inputPath,
	}, state)

	go func() {
		if err := srv.run(); err != nil {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			os.Exit(1)
		}
	}()

	// ── Graceful shutdown ─────────────────────────────────────────────────
	// Exit when qwen exits naturally OR when we receive SIGTERM/SIGINT.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-sig:
		state.kill()
	case <-exitCh:
		// qwen already exited — nothing more to do
	}
	os.Exit(0)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
