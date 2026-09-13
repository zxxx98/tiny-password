/**
 * Latest-wins guard for async sequences (list loads, search, generator
 * requests). Every new call invalidates all older ones: their responses are
 * still received but the caller's `isCurrent(id)` check rejects them, so a
 * late response can never overwrite newer state. Callers additionally abort
 * the HTTP request itself; this guard is the second line of defense.
 */
export class LatestTracker {
  private counter = 0;

  next(): number {
    this.counter += 1;
    return this.counter;
  }

  isCurrent(id: number): boolean {
    return this.counter === id;
  }
}

/**
 * Debounced "latest request wins" runner used by the password generator.
 *
 * Starting a new run aborts the in-flight one, and invalidate() aborts the
 * current run plus any pending debounced schedule — so a producer checking
 * `signal.aborted` after its await can never apply a superseded response.
 */
export class DebouncedRunner {
  private timer: ReturnType<typeof setTimeout> | null = null;
  private current: AbortController | null = null;

  constructor(private readonly debounceMs: number) {}

  schedule(producer: (signal: AbortSignal) => Promise<void>): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
    }
    this.timer = setTimeout(() => {
      this.timer = null;
      void this.run(producer);
    }, this.debounceMs);
  }

  /** Run immediately (no debounce), aborting anything scheduled/in-flight. */
  runNow(producer: (signal: AbortSignal) => Promise<void>): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    void this.run(producer);
  }

  private async run(producer: (signal: AbortSignal) => Promise<void>): Promise<void> {
    if (this.current) {
      this.current.abort();
    }
    const controller = new AbortController();
    this.current = controller;
    try {
      await producer(controller.signal);
    } catch {
      // Producers handle their own errors; this only guards rejections.
    }
  }

  /** Abort everything in flight and drop pending schedules. */
  invalidate(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    if (this.current) {
      this.current.abort();
      this.current = null;
    }
  }

  dispose(): void {
    this.invalidate();
  }
}
