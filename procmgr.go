package main

import (
	"bufio"
	"fmt"
	"os"
	"sync"
	"time"
)

// ── ActiveProject ─────────────────────────────────────────────────────────

// ActiveProject bundles all runtime state for a single running Qwen instance.
type ActiveProject struct {
	Project    Project
	State      *State
	Proc       *qwenProc
	Tailer     *Tailer
	Hub        *hub
	InputQueue chan inputJob
	SessionDir string
	EventsPath string
	InputPath  string
}

// ── ProcManager ───────────────────────────────────────────────────────────

// ProcManager manages multiple concurrent Qwen processes, one per project.
type ProcManager struct {
	mu     sync.RWMutex
	active map[string]*ActiveProject // keyed by project ID
}

// NewProcManager creates a new process manager.
func NewProcManager() *ProcManager {
	return &ProcManager{
		active: make(map[string]*ActiveProject),
	}
}

// Start spawns a Qwen process for the given project.
func (pm *ProcManager) Start(proj Project, resolvedArgs []string) (*ActiveProject, error) {
	pm.mu.Lock()
	if _, exists := pm.active[proj.ID]; exists {
		pm.mu.Unlock()
		return nil, fmt.Errorf("project %s is already running", proj.Name)
	}
	pm.mu.Unlock()

	// Set up session files
	sf, err := ensureSessionFiles(proj.Path)
	if err != nil {
		return nil, fmt.Errorf("session files: %w", err)
	}

	// Reset events and input files for fresh session
	if err := os.WriteFile(sf.eventsPath, nil, 0o600); err != nil {
		return nil, fmt.Errorf("init events file: %w", err)
	}
	if err := os.WriteFile(sf.inputPath, nil, 0o600); err != nil {
		return nil, fmt.Errorf("init input file: %w", err)
	}

	// Spawn qwen
	fmt.Fprintf(os.Stderr, "[procmgr] Starting Qwen for project %q in %s\n", proj.Name, proj.Path)
	proc, err := spawnQwen(spawnOptions{
		projectDir: proj.Path,
		eventsPath: sf.eventsPath,
		inputPath:  sf.inputPath,
		extraArgs:  resolvedArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn qwen: %w", err)
	}

	state := newState()
	state.setProcess(proc)

	ap := &ActiveProject{
		Project:    proj,
		State:      state,
		Proc:       proc,
		Tailer:     newTailer(sf.eventsPath),
		Hub:        newHub(),
		InputQueue: make(chan inputJob, 1024),
		SessionDir: sf.sessionDir,
		EventsPath: sf.eventsPath,
		InputPath:  sf.inputPath,
	}

	// Start input worker
	go pm.inputWorker(ap)

	// Stream stderr with [project-name] prefix
	go func() {
		scanner := bufio.NewScanner(proc.stderrPipe)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			fmt.Fprintf(os.Stderr, "[qwen/%s] %s\n", proj.Name, scanner.Text())
		}
	}()

	// Drain PTY master
	go func() {
		defer proc.ptyMaster.Close()
		buf := make([]byte, 4096)
		for {
			if _, err := proc.ptyMaster.Read(buf); err != nil {
				return
			}
		}
	}()

	// Monitor process lifecycle
	go func() {
		err := proc.cmd.Wait()
		state.setStopped()

		if err != nil {
			fmt.Fprintf(os.Stderr, "[procmgr] Qwen for %q exited with error: %v\n", proj.Name, err)
		} else {
			fmt.Fprintf(os.Stderr, "[procmgr] Qwen for %q exited successfully.\n", proj.Name)
		}

		// Broadcast status change to connected clients
		ap.Hub.broadcast(marshalProjectStatus(ap))
	}()

	// Start event tailer and broadcasting
	go func() {
		for raw := range ap.Tailer.Events {
			onProjectLiveEvent(ap, raw)
		}
	}()
	ap.Tailer.Start()

	pm.mu.Lock()
	pm.active[proj.ID] = ap
	pm.mu.Unlock()

	return ap, nil
}

// Stop gracefully stops the Qwen process for a project.
// It first tries /exit, then falls back to kill after timeout.
func (pm *ProcManager) Stop(projectID string) error {
	pm.mu.RLock()
	ap, ok := pm.active[projectID]
	pm.mu.RUnlock()
	if !ok {
		return fmt.Errorf("project %s is not running", projectID)
	}

	// Try graceful exit via /exit command
	fmt.Fprintf(os.Stderr, "[procmgr] Sending /exit to %q\n", ap.Project.Name)
	_ = appendInput(ap.InputPath, map[string]any{
		"type": "submit",
		"text": "/exit",
	})

	// Wait for graceful exit with timeout
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Wait for process state to become stopped
		for i := 0; i < 15; i++ { // 1.5 seconds
			status, _ := ap.State.get()
			if status == "stopped" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	<-done
	status, _ := ap.State.get()
	if status != "stopped" {
		fmt.Fprintf(os.Stderr, "[procmgr] Graceful exit timed out for %q, killing\n", ap.Project.Name)
		ap.State.kill()
	}

	// Clean up
	ap.Tailer.Stop()
	pm.mu.Lock()
	delete(pm.active, projectID)
	pm.mu.Unlock()

	// Broadcast final status
	ap.Hub.broadcast(marshalProjectStatus(ap))

	return nil
}

// GetActive returns the ActiveProject for a given project ID, or nil.
func (pm *ProcManager) GetActive(projectID string) *ActiveProject {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.active[projectID]
}

// ListActive returns all active project IDs.
func (pm *ProcManager) ListActive() map[string]*ActiveProject {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	out := make(map[string]*ActiveProject, len(pm.active))
	for k, v := range pm.active {
		out[k] = v
	}
	return out
}

// KillAll stops all running Qwen processes (used during shutdown).
func (pm *ProcManager) KillAll() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for id, ap := range pm.active {
		fmt.Fprintf(os.Stderr, "[procmgr] Killing Qwen for %q\n", ap.Project.Name)
		ap.State.kill()
		ap.Tailer.Stop()
		delete(pm.active, id)
	}
}

// ── Internal helpers ──────────────────────────────────────────────────────

func (pm *ProcManager) inputWorker(ap *ActiveProject) {
	for job := range ap.InputQueue {
		err := appendInput(ap.InputPath, job.data)
		job.respCh <- err
	}
}
