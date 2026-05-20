package main

import (
	"encoding/json"
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
	projectDir string   // defaults to cwd
	host       string   // defaults to "0.0.0.0"
	port       int      // defaults to 4000
	origins    []string // allowed WebSocket origins
	qwenArgs   []string // passed through verbatim to qwen
}

type settingsFile struct {
	Host     string   `json:"host"`
	Port     *int     `json:"port"`
	Origins  []string `json:"origins"`
	QwenArgs []string `json:"qwenArgs"`
}

func loadSettings() (settingsFile, error) {
	var s settingsFile
	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	path := filepath.Join(home, ".qwen-code-web", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // file does not exist, that's fine
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parsing %s: %w", path, err)
	}
	return s, nil
}

// parseArgs splits os.Args into our flags and qwen's flags.
// We claim: --project-dir, --port, --host, --origins (and their -short forms).
// Everything else (e.g. -c, -y, --model) is forwarded to qwen unchanged.
func parseArgs(base cliOpts) cliOpts {
	opts := base
	args := os.Args[1:]

	hasCliQwenArgs := false
	var cliQwenArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// --flag=value forms
		if v, ok := cutPrefix(arg, "--port="); ok {
			p, err := strconv.Atoi(v)
			if err != nil {
				fatalf("invalid port: %q", v)
			}
			opts.port = p
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
		if v, ok := cutPrefix(arg, "--origins="); ok {
			opts.origins = strings.Split(v, ",")
			continue
		}
		if v, ok := cutPrefix(arg, "--origin="); ok {
			opts.origins = strings.Split(v, ",")
			continue
		}

		switch arg {
		case "--port", "-port":
			if i+1 < len(args) {
				i++
				p, err := strconv.Atoi(args[i])
				if err != nil {
					fatalf("invalid port: %q", args[i])
				}
				opts.port = p
			} else {
				fatalf("missing value for --port")
			}
		case "--project-dir", "-project-dir":
			if i+1 < len(args) {
				i++
				opts.projectDir = args[i]
			} else {
				fatalf("missing value for --project-dir")
			}
		case "--host", "-host":
			if i+1 < len(args) {
				i++
				opts.host = args[i]
			} else {
				fatalf("missing value for --host")
			}
		case "--origins", "-origins", "--origin", "-origin":
			if i+1 < len(args) {
				i++
				opts.origins = strings.Split(args[i], ",")
			} else {
				fatalf("missing value for --origins")
			}
		case "--help", "-h", "-help":
			printHelp()
			os.Exit(0)
		default:
			hasCliQwenArgs = true
			cliQwenArgs = append(cliQwenArgs, arg)
		}
	}

	if hasCliQwenArgs {
		opts.qwenArgs = cliQwenArgs
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
  --origins <list>       WebSocket allowed origins (comma-separated list, e.g. "*")

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
	// 1. Start with hardcoded defaults
	baseOpts := cliOpts{
		host: "0.0.0.0",
		port: 4000,
	}

	// 2. Overwrite with ~/.qwen-code-web/settings.json if present
	settings, err := loadSettings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load settings.json: %v\n", err)
	} else {
		if settings.Host != "" {
			baseOpts.host = settings.Host
		}
		if settings.Port != nil {
			baseOpts.port = *settings.Port
		}
		if settings.Origins != nil {
			baseOpts.origins = settings.Origins
		}
		if settings.QwenArgs != nil {
			baseOpts.qwenArgs = settings.QwenArgs
		}
	}

	// 3. Overwrite with command-line arguments (highest priority)
	opts := parseArgs(baseOpts)

	// ── Resolve project directory ────────────────────────────────────────
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

	if err := os.WriteFile(sf.eventsPath, nil, 0o600); err != nil {
		fatalf("initialising events file: %v", err)
	}
	if err := os.WriteFile(sf.inputPath, nil, 0o600); err != nil {
		fatalf("initialising input file: %v", err)
	}

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

	// exitCh receives the wait error when qwen exits.
	exitCh := make(chan error, 1)
	go func() {
		err := proc.cmd.Wait()
		state.setStopped()
		exitCh <- err
	}()

	// ── Server (runs silently in the background, no TTY needed) ──────────
	srv := newServer(serverConfig{
		host:       opts.host,
		port:       opts.port,
		origins:    opts.origins,
		projectDir: projectDir,
		eventsPath: sf.eventsPath,
		inputPath:  sf.inputPath,
	}, state)

	go func() {
		if err := srv.run(); err != nil {
			fmt.Fprintf(os.Stderr, "server error: %v\n", err)
			state.kill()
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
	case err := <-exitCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nQwen process exited with error: %v\n", err)
			os.Exit(1)
		} else {
			fmt.Fprintln(os.Stderr, "\nQwen process exited successfully.")
		}
	}
	os.Exit(0)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
