import fs from 'node:fs';
import path from 'node:path';
import { execSync, spawn as cpSpawn } from 'node:child_process';
import * as pty from 'node-pty';
import type { SessionFiles, QwenProcess } from './types';

// ── Qwen binary resolution ────────────────────────────────────────────────

/**
 * Resolve the absolute path to the qwen script.
 * Tries `which qwen` first, then common nvm / npm-global locations.
 */
export function resolveQwenBinary(): string {
  try {
    const bin = execSync('which qwen', {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
    if (bin) return bin;
  } catch { /* not in PATH */ }

  const candidates: string[] = [];

  // nvm: ~/.nvm/versions/node/*/bin/qwen
  const nvmDir = process.env.NVM_DIR || path.join(process.env.HOME || '~', '.nvm');
  try {
    const versions = fs.readdirSync(path.join(nvmDir, 'versions', 'node'));
    for (const v of versions) {
      candidates.push(path.join(nvmDir, 'versions', 'node', v, 'bin', 'qwen'));
    }
  } catch { /* no nvm */ }

  // npm global prefix
  try {
    const prefix = execSync('npm config get prefix', {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
    if (prefix) candidates.push(path.join(prefix, 'bin', 'qwen'));
  } catch { /* ignore */ }

  candidates.push(
    '/usr/local/bin/qwen',
    '/usr/bin/qwen',
    path.join(process.env.HOME || '', '.local', 'bin', 'qwen'),
  );

  for (const c of candidates) {
    try { fs.accessSync(c, fs.constants.X_OK); return c; } catch { /* try next */ }
  }

  return 'qwen'; // last resort — let the OS complain clearly
}

// ── Session file management ───────────────────────────────────────────────

export async function ensureSessionFiles(projectDir: string): Promise<SessionFiles> {
  const sessionDir = path.join(projectDir, '.qwen-code-web');
  await fs.promises.mkdir(sessionDir, { recursive: true });
  await fs.promises.chmod(sessionDir, 0o700);

  const eventsPath = path.join(sessionDir, 'events.jsonl');
  const inputPath = path.join(sessionDir, 'input.jsonl');
  const lastSessionIdPath = path.join(sessionDir, 'last-session-id');

  for (const p of [eventsPath, inputPath]) {
    try { await fs.promises.access(p); }
    catch { await fs.promises.writeFile(p, '', { mode: 0o600 }); }
  }

  return { sessionDir, eventsPath, inputPath, lastSessionIdPath };
}

export async function loadLastSessionId(p: string): Promise<string | null> {
  try { return (await fs.promises.readFile(p, 'utf8')).trim() || null; }
  catch { return null; }
}

export async function saveSessionId(p: string, id: string): Promise<void> {
  await fs.promises.writeFile(p, id, { encoding: 'utf8', mode: 0o600 });
}

// ── Process spawn ─────────────────────────────────────────────────────────

export interface SpawnOptions {
  projectDir: string;
  eventsPath: string;
  inputPath: string;
  resumeSessionId?: string | null;
}

/**
 * Spawn qwen and return a unified QwenProcess handle.
 *
 * Strategy:
 *  1. Try node-pty  → gives qwen a hidden headless PTY (ideal).
 *  2. On failure    → fall back to child_process.spawn with stdio:inherit
 *     so qwen uses the current terminal as its TTY (works for local use).
 */
export function spawnQwen(opts: SpawnOptions): QwenProcess {
  const nodeBin = process.execPath;        // exact node binary already running
  const qwenBin = resolveQwenBinary();

  const args: string[] = [
    '--json-file', opts.eventsPath,
    '--input-file', opts.inputPath,
  ];
  if (opts.resumeSessionId) args.push('--resume', opts.resumeSessionId);

  console.log(`Using node:  ${nodeBin}`);
  console.log(`Using qwen:  ${qwenBin}`);

  // ── Attempt 1: node-pty (hidden PTY, browser-only interface) ─────────
  try {
    const child = pty.spawn(nodeBin, [qwenBin, ...args], {
      cwd: opts.projectDir,
      cols: 220,
      rows: 50,
      env: { ...process.env, TERM: 'xterm-256color' },
    });

    // Drain PTY stdout — output comes through events.jsonl, not raw bytes.
    child.onData(() => {});
    console.log('PTY mode: qwen running in headless PTY');

    return {
      kill: () => { try { child.kill(); } catch { /* already dead */ } },
      onExit: (cb) => { child.onExit(({ exitCode }) => cb(exitCode ?? undefined)); },
    };
  } catch (ptyErr) {
    console.warn(`[warn] node-pty unavailable (${(ptyErr as Error).message})`);
    console.warn('[warn] Falling back to terminal-inherit mode.');
    console.warn('[warn] qwen TUI will render in this terminal window.');
  }

  // ── Attempt 2: child_process.spawn, inherit current terminal ─────────
  // stdio:'inherit' passes the parent's TTY to qwen so it enters interactive
  // mode. The web interface still works via events.jsonl / input.jsonl.
  const child = cpSpawn(nodeBin, [qwenBin, ...args], {
    cwd: opts.projectDir,
    stdio: 'inherit',
    env: { ...process.env },
  });

  console.log('Terminal mode: qwen TUI sharing this terminal');

  const exitCallbacks: Array<(code: number | undefined) => void> = [];
  child.on('exit', (code) => {
    for (const cb of exitCallbacks) cb(code ?? undefined);
  });

  return {
    kill: () => { try { child.kill('SIGTERM'); } catch { /* already dead */ } },
    onExit: (cb) => { exitCallbacks.push(cb); },
  };
}

// ── Input writer ──────────────────────────────────────────────────────────

export async function appendInput(
  inputPath: string,
  command: Record<string, unknown>,
): Promise<void> {
  await fs.promises.appendFile(inputPath, JSON.stringify(command) + '\n', 'utf8');
}
