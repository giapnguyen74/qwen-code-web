package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/creack/pty"
)

// ── Session files ─────────────────────────────────────────────────────────

type sessionFiles struct {
	sessionDir        string
	eventsPath        string
	inputPath         string
	lastSessionIDPath string
}

func ensureProjectDir(dir string) error {
	created := false
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		fmt.Printf("Created directory: %s\n", dir)
		created = true
	}

	// Check if already inside a git repo (walk up the tree)
	if !created {
		out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-dir").
			CombinedOutput()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return nil // already in a git repo
		}
	}

	// Check for .git directly in this dir
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}

	// git init
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		fmt.Printf("Warning: git init failed (%v). Continuing without git.\n", string(out))
		return nil
	}
	fmt.Printf("Initialised git repo in: %s\n", dir)

	// Write .gitignore if absent
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		os.WriteFile(gi, []byte("node_modules/\ndist/\n.qwen-code-web/\n*.log\n"), 0o644) //nolint:errcheck
		fmt.Println("Wrote .gitignore")
	}
	return nil
}

// sessionDirForProject returns ~/.qwen-code-web/sessions/<basename>_<8hexchars>/
// keyed by the absolute project path so each project gets its own slot.
func sessionDirForProject(absProjectDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// Short readable name: last path component + 8-char hash for uniqueness
	base := filepath.Base(absProjectDir)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(absProjectDir)))[:8]
	name := fmt.Sprintf("%s_%s", base, hash)
	return filepath.Join(home, ".qwen-code-web", "sessions", name), nil
}

func ensureSessionFiles(projectDir string) (sessionFiles, error) {
	sd, err := sessionDirForProject(projectDir)
	if err != nil {
		return sessionFiles{}, err
	}
	if err := os.MkdirAll(sd, 0o700); err != nil {
		return sessionFiles{}, fmt.Errorf("mkdir session dir: %w", err)
	}

	ep := filepath.Join(sd, "events.jsonl")
	ip := filepath.Join(sd, "input.jsonl")
	lp := filepath.Join(sd, "last-session-id")

	for _, p := range []string{ep, ip} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				return sessionFiles{}, err
			}
		}
	}

	return sessionFiles{
		sessionDir:        sd,
		eventsPath:        ep,
		inputPath:         ip,
		lastSessionIDPath: lp,
	}, nil
}

func loadLastSessionID(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func saveLastSessionID(path, id string) {
	os.WriteFile(path, []byte(id), 0o600) //nolint:errcheck
}

// ── Process spawn ─────────────────────────────────────────────────────────

type spawnOptions struct {
	projectDir      string
	eventsPath      string
	inputPath       string
	resumeSessionID string
	extraArgs       []string // forwarded verbatim to qwen
}

type qwenProc struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

// State holds runtime session state, safe for concurrent access.
type State struct {
	mu        sync.RWMutex
	status    string // "starting" | "running" | "stopped"
	sessionID string
	proc      *qwenProc
}

func newState() *State { return &State{status: "starting"} }

func (s *State) setProcess(p *qwenProc) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.proc = p
}

func (s *State) setRunning(id string) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.status = "running"
	s.sessionID = id
}

func (s *State) setStopped() {
	s.mu.Lock(); defer s.mu.Unlock()
	s.status = "stopped"
}

func (s *State) get() (status, sessionID string) {
	s.mu.RLock(); defer s.mu.RUnlock()
	return s.status, s.sessionID
}

func (s *State) kill() {
	s.mu.Lock(); defer s.mu.Unlock()
	s.status = "stopped"
	if s.proc == nil {
		return
	}
	if s.proc.ptmx != nil {
		s.proc.ptmx.Close()
	}
	if s.proc.cmd != nil && s.proc.cmd.Process != nil {
		s.proc.cmd.Process.Kill() //nolint:errcheck
	}
}

// resolveQwen finds the qwen binary and the node interpreter.
// Returns (qwenPath, nodePath, error).
func resolveQwen() (string, string, error) {
	// ── Find qwen ─────────────────────────────────────────────────────
	qwenBin, err := exec.LookPath("qwen")
	if err != nil {
		// Check nvm locations
		if home, e := os.UserHomeDir(); e == nil {
			nvmDir := os.Getenv("NVM_DIR")
			if nvmDir == "" {
				nvmDir = filepath.Join(home, ".nvm")
			}
			versionsDir := filepath.Join(nvmDir, "versions", "node")
			if entries, e := os.ReadDir(versionsDir); e == nil {
				for _, entry := range entries {
					candidate := filepath.Join(versionsDir, entry.Name(), "bin", "qwen")
					if isExec(candidate) {
						qwenBin = candidate
						break
					}
				}
			}
			// common fixed locations
			if qwenBin == "" {
				for _, loc := range []string{
					"/usr/local/bin/qwen",
					filepath.Join(home, ".local", "bin", "qwen"),
				} {
					if isExec(loc) {
						qwenBin = loc
						break
					}
				}
			}
		}
	}
	if qwenBin == "" {
		return "", "", fmt.Errorf("qwen not found in PATH or nvm directories")
	}

	// ── Find node ─────────────────────────────────────────────────────
	// qwen is a Node.js script; we spawn it as "node qwen [args]"
	// to avoid execve shebang resolution issues on some systems.
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		// Scan nvm same as above
		if home, e := os.UserHomeDir(); e == nil {
			nvmDir := os.Getenv("NVM_DIR")
			if nvmDir == "" {
				nvmDir = filepath.Join(home, ".nvm")
			}
			versionsDir := filepath.Join(nvmDir, "versions", "node")
			if entries, e := os.ReadDir(versionsDir); e == nil {
				for _, entry := range entries {
					candidate := filepath.Join(versionsDir, entry.Name(), "bin", "node")
					if isExec(candidate) {
						nodeBin = candidate
						break
					}
				}
			}
		}
	}
	if nodeBin == "" {
		return "", "", fmt.Errorf("node not found in PATH")
	}

	return qwenBin, nodeBin, nil
}

func isExec(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func spawnQwen(opts spawnOptions) (*qwenProc, error) {
	qwenBin, nodeBin, err := resolveQwen()
	if err != nil {
		return nil, err
	}
	fmt.Printf("Using node: %s\n", nodeBin)
	fmt.Printf("Using qwen: %s\n", qwenBin)

	args := []string{
		qwenBin,
		"--json-file", opts.eventsPath,
		"--input-file", opts.inputPath,
	}
	if opts.resumeSessionID != "" {
		args = append(args, "--resume", opts.resumeSessionID)
	}
	// Append any extra args the user passed (e.g. -c, -y, --model)
	args = append(args, opts.extraArgs...)

	cmd := exec.Command(nodeBin, args...)
	cmd.Dir = opts.projectDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 220})
	if err != nil {
		return nil, fmt.Errorf("pty.Start: %w", err)
	}

	// Drain PTY stdout — structured output comes via events.jsonl.
	go io.Copy(io.Discard, ptmx) //nolint:errcheck

	return &qwenProc{cmd: cmd, ptmx: ptmx}, nil
}

// appendInput appends one JSONL command to the qwen input file.
func appendInput(inputPath string, v any) error {
	f, err := os.OpenFile(inputPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := fmt.Sprintf("%s\n", mustMarshal(v))
	_, err = fmt.Fprint(f, enc)
	return err
}
