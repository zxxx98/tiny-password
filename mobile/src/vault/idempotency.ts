// Idempotency keys for item creation. The server replays the original result
// for the same (user, operation, key, content fingerprint), so a network
// retry of one logical create must reuse the key, while any content change
// must move to a fresh key. The manager binds the key to the exact request
// content: retries with identical content keep the key; edits invalidate it.

export interface IdempotencyKeyManager {
  /** Key for the given canonical request content; stable across retries. */
  keyFor(canonicalContent: string): string;
  /** Drop the current binding (after success, cancel or content commit). */
  reset(): void;
  /** Whether a key is currently bound to the given content. */
  isBoundTo(canonicalContent: string): boolean;
}

const KEY_ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
const KEY_LENGTH = 24; // within the server's 16..128 [A-Za-z0-9_-] rule

export function randomIdempotencyKey(random: () => number = Math.random): string {
  let key = 'm-';
  for (let i = 0; i < KEY_LENGTH; i++) {
    key += KEY_ALPHABET.charAt(Math.floor(random() * KEY_ALPHABET.length));
  }
  return key;
}

export function createIdempotencyKeyManager(
  random: () => number = Math.random,
): IdempotencyKeyManager {
  let boundKey: string | null = null;
  let boundContent: string | null = null;
  return {
    keyFor(canonicalContent: string): string {
      if (boundKey && boundContent === canonicalContent) {
        return boundKey;
      }
      boundKey = randomIdempotencyKey(random);
      boundContent = canonicalContent;
      return boundKey;
    },
    reset(): void {
      boundKey = null;
      boundContent = null;
    },
    isBoundTo(canonicalContent: string): boolean {
      return boundKey !== null && boundContent === canonicalContent;
    },
  };
}

/**
 * Canonical JSON for idempotency binding: only the fields the server's
 * create fingerprint sees, sorted keys so re-renders cannot change identity.
 */
export function canonicalCreateContent(
  payload: Record<string, unknown>,
  fixed: {item_type: string; vault_scope: string},
): string {
  return JSON.stringify({fixed, payload: sortKeys(payload)});
}

function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(sortKeys);
  }
  if (value !== null && typeof value === 'object') {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as Record<string, unknown>).sort()) {
      out[key] = sortKeys((value as Record<string, unknown>)[key]);
    }
    return out;
  }
  return value;
}
