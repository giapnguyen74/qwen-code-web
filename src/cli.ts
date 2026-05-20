#!/usr/bin/env node
import { Command } from 'commander';
import path from 'node:path';
import fs from 'node:fs';
import { exec, execSync } from 'node:child_process';
import { ensureSessionFiles, loadLastSessionId, spawnQwen } from './session';
import { startServer } from './server';
import type { AppState } from './types';

/**
 * Ensure a directory exists (mkdir -p) and has a git repo.
 * If the directory doesn't exist it is created.
 * If it exists but has no .git, `git init` is run inside it.
 */
async function ensureProjectDir(projectDir: string): Promise<void> {
  let exists = false;
  try {
    const stat = await fs.promises.stat(projectDir);
    if (!stat.isDirectory()) {
      console.error(`Error: path exists but is not a directory: ${projectDir}`);
      process.exit(1);
    }
    exists = true;
  } catch {
    // doesn't exist — create it
  }

  if (!exists) {
    await fs.promises.mkdir(projectDir, { recursive: true });
    console.log(`Created directory: ${projectDir}`);
  }

  // Check for .git anywhere up the tree (git rev-parse --git-dir)
  const gitDir = path.join(projectDir, '.git');
  let hasGit = false;
  try {
    await fs.promises.stat(gitDir);
    hasGit = true;
  } catch {
    // Try walking up — if a parent repo already covers this path, skip init
    try {
      execSync('git rev-parse --git-dir', { cwd: projectDir, stdio: 'ignore' });
      hasGit = true;
    } catch {
      hasGit = false;
    }
  }

  if (!hasGit) {
    try {
      execSync('git init', { cwd: projectDir, stdio: 'pipe' });
      console.log(`Initialised git repo in: ${projectDir}`);

      // Write a sensible .gitignore if one doesn't exist yet
      const gitignorePath = path.join(projectDir, '.gitignore');
      try {
        await fs.promises.access(gitignorePath);
      } catch {
        await fs.promises.writeFile(
          gitignorePath,
          'node_modules/\ndist/\n.qwen-code-web/\n*.log\n',
          'utf8',
        );
        console.log(`Wrote .gitignore`);
      }
    } catch (err) {
      console.warn(`Warning: could not run git init (${err}). Continuing without git.`);
    }
  }
}

const program = new Command();

program
  .name('qwen-code-web')
  .description('Browser UI for Qwen Code TUI sessions')
  .requiredOption('-d, --project-dir <path>', 'Project directory to run Qwen Code in')
  .option('-p, --port <number>', 'HTTP server port', '3000')
  .option('--resume', 'Resume the last session in this project', false)
  .parse(process.argv);

const opts = program.opts<{ projectDir: string; port: string; resume: boolean }>();

async function main(): Promise<void> {
  // ── Ensure project dir exists and has a git repo ────────────────────
  const projectDir = path.resolve(opts.projectDir);
  await ensureProjectDir(projectDir);

  const port = parseInt(opts.port, 10);
  if (isNaN(port) || port < 1 || port > 65535) {
    console.error('Error: invalid port number');
    process.exit(1);
  }

  // ── Session files ────────────────────────────────────────────────────
  const sessionFiles = await ensureSessionFiles(projectDir);

  let resumeSessionId: string | null = null;

  if (opts.resume) {
    resumeSessionId = await loadLastSessionId(sessionFiles.lastSessionIdPath);
    if (resumeSessionId) {
      console.log(`Resuming session: ${resumeSessionId}`);
    } else {
      console.log('No previous session found — starting fresh');
    }
    // On resume: keep events.jsonl (history), clear input.jsonl (stale commands)
    await fs.promises.writeFile(sessionFiles.inputPath, '', { mode: 0o600 });
  } else {
    // Fresh start: clear both files
    await fs.promises.writeFile(sessionFiles.eventsPath, '', { mode: 0o600 });
    await fs.promises.writeFile(sessionFiles.inputPath, '', { mode: 0o600 });
  }

  // ── App state ────────────────────────────────────────────────────────
  const state: AppState = {
    status: 'starting',
    sessionId: null,
    qwenProcess: null,
  };

  // ── Spawn qwen ───────────────────────────────────────────────────────
  console.log(`Starting Qwen Code in: ${projectDir}`);
  try {
    const qwenProcess = spawnQwen({
      projectDir,
      eventsPath: sessionFiles.eventsPath,
      inputPath: sessionFiles.inputPath,
      resumeSessionId,
    });

    state.qwenProcess = qwenProcess;

    qwenProcess.onExit((code) => {
      state.status = 'stopped';
      state.qwenProcess = null;
      console.log(`\nQwen Code exited (code=${code ?? '?'})`);
    });
  } catch (err) {
    console.error('\nFailed to spawn qwen:', err);
    console.error('Make sure "qwen" is installed and available in PATH.');
    process.exit(1);
  }

  // ── Start HTTP + WebSocket server ────────────────────────────────────
  startServer(
    {
      port,
      projectDir,
      eventsPath: sessionFiles.eventsPath,
      inputPath: sessionFiles.inputPath,
      lastSessionIdPath: sessionFiles.lastSessionIdPath,
    },
    state,
  );

  const url = `http://localhost:${port}`;
  console.log(`\n  qwen-code-web  →  ${url}\n`);

  // Open browser
  const openCmd =
    process.platform === 'darwin' ? 'open' :
    process.platform === 'win32'  ? 'start' :
    'xdg-open';
  exec(`${openCmd} ${url}`, (err) => {
    if (err) console.log(`  Could not open browser automatically. Please visit: ${url}`);
  });

  // ── Graceful shutdown ────────────────────────────────────────────────
  function shutdown(): void {
    console.log('\nShutting down...');
    if (state.qwenProcess) {
      try { state.qwenProcess.kill(); } catch {}
    }
    process.exit(0);
  }

  process.on('SIGTERM', shutdown);
  process.on('SIGINT', shutdown);
}

main().catch((err) => {
  console.error('Fatal:', err);
  process.exit(1);
});
