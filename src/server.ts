import http from 'node:http';
import path from 'node:path';
import express from 'express';
import { WebSocketServer, WebSocket } from 'ws';
import { JsonlTailer } from './tailer';
import { appendInput, saveSessionId } from './session';
import type { AppState, ServerConfig, ServerMessage, QwenEvent } from './types';

function send(ws: WebSocket, msg: ServerMessage): void {
  if (ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify(msg));
  }
}

export function startServer(config: ServerConfig, state: AppState): http.Server {
  const app = express();
  app.use(express.json());

  // ── Static frontend ────────────────────────────────────────────────────
  const publicDir = path.join(__dirname, '..', 'public');
  app.use(express.static(publicDir));

  // ── API routes ─────────────────────────────────────────────────────────

  app.get('/status', (_req, res) => {
    res.json({
      status: state.status,
      sessionId: state.sessionId,
      projectDir: config.projectDir,
    });
  });

  app.post('/message', async (req, res) => {
    const { text } = req.body as { text?: string };
    if (!text?.trim()) {
      res.status(400).json({ error: 'text is required' });
      return;
    }
    if (state.status === 'stopped') {
      res.status(409).json({ error: 'session is stopped' });
      return;
    }
    try {
      await appendInput(config.inputPath, { type: 'submit', text: text.trim() });
      res.json({ ok: true });
    } catch (err) {
      res.status(500).json({ error: String(err) });
    }
  });

  app.post('/approve', async (req, res) => {
    const { requestId, allowed } = req.body as { requestId?: string; allowed?: boolean };
    if (!requestId || allowed === undefined) {
      res.status(400).json({ error: 'requestId and allowed are required' });
      return;
    }
    try {
      await appendInput(config.inputPath, {
        type: 'confirmation_response',
        request_id: requestId,
        allowed,
      });
      res.json({ ok: true });
    } catch (err) {
      res.status(500).json({ error: String(err) });
    }
  });

  app.post('/stop', (_req, res) => {
    if (state.qwenProcess) {
      try { state.qwenProcess.kill(); } catch {}
    }
    state.status = 'stopped';
    res.json({ ok: true });
  });

  // ── HTTP + WebSocket server ────────────────────────────────────────────

  const server = http.createServer(app);
  const wss = new WebSocketServer({ server, path: '/events' });

  // Live event tailer — tails events.jsonl starting from NOW (not replaying history)
  const tailer = new JsonlTailer(config.eventsPath);

  // Set of active WS clients receiving live events
  const liveClients = new Set<WebSocket>();

  tailer.on('event', (rawEvent: unknown) => {
    const ev = rawEvent as QwenEvent;

    // Intercept session lifecycle events
    if (ev.type === 'system') {
      const sysEv = ev as { type: 'system'; subtype: string; data?: { session_id?: string } };
      if (sysEv.subtype === 'session_start' && sysEv.data?.session_id) {
        state.sessionId = sysEv.data.session_id;
        state.status = 'running';
        saveSessionId(config.lastSessionIdPath, state.sessionId).catch(console.error);
        // Broadcast updated status
        for (const ws of liveClients) {
          send(ws, { type: 'server_status', status: 'running', sessionId: state.sessionId });
        }
      } else if (sysEv.subtype === 'session_end') {
        state.status = 'stopped';
        for (const ws of liveClients) {
          send(ws, { type: 'server_status', status: 'stopped', sessionId: state.sessionId });
        }
      }
    }

    // Broadcast to all live clients
    for (const ws of liveClients) {
      send(ws, { type: 'qwen_event', data: ev });
    }
  });

  tailer.start();

  // ── WebSocket connection handler ───────────────────────────────────────

  wss.on('connection', async (ws) => {
    // Buffer live events that arrive during replay to avoid any gap
    const liveBuffer: QwenEvent[] = [];
    let replayDone = false;

    const bufferingListener = (rawEvent: unknown) => {
      if (!replayDone) {
        liveBuffer.push(rawEvent as QwenEvent);
      }
    };
    tailer.on('event', bufferingListener);

    // Send current status first
    send(ws, { type: 'server_status', status: state.status, sessionId: state.sessionId });

    // Replay full history from events.jsonl
    send(ws, { type: 'replay_start' });
    const replayTailer = new JsonlTailer(config.eventsPath);
    const allEvents = await replayTailer.readAll();
    for (const ev of allEvents) {
      send(ws, { type: 'qwen_event', data: ev as QwenEvent });
    }

    // Flush buffered live events (arrived during replay)
    for (const ev of liveBuffer) {
      send(ws, { type: 'qwen_event', data: ev });
    }
    liveBuffer.length = 0;
    replayDone = true;

    send(ws, { type: 'replay_end' });

    // Remove buffering listener, add to live clients
    tailer.off('event', bufferingListener);
    liveClients.add(ws);

    ws.on('close', () => liveClients.delete(ws));
    ws.on('error', () => liveClients.delete(ws));
  });

  server.listen(config.port, '127.0.0.1', () => {
    // listening — caller prints the URL
  });

  return server;
}
