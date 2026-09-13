import {DebouncedRunner, LatestTracker} from '../src/api/async';

describe('LatestTracker', () => {
  it('only the newest id is current', () => {
    const t = new LatestTracker();
    const a = t.next();
    expect(t.isCurrent(a)).toBe(true);
    const b = t.next();
    expect(t.isCurrent(a)).toBe(false);
    expect(t.isCurrent(b)).toBe(true);
  });
});

describe('DebouncedRunner', () => {
  beforeEach(() => jest.useFakeTimers());
  afterEach(() => jest.useRealTimers());

  it('collapses rapid schedules into one run (debounce)', async () => {
    const runner = new DebouncedRunner(300);
    const runs: number[] = [];
    runner.schedule(_signal => {
      runs.push(1);
      return Promise.resolve();
    });
    runner.schedule(_signal => {
      runs.push(2);
      return Promise.resolve();
    });
    await jest.advanceTimersByTimeAsync(400);
    expect(runs).toEqual([2]);
    runner.dispose();
  });

  it('runNow invalidates a pending debounced run', async () => {
    const runner = new DebouncedRunner(300);
    const runs: string[] = [];
    runner.schedule(() => {
      runs.push('debounced');
      return Promise.resolve();
    });
    runner.runNow(() => {
      runs.push('now');
      return Promise.resolve();
    });
    await jest.advanceTimersByTimeAsync(400);
    expect(runs).toEqual(['now']);
    runner.dispose();
  });

  it('a slow in-flight run is invalidated by a newer schedule', async () => {
    const runner = new DebouncedRunner(0);
    let resolveFirst: () => void = () => {};
    const firstStarted = new Promise<void>(res => {
      resolveFirst = res;
    });
    const order: string[] = [];
    runner.runNow(signal => {
      order.push('first-start');
      resolveFirst();
      return new Promise<void>(resolve => {
        // hang until the test resolves it
        const t = setInterval(() => {
          if (signal.aborted) {
            clearInterval(t);
            resolve();
          }
        }, 5);
      });
    });
    await firstStarted;
    runner.runNow(() => {
      order.push('second');
      return Promise.resolve();
    });
    await jest.advanceTimersByTimeAsync(50);
    expect(order).toEqual(['first-start', 'second']);
    runner.dispose();
  });
});
