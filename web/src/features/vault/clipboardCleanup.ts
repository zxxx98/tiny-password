import { SESSION_EXPIRED_EVENT } from "../../app/session";

export type ClipboardCleanupResult = "cleared" | "changed" | "unavailable";

type CleanupTask = {
  value: string;
  timer: number;
  resolve: (result: ClipboardCleanupResult) => void;
};

let task: CleanupTask | null = null;
let listenerInstalled = false;

function clearTask(result: ClipboardCleanupResult) {
  if (!task) return;
  window.clearTimeout(task.timer);
  const current = task;
  task = null;
  current.resolve(result);
}

function installSessionCleanup() {
  if (listenerInstalled || typeof window === "undefined") return;
  listenerInstalled = true;
  window.addEventListener(SESSION_EXPIRED_EVENT, () => clearClipboardCleanup());
}

async function clean(value: string): Promise<ClipboardCleanupResult> {
  try {
    const current = await navigator.clipboard.readText();
    if (current !== value) return "changed";
    await navigator.clipboard.writeText("");
    return "cleared";
  } catch {
    return "unavailable";
  }
}

/** Schedules best-effort cleanup independently from any field component. */
export function scheduleClipboardCleanup(value: string): Promise<ClipboardCleanupResult> {
  installSessionCleanup();
  clearTask("changed");
  return new Promise((resolve) => {
    task = {
      value,
      timer: window.setTimeout(async () => {
        const current = task;
        task = null;
        const result = await clean(value);
        current?.resolve(result);
      }, 30_000),
      resolve,
    };
  });
}

export function clearClipboardCleanup() {
  clearTask("changed");
}
