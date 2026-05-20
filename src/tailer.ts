import fs from 'node:fs';
import { EventEmitter } from 'node:events';

/**
 * Tails a JSONL file, emitting each parsed line as an 'event'.
 * Uses 50ms polling with byte-offset tracking to read only new data.
 */
export class JsonlTailer extends EventEmitter {
  private offset = 0;
  private lineBuffer = '';
  private timer: NodeJS.Timeout | null = null;
  private stopped = false;

  constructor(private readonly filePath: string) {
    super();
  }

  /** Read all events from the beginning of the file. One-shot, does not advance this.offset. */
  async readAll(): Promise<unknown[]> {
    const events: unknown[] = [];
    try {
      const content = await fs.promises.readFile(this.filePath, 'utf8');
      for (const line of content.split('\n')) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        try {
          events.push(JSON.parse(trimmed));
        } catch {
          // skip malformed lines
        }
      }
    } catch {
      // file may not exist yet — return empty
    }
    return events;
  }

  /** Start polling for new lines. Emits 'event' for each parsed JSON object. */
  start(): void {
    if (this.timer) return;
    // Initialize offset to current file size so we only tail NEW events
    fs.promises.stat(this.filePath)
      .then(s => { this.offset = s.size; })
      .catch(() => { this.offset = 0; })
      .finally(() => {
        this.timer = setInterval(() => { this.poll().catch(() => {}); }, 50);
      });
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  private async poll(): Promise<void> {
    if (this.stopped) return;
    try {
      const stat = await fs.promises.stat(this.filePath);

      // File was truncated/rotated
      if (stat.size < this.offset) {
        this.offset = 0;
        this.lineBuffer = '';
      }

      if (stat.size === this.offset) return;

      const stream = fs.createReadStream(this.filePath, {
        start: this.offset,
        end: stat.size - 1,
        encoding: 'utf8',
      });

      this.offset = stat.size;

      await new Promise<void>((resolve, reject) => {
        stream.on('data', (chunk: string | Buffer) => {
          this.lineBuffer += chunk.toString();
          let idx: number;
          while ((idx = this.lineBuffer.indexOf('\n')) >= 0) {
            const line = this.lineBuffer.slice(0, idx).trim();
            this.lineBuffer = this.lineBuffer.slice(idx + 1);
            if (!line) continue;
            try {
              this.emit('event', JSON.parse(line));
            } catch {
              // skip malformed lines
            }
          }
        });
        stream.on('end', resolve);
        stream.on('error', reject);
      });
    } catch {
      // file not readable yet — retry next tick
    }
  }
}
