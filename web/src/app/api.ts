import { SESSION_EXPIRED_EVENT, sessionStore } from "./session";

/**
 * Same-origin API client. Every error carries the server's stable code and
 * the request_id so any page can render it. A 401 clears the in-memory
 * session and fires the session-expired event the shell listens to.
 * Sensitive data only ever lives in memory (design §12).
 */
export class ApiError extends Error {
  readonly code: string;
  readonly requestId: string;
  readonly status: number;
  /** Current revision, present on 409 REVISION_CONFLICT. */
  readonly currentRevision?: number;

  constructor(status: number, code: string, message: string, requestId: string, currentRevision?: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.currentRevision = currentRevision;
  }

  /** 503/429 responses the UI may invite the user to retry. */
  get retryable(): boolean {
    return this.status === 503 || this.status === 429;
  }
}

async function parseError(response: Response): Promise<ApiError> {
  let code = "INTERNAL";
  let message = "请求失败";
  let requestId = "";
  let currentRevision: number | undefined;
  try {
    const body = (await response.json()) as {
      code?: string;
      message?: string;
      request_id?: string;
      current_revision?: number;
    };
    if (body.code) code = body.code;
    if (body.message) message = body.message;
    if (body.request_id) requestId = body.request_id;
    if (typeof body.current_revision === "number") currentRevision = body.current_revision;
  } catch {
    // non-JSON error body; keep defaults
  }
  return new ApiError(response.status, code, message, requestId, currentRevision);
}

function handleAuthFailure(status: number): void {
  if (status === 401) {
    sessionStore.clear();
    if (typeof window !== "undefined") {
      window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    }
  }
}

export type RequestOptions = {
  csrfToken?: string;
  /** Observes the raw response before the ok-check (e.g. rotated CSRF header). */
  onResponse?: (response: Response) => void;
  /** Aborts this single request; the global in-flight registry still applies. */
  signal?: AbortSignal;
};

// In-flight request registry. Locking or logging out aborts everything so a
// late response can never repopulate cleared UI state (design §12.4).
const inFlight = new Set<AbortController>();

export function abortInFlightRequests(): number {
  const count = inFlight.size;
  for (const controller of inFlight) {
    controller.abort();
  }
  return count;
}

export async function request<T>(method: string, url: string, body?: unknown, options?: RequestOptions): Promise<T> {
  const headers: Record<string, string> = {};
  const sendBody = body !== undefined;
  if (sendBody) {
    headers["Content-Type"] = "application/json";
  }
  if (options?.csrfToken && method !== "GET" && method !== "HEAD") {
    headers["X-CSRF-Token"] = options.csrfToken;
  }
  const controller = new AbortController();
  inFlight.add(controller);
  if (options?.signal) {
    // Compose the caller's signal with the global registry: either aborts.
    if (options.signal.aborted) {
      controller.abort();
    } else {
      options.signal.addEventListener("abort", () => controller.abort(), { once: true });
    }
  }
  let response: Response;
  try {
    response = await fetch(url, {
      method,
      credentials: "same-origin",
      headers,
      body: sendBody ? JSON.stringify(body) : undefined,
      signal: controller.signal,
    });
  } finally {
    inFlight.delete(controller);
  }
  options?.onResponse?.(response);
  if (!response.ok) {
    handleAuthFailure(response.status);
    throw await parseError(response);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

export async function getJSON<T>(url: string): Promise<T> {
  return request<T>("GET", url);
}

export async function postJSON<T>(url: string, body: unknown, options?: { csrfToken?: string }): Promise<T> {
  return request<T>("POST", url, body, options);
}

export async function fetchCsrfToken(): Promise<string> {
  const body = await request<{ csrf_token: string }>("POST", "/api/v1/csrf", {});
  return body.csrf_token;
}
