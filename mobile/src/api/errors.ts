import type {ApiErrorShape} from './types';

export interface ApiError {
  code: string;
  message: string;
  requestId?: string;
  currentRevision?: number;
  status?: number;
  /** Parsed from Retry-After (seconds) when the server sends it. */
  retryAfterSeconds?: number;
}

export type ApiResult<T> =
  | {kind: 'success'; status: number; data: T; headers: HeadersLike}
  | {kind: 'http-error'; status: number; error: ApiError}
  | {kind: 'network-error'; message: string}
  | {kind: 'aborted'};

/** Minimal header view shared by RN fetch and Node undici responses. */
export interface HeadersLike {
  get(name: string): string | null;
  getSetCookie?(): string[];
}

export function parseErrorEnvelope(raw: string, status: number): ApiError {
  let parsed: ApiErrorShape | null = null;
  try {
    parsed = raw ? (JSON.parse(raw) as ApiErrorShape) : null;
  } catch {
    parsed = null;
  }
  if (parsed && typeof parsed.code === 'string') {
    return {
      code: parsed.code,
      message: typeof parsed.message === 'string' ? parsed.message : '请求失败',
      requestId: parsed.request_id,
      currentRevision: parsed.current_revision,
      status,
    };
  }
  return {
    code: 'INTERNAL',
    message: '服务返回了无法解析的错误',
    status,
  };
}

export function isHttpError<T>(
  result: ApiResult<T>,
): result is {kind: 'http-error'; status: number; error: ApiError} {
  return result.kind === 'http-error';
}

export function isNetworkError<T>(
  result: ApiResult<T>,
): result is {kind: 'network-error'; message: string} {
  return result.kind === 'network-error';
}
