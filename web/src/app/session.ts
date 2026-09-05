import { useSyncExternalStore } from "react";

/**
 * In-memory session state (design §12: 敏感状态不持久化). The principal and
 * the CSRF token live only in this module — nothing ever touches
 * localStorage, sessionStorage, or cookies outside the session cookie the
 * server already controls.
 */
export type SessionInfo = {
  id: string;
  created_at: string;
  expires_at: string;
  idle_expires_at?: string;
  current?: boolean;
};

export type Principal = {
  user_id: string;
  username: string;
  role: "admin" | "member";
  must_change_password: boolean;
  idle_timeout_minutes: number;
  session?: SessionInfo;
};

type SessionState = {
  principal: Principal | null;
  csrfToken: string;
};

let state: SessionState = { principal: null, csrfToken: "" };
const listeners = new Set<() => void>();

function notify() {
  for (const listener of listeners) {
    listener();
  }
}

export const sessionStore = {
  get(): SessionState {
    return state;
  },
  subscribe(listener: () => void): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  set(next: SessionState) {
    state = next;
    notify();
  },
  /** Clears everything, including the in-memory CSRF token. */
  clear() {
    state = { principal: null, csrfToken: "" };
    notify();
  },
};

export function useSession(): SessionState {
  return useSyncExternalStore(
    (listener) => sessionStore.subscribe(listener),
    () => sessionStore.get(),
    () => sessionStore.get(),
  );
}

/** Event fired by the API client when any call ends in 401. */
export const SESSION_EXPIRED_EVENT = "tp:session-expired";
