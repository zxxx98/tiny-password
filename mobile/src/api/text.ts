// Text measurement helpers matching the server's limits: OpenAPI string
// bounds are Unicode code points ("characters"), the password policy also
// caps UTF-8 bytes. Hermes has no TextEncoder, so bytes are counted by hand.

export function runeLength(s: string): number {
  return Array.from(s).length;
}

export function utf8ByteLength(s: string): number {
  let bytes = 0;
  for (const ch of s) {
    const cp = ch.codePointAt(0)!;
    if (cp <= 0x7f) {
      bytes += 1;
    } else if (cp <= 0x7ff) {
      bytes += 2;
    } else if (cp <= 0xffff) {
      bytes += 3;
    } else {
      bytes += 4;
    }
  }
  return bytes;
}

export function isBlank(s: string): boolean {
  return s.trim().length === 0;
}
