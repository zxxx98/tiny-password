// Minimal same-origin API client. Grows with T07/T14 (error handling,
// session awareness, request_id plumbing). Data stays in memory only.
export class ApiError extends Error {
  readonly code: string;
  readonly requestId: string;
  readonly status: number;

  constructor(status: number, code: string, message: string, requestId: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }
}

async function parseError(response: Response): Promise<ApiError> {
  let code = "INTERNAL";
  let message = "请求失败";
  let requestId = "";
  try {
    const body = (await response.json()) as {
      code?: string;
      message?: string;
      request_id?: string;
    };
    if (body.code) code = body.code;
    if (body.message) message = body.message;
    if (body.request_id) requestId = body.request_id;
  } catch {
    // non-JSON error body; keep defaults
  }
  return new ApiError(response.status, code, message, requestId);
}

export async function getJSON<T>(url: string): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin" });
  if (!response.ok) {
    throw await parseError(response);
  }
  return (await response.json()) as T;
}

export async function postJSON<T>(
  url: string,
  body: unknown,
  options?: { csrfToken?: string },
): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };
  if (options?.csrfToken) {
    headers["X-CSRF-Token"] = options.csrfToken;
  }
  const response = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers,
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw await parseError(response);
  }
  return (await response.json()) as T;
}

export async function fetchCsrfToken(): Promise<string> {
  const body = await postJSON<{ csrf_token: string }>("/api/v1/csrf", {});
  return body.csrf_token;
}
