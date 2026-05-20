package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

//go:embed public/index.html
var indexHTML []byte

// ── WebSocket hub ─────────────────────────────────────────────────────────

type hub struct {
	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
}

func newHub() *hub {
	return &hub{subscribers: make(map[chan []byte]struct{})}
}

func (h *hub) subscribe() chan []byte {
	ch := make(chan []byte, 512)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subscribers, ch)
	h.mu.Unlock()
}

func (h *hub) broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- msg:
		default: // slow client — skip rather than block
		}
	}
}

// ── Server ────────────────────────────────────────────────────────────────

type serverConfig struct {
	port       int
	projectDir string
	eventsPath string
	inputPath  string
}

type Server struct {
	cfg    serverConfig
	state  *State
	tailer *Tailer
	hub    *hub
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
}

func newServer(cfg serverConfig, state *State) *Server {
	return &Server{
		cfg:    cfg,
		state:  state,
		tailer: newTailer(cfg.eventsPath),
		hub:    newHub(),
	}
}

func (s *Server) run() error {
	// Process live events: update state + broadcast to WS clients
	go func() {
		for raw := range s.tailer.Events {
			s.onLiveEvent(raw)
		}
	}()
	s.tailer.Start()

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/message", s.handleMessage)
	mux.HandleFunc("/approve", s.handleApprove)
	mux.HandleFunc("/stop", s.handleStop)
	mux.HandleFunc("/events", s.handleWS)

	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.port)
	return http.ListenAndServe(addr, mux)
}

// onLiveEvent inspects a live event for session lifecycle changes,
// updates state, and broadcasts to all connected clients.
func (s *Server) onLiveEvent(raw json.RawMessage) {
	var base struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Data    struct {
			SessionID string `json:"session_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &base); err == nil {
		if base.Type == "system" {
			switch base.Subtype {
			case "session_start":
				if base.Data.SessionID != "" {
					s.state.setRunning(base.Data.SessionID)
					s.hub.broadcast(s.marshalStatus())
				}
			case "session_end":
				s.state.setStopped()
				s.hub.broadcast(s.marshalStatus())
			}
		}
	}
	s.hub.broadcast(s.marshalQwenEvent(raw))
}

// ── HTTP handlers ─────────────────────────────────────────────────────────

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML) //nolint:errcheck
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, sessionID := s.state.get()
	writeJSON(w, map[string]any{
		"status":     status,
		"sessionId":  sessionID,
		"projectDir": s.cfg.projectDir,
	})
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	status, _ := s.state.get()
	if status == "stopped" {
		writeError(w, http.StatusConflict, "session is stopped")
		return
	}
	if err := appendInput(s.cfg.inputPath, map[string]any{
		"type": "submit",
		"text": strings.TrimSpace(body.Text),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		Allowed   *bool  `json:"allowed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RequestID == "" || body.Allowed == nil {
		writeError(w, http.StatusBadRequest, "requestId and allowed are required")
		return
	}
	if err := appendInput(s.cfg.inputPath, map[string]any{
		"type":       "confirmation_response",
		"request_id": body.RequestID,
		"allowed":    *body.Allowed,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.state.kill()
	writeJSON(w, map[string]any{"ok": true})
}

// ── WebSocket handler ─────────────────────────────────────────────────────

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Subscribe to live events BEFORE replay to avoid a gap.
	liveCh := s.hub.subscribe()
	defer s.hub.unsubscribe(liveCh)

	write := func(msg []byte) bool {
		return conn.WriteMessage(websocket.TextMessage, msg) == nil
	}

	// 1. Current status
	if !write(s.marshalStatus()) {
		return
	}

	// 2. Replay full history from file
	if !write([]byte(`{"type":"replay_start"}`)) {
		return
	}
	for _, ev := range s.tailer.ReadAll() {
		if !write(s.marshalQwenEvent(ev)) {
			return
		}
	}
	// 3. Flush events buffered during replay (no gap)
	for {
		select {
		case ev := <-liveCh:
			if !write(ev) {
				return
			}
		default:
			goto replayDone
		}
	}
replayDone:
	if !write([]byte(`{"type":"replay_end"}`)) {
		return
	}

	// 4. Drain incoming WS frames (browser sends nothing, but we must read
	//    to detect close frames and avoid connection leaks).
	connClosed := make(chan struct{})
	go func() {
		defer close(connClosed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// 5. Stream live events until client disconnects
	for {
		select {
		case ev, ok := <-liveCh:
			if !ok {
				return
			}
			if !write(ev) {
				return
			}
		case <-connClosed:
			return
		}
	}
}

// ── Message helpers ───────────────────────────────────────────────────────

func (s *Server) marshalStatus() []byte {
	status, sessionID := s.state.get()
	return mustMarshalRaw(map[string]any{
		"type":      "server_status",
		"status":    status,
		"sessionId": sessionID,
	})
}

func (s *Server) marshalQwenEvent(raw json.RawMessage) []byte {
	return mustMarshalRaw(map[string]any{
		"type": "qwen_event",
		"data": raw,
	})
}

// ── JSON utilities ────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": msg}) //nolint:errcheck
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func mustMarshalRaw(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// satisfy unused import if needed
var _ = io.Discard
