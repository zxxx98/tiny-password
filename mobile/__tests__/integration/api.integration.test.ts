import {spawn, spawnSync, type ChildProcess} from 'child_process';
import {mkdtempSync, writeFileSync} from 'fs';
import {tmpdir} from 'os';
import path from 'path';
import net from 'net';

import {normalizeServerUrl} from '../../src/api/client';
import {SessionController} from '../../src/auth/session';
import {createIdempotencyKeyManager, canonicalCreateContent} from '../../src/vault/idempotency';
import {mergePayload} from '../../src/vault/payloadMerge';

/**
 * API-level integration test against a REAL tiny-password Go server.
 * Drives the exact client code the Android app uses (TinyPasswordApi +
 * SessionController + CookieJar) over Node's undici fetch.
 *
 * Run with: MOBILE_INTEGRATION=1 npx jest __tests__/integration
 * Requires: go toolchain. Skipped by default (no server, no device needed).
 */
const RUN = process.env.MOBILE_INTEGRATION === '1';
// When MOBILE_INTEGRATION_SERVER_URL is set, the suite drives that already-
// running server instead of building and spawning one locally (useful on
// hosts where the server's Linux-only tmpfs staging cannot start).
const EXTERNAL_URL = process.env.MOBILE_INTEGRATION_SERVER_URL || '';
const d = RUN ? describe : describe.skip;

function freePort(): Promise<number> {
  return new Promise(resolve => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
  });
}

async function waitFor(url: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url);
      if (res.ok) {
        return;
      }
    } catch {
      // not up yet
    }
    await new Promise<void>(r => setTimeout(r, 250));
  }
  throw new Error('server did not become ready: ' + url);
}

d('mobile API client against the real Go server', () => {
  const repoRoot = path.resolve(__dirname, '../../..');
  let serverProcess: ChildProcess;
  let dataDir: string;
  let baseUrl: string;
  let setupToken = '';

  const adminPassword = 'admin-password-2026';
  const memberUsername = 'member1';
  const memberInitial = 'member-initial-1';
  const memberNewPassword = 'member-new-password-1';

  beforeAll(async () => {
    if (EXTERNAL_URL) {
      baseUrl = EXTERNAL_URL;
      setupToken = process.env.MOBILE_INTEGRATION_SETUP_TOKEN || '';
      return;
    }
    const buildDir = mkdtempSync(path.join(tmpdir(), 'tp-build-'));
    const binary = path.join(
      buildDir,
      process.platform === 'win32' ? 'tiny-password.exe' : 'tiny-password',
    );
    const build = spawnSync('go', ['build', '-o', binary, './cmd/tiny-password'], {
      cwd: repoRoot,
      encoding: 'utf8',
      timeout: 300000,
    });
    if (build.status !== 0) {
      throw new Error('go build failed: ' + build.stderr);
    }

    dataDir = mkdtempSync(path.join(tmpdir(), 'tp-data-'));
    const keyFile = path.join(dataDir, 'master_key');
    const keyBytes = Buffer.from(Array.from({length: 32}, () => Math.floor(Math.random() * 256)));
    writeFileSync(keyFile, keyBytes);

    const port = await freePort();
    baseUrl = `http://127.0.0.1:${port}`;
    serverProcess = spawn(binary, [], {
      env: {
        ...process.env,
        TP_ADDR: `127.0.0.1:${port}`,
        TP_DATA_DIR: dataDir,
        TP_MASTER_KEY_FILE: keyFile,
        TP_ALLOW_INSECURE_COOKIES: '1',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    serverProcess.stdout!.setEncoding('utf8');
    serverProcess.stdout!.on('data', (chunk: string) => {
      for (const line of chunk.split('\n')) {
        if (!line.trim()) {
          continue;
        }
        try {
          const event = JSON.parse(line);
          if (event.msg === 'setup_token_issued') {
            setupToken = event.token;
          }
        } catch {
          // non-JSON line
        }
      }
    });
    await waitFor(`${baseUrl}/healthz`, 30000);
    await waitFor(`${baseUrl}/api/v1/setup/status`, 30000);
  }, 400000);

  afterAll(() => {
    serverProcess?.kill('SIGTERM');
  });

  /** Fresh SessionController bound to the test server. */
  function newSession(): SessionController {
    const controller = new SessionController();
    controller.setServerUrl(normalizeServerUrl(baseUrl)!);
    return controller;
  }

  it('setup: admin is created with the one-time token (pre-auth CSRF enforced)', async () => {
    const session = newSession();
    const api = session.getApi()!;

    // Without the pre-auth context the write must be rejected.
    const noCsrf = await api.request({
      method: 'POST',
      path: '/setup/init',
      body: {token: setupToken, username: 'admin', password: adminPassword},
    });
    expect(noCsrf.kind).toBe('http-error');

    expect(await session.preflight()).toBe(true);
    const result = await api.request({
      method: 'POST',
      path: '/setup/init',
      body: {token: setupToken, username: 'admin', password: adminPassword},
      csrfToken: session.preauthToken!,
    });
    expect(result.kind).toBe('success');
  });

  it('admin login + member creation + forced member password change', async () => {
    const admin = newSession();
    await admin.preflight();
    const login = await admin.login('admin', adminPassword);
    expect(login.ok).toBe(true);
    expect(admin.getSnapshot().phase).toBe('authenticated');

    const api = admin.getApi()!;
    const created = await api.request({
      method: 'POST',
      path: '/users',
      body: {username: memberUsername, initial_password: memberInitial},
      csrfToken: admin.csrfToken!,
    });
    expect(created.kind).toBe('success');

    // Member first login: must change password.
    const member = newSession();
    await member.preflight();
    const memberLogin = await member.login(memberUsername, memberInitial);
    expect(memberLogin.ok).toBe(true);
    expect(member.getSnapshot().phase).toBe('must-change');

    // Items are NOT accessible while the change is pending.
    const blocked = await member.getApi()!.listItems();
    expect(blocked.kind).toBe('http-error');
    if (blocked.kind === 'http-error') {
      expect(blocked.error.code).toBe('PASSWORD_CHANGE_REQUIRED');
    }

    const change = await member.changePassword(memberInitial, memberNewPassword);
    expect(change.ok).toBe(true);
    expect(await member.confirmSession()).toBe('confirmed');
    expect(member.getSnapshot().phase).toBe('authenticated');
  });

  it('vault: create with idempotent retry, list Meta, search, detail preservation', async () => {
    const member = newSession();
    await member.preflight();
    await member.login(memberUsername, memberNewPassword);
    const api = member.getApi()!;
    const keyManager = createIdempotencyKeyManager();

    const payload = {
      name: 'GitHub',
      username: 'octocat@example.com',
      password: 'entry-secret-1',
      urls: ['https://github.com', 'https://gist.github.com'],
      notes: 'work account',
      password_updated_at: '2026-02-03',
    };
    const key = keyManager.keyFor(
      canonicalCreateContent(payload as unknown as Record<string, unknown>, {
        item_type: 'login',
        vault_scope: 'personal',
      }),
    );

    const created = await api.createItem(payload, 'login', key, member.csrfToken!);
    expect(created.kind).toBe('success');
    const first = created.kind === 'success' ? created.data : null;
    expect(first!.payload.name).toBe('GitHub');
    expect(first!.revision).toBe(1);

    // Network-style retry with the SAME key and content replays the original.
    const retry = await api.createItem(payload, 'login', key, member.csrfToken!);
    expect(retry.kind).toBe('success');
    expect((retry as unknown as {data: {id: string}}).data.id).toBe(first!.id);

    // A different key creates a genuinely new item (server dedup is key-bound).
    const other = await api.createItem(
      {name: 'Amazon', username: '', password: 'p2'},
      'login',
      'idem-key-second-00001',
      member.csrfToken!,
    );
    expect(other.kind).toBe('success');
    expect((other as unknown as {data: {id: string}}).data.id).not.toBe(first!.id);

    // Every supported non-login type creates and reads back typed payloads.
    const ssh = await api.createItem(
      {
        name: 'deploy',
        algorithm: 'ed25519',
        public_key: 'ssh-ed25519 AAAA',
        private_key: '-----BEGIN OPENSSH PRIVATE KEY-----',
      },
      'ssh_key',
      'idem-key-ssh-000001',
      member.csrfToken!,
    );
    expect(ssh.kind).toBe('success');
    const sshDetail = ssh.kind === 'success' ? ssh.data : null;
    expect(sshDetail!.item_type).toBe('ssh_key');
    expect((sshDetail!.payload as {algorithm: string}).algorithm).toBe('ed25519');

    const note = await api.createItem({name: 'note', body: 'secret body'}, 'secure_note', 'idem-key-note-00001', member.csrfToken!);
    expect(note.kind).toBe('success');

    const card = await api.createItem(
      {name: 'card', cardholder: 'ADA', number: '4242424242424242', exp_month: 4, exp_year: 2029},
      'credit_card',
      'idem-key-card-00001',
      member.csrfToken!,
    );
    expect(card.kind).toBe('success');

    const identity = await api.createItem({name: 'me', full_name: 'Ada'}, 'identity', 'idem-key-ident-0001', member.csrfToken!);
    expect(identity.kind).toBe('success');

    const secretEntry = await api.createItem(
      {name: 'tokens', entries: [{key: 'A', value: '1'}]},
      'secret',
      'idem-key-secr-00001',
      member.csrfToken!,
    );
    expect(secretEntry.kind).toBe('success');

    const sharedPayload = {
      name: 'Shared GitHub',
      username: 'shared@example.com',
      password: 'shared-secret-1',
    };
    const shared = await api.request<{id: string}>({
      method: 'POST',
      path: '/items',
      body: {item_type: 'login', vault_scope: 'shared', payload: sharedPayload},
      csrfToken: member.csrfToken!,
      extraHeaders: {'Idempotency-Key': 'idem-key-shared-00001'},
    });
    expect(shared.kind).toBe('success');

    // List uses Meta only: no payload fields leak into the page, and all six
    // types appear without any type filter. Readable shared items are included
    // alongside the member's personal items.
    const list = await api.listItems();
    expect(list.kind).toBe('success');
    const listData = (list as unknown as {data: {items: Array<Record<string, unknown>>; next_cursor: null}}).data;
    expect(listData.items).toHaveLength(8);
    expect(listData.next_cursor).toBeNull();
    expect(listData.items[0].title).toBeDefined();
    expect(listData.items[0].payload).toBeUndefined();
    expect(new Set(listData.items.map(item => item.vault_scope))).toEqual(new Set(['personal', 'shared']));
    expect(listData.items.some(item => item.title === 'Shared GitHub' && item.vault_scope === 'shared')).toBe(true);
    const listedTypes = new Set(listData.items.map(i => i.item_type));
    expect([...listedTypes].sort()).toEqual(['credit_card', 'identity', 'login', 'secret', 'secure_note', 'ssh_key']);

    // Server-side search matches names/usernames/urls/notes.
    const hit = await api.searchItems('octocat', null, member.csrfToken!);
    const miss = await api.searchItems('not-present-query', null, member.csrfToken!);
    const urlHit = await api.searchItems('gist.github.com', null, member.csrfToken!);
    const sharedHit = await api.searchItems('Shared GitHub', null, member.csrfToken!);
    expect((hit as unknown as {data: {items: unknown[]}}).data.items).toHaveLength(1);
    expect((miss as unknown as {data: {items: unknown[]}}).data.items).toHaveLength(0);
    expect((urlHit as unknown as {data: {items: unknown[]}}).data.items).toHaveLength(1);
    expect((sharedHit as unknown as {data: {items: Array<{title?: string; vault_scope: string}>}}).data.items).toEqual([
      expect.objectContaining({title: 'Shared GitHub', vault_scope: 'shared'}),
    ]);

    // Detail exposes the full payload including the unshown date field.
    const detail = await api.getItem(first!.id);
    expect(detail.kind).toBe('success');
    const full = (detail as unknown as {data: {payload: Record<string, unknown>; revision: number}}).data;
    expect(full.payload.password_updated_at).toBe('2026-02-03');
    expect(full.revision).toBe(1);

    // Update merges onto the original payload, keeps the date, bumps revision.
    const edits = {
      name: 'GitHub',
      username: 'new-email@example.com',
      password: 'entry-secret-1',
      urls: ['https://github.com'],
      notes: 'work account',
    };
    const merged = mergePayload('login', full.payload as never, edits);
    const update = await api.updateItem(first!.id, full.revision, merged, member.csrfToken!);
    expect(update.kind).toBe('success');
    const updated = (update as unknown as {data: {payload: Record<string, unknown>; revision: number}}).data;
    expect(updated.revision).toBe(2);
    expect(updated.payload.username).toBe('new-email@example.com');
    expect(updated.payload.password_updated_at).toBe('2026-02-03');
    expect(updated.payload.urls).toEqual(['https://github.com']);

    // Stale revision → 409 REVISION_CONFLICT with the current revision.
    const conflict = await api.updateItem(first!.id, full.revision, merged, member.csrfToken!);
    expect(conflict.kind).toBe('http-error');
    if (conflict.kind === 'http-error') {
      expect(conflict.error.code).toBe('REVISION_CONFLICT');
      expect(conflict.error.currentRevision).toBe(2);
    }
  });

  it('generator returns a CSPRNG value of the requested length', async () => {
    const member = newSession();
    await member.preflight();
    await member.login(memberUsername, memberNewPassword);
    const result = await member.getApi()!.generatePassword(
      {length: 24, lowercase: true, uppercase: true, digits: true, symbols: true, exclude_ambiguous: true},
      member.csrfToken!,
    );
    expect(result.kind).toBe('success');
    if (result.kind === 'success') {
      expect(result.data.value).toHaveLength(24);
    }
  });

  it('trash semantics: delete hides from list and detail 404s; sign-out kills the session', async () => {
    const member = newSession();
    await member.preflight();
    await member.login(memberUsername, memberNewPassword);
    const api = member.getApi()!;

    const created = await api.createItem(
      {name: 'TrashMe', username: '', password: 'pw'},
      'login',
      'idem-key-trash-000001',
      member.csrfToken!,
    );
    expect(created.kind).toBe('success');
    const id = (created as unknown as {data: {id: string}}).data.id;

    const trashed = await api.trashItem(id, member.csrfToken!);
    expect(trashed.kind).toBe('success');

    const gone = await api.getItem(id);
    expect(gone.kind).toBe('http-error');
    if (gone.kind === 'http-error') {
      expect(gone.status).toBe(404);
    }

    const list = await api.listItems();
    expect((list as unknown as {data: {items: unknown[]}}).data.items.every(i => (i as {id: string}).id !== id)).toBe(true);

    const out = await member.signOut();
    expect(out.serverConfirmed).toBe(true);
    const after = await api.listItems();
    expect(after.kind).toBe('http-error');
    if (after.kind === 'http-error') {
      expect(after.status).toBe(401);
    }
    expect(member.getSnapshot().phase).toBe('signed-out');
  });
});
