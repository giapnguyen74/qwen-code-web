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
)

// ── Session files ─────────────────────────────────────────────────────────

type sessionFiles struct {
	sessionDir string
	eventsPath string
	inputPath  string
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

	for _, p := range []string{ep, ip} {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				return sessionFiles{}, err
			}
		}
	}

	return sessionFiles{
		sessionDir: sd,
		eventsPath: ep,
		inputPath:  ip,
	}, nil
}

// ── Process spawn ─────────────────────────────────────────────────────────

type spawnOptions struct {
	projectDir string
	eventsPath string
	inputPath  string
	extraArgs  []string // forwarded verbatim to qwen
}

type qwenProc struct {
	cmd *exec.Cmd
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
	if s.proc.cmd != nil && s.proc.cmd.Process != nil {
		s.proc.cmd.Process.Kill() //nolint:errcheck
	}
}

// resolveQwen finds the qwen binary path.
// It uses "which qwen" first, then exec.LookPath, and finally falls back to common paths.
func resolveQwen() (string, error) {
	// 1. Try running "which qwen" inside login shells (very robust on macOS/Linux)
	for _, shell := range []string{"zsh", "bash"} {
		cmd := exec.Command(shell, "-l", "-c", "which qwen")
		if out, err := cmd.CombinedOutput(); err == nil {
			path := strings.TrimSpace(string(out))
			if path != "" && isExec(path) {
				return path, nil
			}
		}
	}

	// 2. Try exec.LookPath("qwen")
	if path, err := exec.LookPath("qwen"); err == nil {
		return path, nil
	}

	// 3. Fallback to NVM or other common paths
	if home, err := os.UserHomeDir(); err == nil {
		nvmDir := os.Getenv("NVM_DIR")
		if nvmDir == "" {
			nvmDir = filepath.Join(home, ".nvm")
		}
		versionsDir := filepath.Join(nvmDir, "versions", "node")
		if entries, err := os.ReadDir(versionsDir); err == nil {
			for _, entry := range entries {
				candidate := filepath.Join(versionsDir, entry.Name(), "bin", "qwen")
				if isExec(candidate) {
					return candidate, nil
				}
			}
		}
		// common fixed locations
		for _, loc := range []string{
			"/opt/homebrew/bin/qwen",
			"/usr/local/bin/qwen",
			filepath.Join(home, ".local", "bin", "qwen"),
		} {
			if isExec(loc) {
				return loc, nil
			}
		}
	}

	return "", fmt.Errorf("qwen not found in PATH or standard locations")
}

func isExec(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func isNodeScript(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var buf [256]byte
	n, err := f.Read(buf[:])
	if err != nil && err != io.EOF {
		return false
	}
	content := string(buf[:n])
	if strings.HasPrefix(content, "#!") {
		firstLine := content
		if idx := strings.Index(content, "\n"); idx >= 0 {
			firstLine = content[:idx]
		}
		return strings.Contains(firstLine, "node")
	}
	return false
}

func resolveNode() (string, error) {
	// 1. Try running "which node" inside login shells (very robust on macOS/Linux)
	for _, shell := range []string{"zsh", "bash"} {
		cmd := exec.Command(shell, "-l", "-c", "which node")
		if out, err := cmd.CombinedOutput(); err == nil {
			path := strings.TrimSpace(string(out))
			if path != "" && isExec(path) {
				return path, nil
			}
		}
	}

	// 2. Try exec.LookPath("node")
	if path, err := exec.LookPath("node"); err == nil {
		return path, nil
	}

	// 3. Fallback to NVM or other common paths
	if home, err := os.UserHomeDir(); err == nil {
		nvmDir := os.Getenv("NVM_DIR")
		if nvmDir == "" {
			nvmDir = filepath.Join(home, ".nvm")
		}
		versionsDir := filepath.Join(nvmDir, "versions", "node")
		if entries, err := os.ReadDir(versionsDir); err == nil {
			for _, entry := range entries {
				candidate := filepath.Join(versionsDir, entry.Name(), "bin", "node")
				if isExec(candidate) {
					return candidate, nil
				}
			}
		}
	}

	return "", fmt.Errorf("node not found")
}

func spawnQwen(opts spawnOptions) (*qwenProc, error) {
	qwenBin, err := resolveQwen()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "Using qwen: %s\n", qwenBin)

	args := []string{
		"--json-file", opts.eventsPath,
		"--input-file", opts.inputPath,
	}
	args = append(args, opts.extraArgs...)

	var cmd *exec.Cmd
	if isNodeScript(qwenBin) {
		nodeBin, err := resolveNode()
		if err == nil {
			fmt.Fprintf(os.Stderr, "Detected Node.js script. Spawning via node: %s\n", nodeBin)
			nodeArgs := append([]string{qwenBin}, args...)
			cmd = exec.Command(nodeBin, nodeArgs...)
		} else {
			fmt.Fprintf(os.Stderr, "Detected Node.js script but node not found: %v. Spawning directly.\n", err)
			cmd = exec.Command(qwenBin, args...)
		}
	} else {
		cmd = exec.Command(qwenBin, args...)
	}

	cmd.Dir = opts.projectDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// Give qwen full ownership of the real terminal so its TUI renders natively.
	// Structured events are captured via --json-file; the web server reads that
	// file independently and does not touch the terminal at all.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cmd.Start: %w", err)
	}

	return &qwenProc{cmd: cmd}, nil
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
