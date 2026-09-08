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

function handleAuthFailure(status: number, suppressEvent = false): void {
  if (status === 401) {
    sessionStore.clear();
    if (!suppressEvent && typeof window !== "undefined") {
      window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
    }
  }
}

export type RequestOptions = {
  csrfToken?: string;
  /** Idempotency-Key header for safe create retries. */
  idempotencyKey?: string;
  /** Observes the raw response before the ok-check (e.g. rotated CSRF header). */
  onResponse?: (response: Response) => void;
  /** Aborts this single request; the global in-flight registry still applies. */
  signal?: AbortSignal;
  /** Reports bytes received while reading a binary response body. */
  onDownloadProgress?: (receivedBytes: number, totalBytes?: number) => void;
  /** Best-effort cleanup calls still clear a 401 session, but skip a duplicate expiry event. */
  suppressAuthFailure?: boolean;
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

function isBodyInit(body: unknown): body is BodyInit {
  return (
    (typeof FormData !== "undefined" && body instanceof FormData) ||
    (typeof Blob !== "undefined" && body instanceof Blob) ||
    (typeof URLSearchParams !== "undefined" && body instanceof URLSearchParams) ||
    body instanceof ArrayBuffer ||
    (typeof ReadableStream !== "undefined" && body instanceof ReadableStream)
  );
}

async function requestWithResponse<T>(
  method: string,
  url: string,
  body: unknown,
  options: RequestOptions | undefined,
  read: (response: Response) => Promise<T>,
): Promise<T> {
  const headers: Record<string, string> = {};
  const sendBody = body !== undefined;
  const rawBody = sendBody && isBodyInit(body);
  if (sendBody && !rawBody) {
    headers["Content-Type"] = "application/json";
  }
  if (options?.csrfToken && method !== "GET" && method !== "HEAD") {
    headers["X-CSRF-Token"] = options.csrfToken;
  }
  if (options?.idempotencyKey) {
    headers["Idempotency-Key"] = options.idempotencyKey;
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
      body: sendBody ? (rawBody ? body : JSON.stringify(body)) : undefined,
      signal: controller.signal,
    });
    options?.onResponse?.(response);
    if (!response.ok) {
      // Do not let the 401 broadcast abort the response body before its
      // structured ApiError has been parsed. Successful body reads remain in
      // the in-flight registry until they finish, so global aborts cover
      // binary downloads as well as fetch() itself.
      inFlight.delete(controller);
      handleAuthFailure(response.status, options?.suppressAuthFailure);
      throw await parseError(response);
    }
    return await read(response);
  } finally {
    inFlight.delete(controller);
  }
}

export async function request<T>(method: string, url: string, body?: unknown, options?: RequestOptions): Promise<T> {
  return requestWithResponse(method, url, body, options, async (response) => {
    if (response.status === 204) {
      return undefined as T;
    }
    return (await response.json()) as T;
  });
}

/** Session-aware binary response helper used by archive downloads. */
export async function requestBlob(
  method: string,
  url: string,
  body?: unknown,
  options?: RequestOptions,
): Promise<Blob> {
  return requestWithResponse(method, url, body, options, async (response) => {
    const progress = options?.onDownloadProgress;
    const contentLength = response.headers.get("Content-Length");
    const parsedLength = contentLength === null ? Number.NaN : Number.parseInt(contentLength, 10);
    const totalBytes = Number.isFinite(parsedLength) && parsedLength >= 0 ? parsedLength : undefined;

    if (!response.body) {
      const blob = await response.blob();
      progress?.(blob.size, totalBytes ?? blob.size);
      return blob;
    }

    const reader = response.body.getReader();
    const chunks: BlobPart[] = [];
    let receivedBytes = 0;
    progress?.(0, totalBytes);
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        if (!value) continue;
        chunks.push(value);
        receivedBytes += value.byteLength;
        progress?.(receivedBytes, totalBytes);
      }
    } finally {
      reader.releaseLock();
    }
    return new Blob(chunks, { type: response.headers.get("Content-Type") ?? "" });
  });
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
