import {ApiClient, TinyPasswordApi, type FetchLike} from '../api/client';
import type {CurrentPrincipal} from '../api/types';

export type SessionPhase =
  | 'unconfigured' // no valid server address yet
  | 'signed-out' // server configured, no session
  | 'authenticating' // login request in flight
  | 'must-change' // logged in but forced password change pending
  | 'confirming-change' // password change accepted; session confirmation pending
  | 'authenticated';

export interface SessionSnapshot {
  phase: SessionPhase;
  principal: CurrentPrincipal | null;
  /** Populated while confirming-change fails so the UI can offer retry. */
  confirmError: string | null;
  serverUrl: string | null;
}

/** Throttle window for POST /auth/session/activity (foreground gestures only). */
export const ACTIVITY_THROTTLE_MS = 5 * 60 * 1000;

type Listener = () => void;

/**
 * Authentication state machine over the cookie+CSRF API. Framework-free so
 * transitions are unit-testable; React binds via useSyncExternalStore.
 *
 * A monotonically increasing generation token guards every async flow: sign
 * out, server switch or session invalidation bump the generation and abort
 * in-flight requests, so stale responses can never mutate newer state.
 */
export class SessionController {
  private phase: SessionPhase = 'unconfigured';
  private principal: CurrentPrincipal | null = null;
  private preauthCsrf: string | null = null;
  private sessionCsrf: string | null = null;
  private confirmError: string | null = null;
  private api: TinyPasswordApi | null = null;
  private generation = 0;
  private listeners = new Set<Listener>();
  private inFlight = new Set<AbortController>();
  private lastActivityMs = 0;
  private clock: () => number;
  private readonly fetchImpl: FetchLike | undefined;

  constructor(clock: () => number = Date.now, fetchImpl?: FetchLike) {
    this.clock = clock;
    this.fetchImpl = fetchImpl;
  }

  // --- store plumbing ---------------------------------------------------------

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): SessionSnapshot => ({
    phase: this.phase,
    principal: this.principal,
    confirmError: this.confirmError,
    serverUrl: this.api ? this.api.client.serverUrl : null,
  });

  private notify(): void {
    for (const listener of this.listeners) {
      listener();
    }
  }

  private setPhase(phase: SessionPhase): void {
    this.phase = phase;
    this.notify();
  }

  // --- generation guard -------------------------------------------------------

  private currentGeneration(): number {
    return this.generation;
  }

  private isCurrent(gen: number): boolean {
    return this.generation === gen;
  }

  private trackSignal(gen: number): {signal: AbortSignal; done: () => void} | null {
    if (!this.isCurrent(gen)) {
      return null;
    }
    const controller = new AbortController();
    this.inFlight.add(controller);
    return {
      signal: controller.signal,
      done: () => this.inFlight.delete(controller),
    };
  }

  /** Abort everything in flight; called on sign out / server switch / 401. */
  private bumpGeneration(): void {
    this.generation += 1;
    for (const controller of this.inFlight) {
      controller.abort();
    }
    this.inFlight.clear();
  }

  // --- actions ----------------------------------------------------------------

  /**
   * Point the session at a server. Replaces the cookie jar and CSRF context
   * so nothing can leak across servers, and cancels every in-flight request
   * from the previous server.
   */
  setServerUrl(url: string): boolean {
    this.bumpGeneration();
    this.preauthCsrf = null;
    this.sessionCsrf = null;
    this.principal = null;
    this.confirmError = null;
    try {
      this.api = new TinyPasswordApi(new ApiClient(url, {fetchImpl: this.fetchImpl}));
    } catch {
      this.api = null;
      this.phase = 'unconfigured';
      this.notify();
      return false;
    }
    this.phase = 'signed-out';
    this.notify();
    return true;
  }

  get serverConfigured(): boolean {
    return this.api !== null;
  }

  /** Pre-auth CSRF for the login request (D07). */
  async preflight(): Promise<boolean> {
    const api = this.api;
    if (!api) {
      return false;
    }
    const gen = this.currentGeneration();
    const tracked = this.trackSignal(gen);
    if (!tracked) {
      return false;
    }
    try {
      const result = await api.issueCsrf(tracked.signal);
      if (!this.isCurrent(gen)) {
        return false;
      }
      if (result.kind === 'success') {
        this.preauthCsrf = result.data.csrf_token;
        return true;
      }
      return false;
    } finally {
      tracked.done();
    }
  }

  /**
   * Username + password login. On success the pre-auth context is consumed
   * server-side; the body's csrf_token is the new session-bound token.
   */
  async login(
    username: string,
    password: string,
  ): Promise<{ok: boolean; error: string | null}> {
    const api = this.api;
    if (!api || !this.preauthCsrf) {
      return {ok: false, error: '缺少预认证上下文，请重试'};
    }
    const gen = this.currentGeneration();
    if (!this.isCurrent(gen)) {
      return {ok: false, error: null};
    }
    this.setPhase('authenticating');
    const tracked = this.trackSignal(gen);
    if (!tracked) {
      return {ok: false, error: null};
    }
    try {
      const result = await api.login(username, password, this.preauthCsrf, tracked.signal);
      if (!this.isCurrent(gen)) {
        return {ok: false, error: null};
      }
      if (result.kind === 'success') {
        this.principal = result.data.user;
        this.sessionCsrf = result.data.csrf_token;
        this.preauthCsrf = null;
        this.confirmError = null;
        this.lastActivityMs = this.clock();
        if (result.data.must_change_password || result.data.user.must_change_password) {
          this.setPhase('must-change');
        } else {
          this.setPhase('authenticated');
        }
        return {ok: true, error: null};
      }
      this.setPhase('signed-out');
      return {ok: false, error: SessionController.loginError(result)};
    } finally {
      tracked.done();
    }
  }

  /** Login-phase error mapping for the UI. */
  static loginError(result: {
    kind: string;
    status?: number;
    error?: {code: string; message: string; retryAfterSeconds?: number};
    message?: string;
  }): string {
    if (result.kind === 'network-error') {
      return result.message || '无法连接服务器，请检查地址与网络';
    }
    if (result.kind === 'aborted') {
      return '';
    }
    if (result.kind === 'http-error' && result.error) {
      if (result.error.code === 'ACCOUNT_DISABLED') {
        return '账号已被停用，请联系管理员';
      }
      if (result.error.code === 'RATE_LIMITED') {
        const wait = result.error.retryAfterSeconds;
        return wait
          ? `尝试过于频繁，请约 ${wait} 秒后再试`
          : '尝试过于频繁，请稍后再试';
      }
      if (result.error.code === 'FORBIDDEN') {
        return '安全校验失败（CSRF/Origin）';
      }
      if (result.status === 401) {
        return '用户名或密码错误';
      }
      return result.error.message;
    }
    return '登录失败';
  }

  /**
   * Forced password change. On 204 the response carries the rotated session
   * cookie and X-CSRF-Token; both replace the old context. The phase moves to
   * confirming-change — the password is never resubmitted if confirmation
   * fails; the UI offers a confirmation retry instead.
   */
  async changePassword(
    currentPassword: string,
    newPassword: string,
  ): Promise<{ok: boolean; error: string | null}> {
    const api = this.api;
    if (!api || !this.sessionCsrf) {
      return {ok: false, error: '会话上下文缺失'};
    }
    const gen = this.currentGeneration();
    const tracked = this.trackSignal(gen);
    if (!tracked) {
      return {ok: false, error: '会话已变更'};
    }
    try {
      const result = await api.changePassword(
        currentPassword,
        newPassword,
        this.sessionCsrf,
        tracked.signal,
      );
      if (!this.isCurrent(gen)) {
        return {ok: false, error: '会话已变更'};
      }
      if (result.kind === 'success') {
        const rotated = result.headers.get('x-csrf-token');
        if (rotated) {
          this.sessionCsrf = rotated;
        }
        this.confirmError = null;
        this.setPhase('confirming-change');
        return {ok: true, error: null};
      }
      if (result.kind === 'http-error' && result.status === 401) {
        this.invalidateLocally();
        return {ok: false, error: '认证失败，请重新登录'};
      }
      if (result.kind === 'network-error') {
        return {ok: false, error: '网络异常，改密未确认'};
      }
      if (result.kind === 'http-error' && result.error) {
        if (result.error.code === 'RATE_LIMITED') {
          const wait = result.error.retryAfterSeconds;
          return {
            ok: false,
            error: wait ? `尝试过于频繁，请约 ${wait} 秒后再试` : '尝试过于频繁，请稍后再试',
          };
        }
        return {ok: false, error: result.error.message};
      }
      return {ok: false, error: '改密失败'};
    } finally {
      tracked.done();
    }
  }

  /**
   * Re-check GET /auth/session after a password change. Does NOT resubmit
   * the change; safe to call repeatedly until it succeeds or the user signs
   * out. Also used by the foreground resume flow to revalidate the session.
   */
  async confirmSession(): Promise<'confirmed' | 'still-required' | 'invalid' | 'unreachable'> {
    const api = this.api;
    if (!api || !this.sessionCsrf) {
      return 'invalid';
    }
    const gen = this.currentGeneration();
    const tracked = this.trackSignal(gen);
    if (!tracked) {
      return 'invalid';
    }
    try {
      const result = await api.getSession(tracked.signal);
      if (!this.isCurrent(gen)) {
        return 'invalid';
      }
      if (result.kind === 'success') {
        this.principal = result.data.user;
        this.sessionCsrf = result.data.csrf_token;
        if (result.data.user.must_change_password) {
          this.confirmError = null;
          this.setPhase('must-change');
          return 'still-required';
        }
        this.confirmError = null;
        this.lastActivityMs = this.clock();
        this.setPhase('authenticated');
        return 'confirmed';
      }
      if (result.kind === 'http-error' && result.status === 401) {
        this.invalidateLocally();
        return 'invalid';
      }
      this.confirmError = '无法确认会话状态，请重试';
      this.notify();
      return 'unreachable';
    } finally {
      tracked.done();
    }
  }

  /** Foreground resume validation. Returns whether content may be shown. */
  async validateOnResume(): Promise<'ok' | 'signed-out' | 'unreachable'> {
    if (this.phase !== 'authenticated' && this.phase !== 'must-change') {
      return 'signed-out';
    }
    const outcome = await this.confirmSession();
    switch (outcome) {
      case 'confirmed':
        return 'ok';
      case 'still-required':
        return 'ok'; // must-change phase is not sensitive-list content
      case 'invalid':
        return 'signed-out';
      case 'unreachable':
        return 'unreachable';
    }
  }

  /** Throttled renewal after real foreground user interactions only. */
  async touchActivity(): Promise<void> {
    if (this.phase !== 'authenticated' || !this.sessionCsrf || !this.api) {
      return;
    }
    const now = this.clock();
    if (now - this.lastActivityMs < ACTIVITY_THROTTLE_MS) {
      return;
    }
    this.lastActivityMs = now;
    const api = this.api;
    const gen = this.currentGeneration();
    const tracked = this.trackSignal(gen);
    if (!tracked) {
      return;
    }
    try {
      const result = await api.recordActivity(this.sessionCsrf, tracked.signal);
      if (!this.isCurrent(gen)) {
        return;
      }
      if (result.kind === 'http-error' && result.status === 401) {
        this.invalidateLocally();
      }
      // Other failures are silently ignored: renewal must never disturb UX.
    } catch {
      // never propagate
    } finally {
      tracked.done();
    }
  }

  /**
   * Sign out: best-effort server revocation, then unconditional local wipe
   * (cookies, CSRF, principal, in-flight requests). Returns whether the
   * server confirmed the revocation.
   */
  async signOut(): Promise<{serverConfirmed: boolean; notice: string | null}> {
    const api = this.api;
    const csrf = this.sessionCsrf;
    this.invalidateLocally();
    if (!api || !csrf) {
      return {serverConfirmed: false, notice: null};
    }
    const result = await api.logout(csrf);
    if (result.kind === 'success') {
      return {serverConfirmed: true, notice: null};
    }
    if (result.kind === 'http-error' && result.status === 401) {
      // Session already gone server-side; treat as confirmed.
      return {serverConfirmed: true, notice: null};
    }
    if (result.kind === 'http-error' && result.error?.code === 'ACCOUNT_DISABLED') {
      return {serverConfirmed: false, notice: '账号已被停用，本地已退出'};
    }
    return {
      serverConfirmed: false,
      notice: '网络异常，服务端会话未确认撤销；本地已退出',
    };
  }

  /**
   * Drop all session state without talking to the server (401 handling,
   * server switch). Cancels in-flight work so late responses are ignored.
   */
  invalidateLocally(): void {
    this.bumpGeneration();
    if (this.api) {
      this.api.client.jar.clear();
    }
    this.preauthCsrf = null;
    this.sessionCsrf = null;
    this.principal = null;
    this.confirmError = null;
    this.lastActivityMs = 0;
    this.setPhase(this.api ? 'signed-out' : 'unconfigured');
  }

  get csrfToken(): string | null {
    return this.sessionCsrf;
  }

  /** Pre-auth CSRF token (setup page equivalent, D07). */
  get preauthToken(): string | null {
    return this.preauthCsrf;
  }

  /** API bound to the current server; null before a server is configured. */
  getApi(): TinyPasswordApi | null {
    return this.api;
  }

  get currentPrincipal(): CurrentPrincipal | null {
    return this.principal;
  }
}
