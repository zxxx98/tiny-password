let fallbackCounter = 0;

function uuidFromRandomValues(): string {
  const bytes = new Uint8Array(16);
  globalThis.crypto.getRandomValues(bytes);
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/** Creates a valid, per-request key without requiring crypto.randomUUID. */
export function createIdempotencyKey(): string {
  const cryptoApi = globalThis.crypto;
  if (typeof cryptoApi?.randomUUID === "function") {
    return cryptoApi.randomUUID();
  }
  if (typeof cryptoApi?.getRandomValues === "function") {
    return uuidFromRandomValues();
  }

  // Idempotency keys are identifiers rather than secrets. Keep the UI usable
  // in runtimes with no Web Crypto implementation at all.
  fallbackCounter += 1;
  return `idempotency-${Date.now().toString(36)}-${fallbackCounter.toString(36)}`;
}
