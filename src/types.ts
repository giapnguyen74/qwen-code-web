// ── Qwen Code Dual Output event types ─────────────────────────────────────

export type ContentBlock =
  | { type: 'text'; text: string }
  | { type: 'tool_use'; id: string; name: string; input: Record<string, unknown> }
  | { type: 'tool_result'; tool_use_id: string; content: string | ContentBlock[] }
  | { type: string; [key: string]: unknown };

export interface QwenSystemEvent {
  type: 'system';
  subtype: 'session_start' | 'session_end' | string;
  data: {
    session_id: string;
    cwd?: string;
    protocol_version?: number;
    supported_events?: string[];
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export interface QwenStreamEvent {
  type: 'stream_event';
  event: {
    type: string;
    index?: number;
    delta?: {
      type: string;
      text?: string;
      partial_json?: string;
    };
    content_block?: {
      type: string;
      id?: string;
      name?: string;
    };
    message?: Record<string, unknown>;
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export interface QwenAssistantEvent {
  type: 'assistant';
  message: {
    role: 'assistant';
    content: ContentBlock[];
    usage?: {
      input_tokens: number;
      output_tokens: number;
    };
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export interface QwenUserEvent {
  type: 'user';
  message: {
    role: 'user';
    content: ContentBlock[];
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export interface QwenControlRequest {
  type: 'control_request';
  request_id: string;
  request: {
    subtype: 'can_use_tool' | string;
    tool_name: string;
    tool_use_id?: string;
    input: Record<string, unknown>;
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export interface QwenControlResponse {
  type: 'control_response';
  response: {
    subtype: 'success' | 'error' | string;
    request_id: string;
    response?: { allowed: boolean };
    error?: string;
    [key: string]: unknown;
  };
  [key: string]: unknown;
}

export type QwenEvent =
  | QwenSystemEvent
  | QwenStreamEvent
  | QwenAssistantEvent
  | QwenUserEvent
  | QwenControlRequest
  | QwenControlResponse
  | { type: string; [key: string]: unknown };

// ── WebSocket messages server → browser ───────────────────────────────────

export type ServerMessage =
  | { type: 'qwen_event'; data: QwenEvent }
  | { type: 'server_status'; status: SessionStatus; sessionId: string | null }
  | { type: 'replay_start' }
  | { type: 'replay_end' }
  | { type: 'error'; message: string };

// ── Session ────────────────────────────────────────────────────────────────

export type SessionStatus = 'starting' | 'running' | 'stopped';

export interface SessionFiles {
  sessionDir: string;
  eventsPath: string;
  inputPath: string;
  lastSessionIdPath: string;
}

/** Thin wrapper so the rest of the app doesn't care whether we used node-pty or child_process. */
export interface QwenProcess {
  kill(): void;
  onExit(cb: (code: number | undefined) => void): void;
}

export interface AppState {
  status: SessionStatus;
  sessionId: string | null;
  qwenProcess: QwenProcess | null;
}

export interface ServerConfig {
  port: number;
  projectDir: string;
  eventsPath: string;
  inputPath: string;
  lastSessionIdPath: string;
}
