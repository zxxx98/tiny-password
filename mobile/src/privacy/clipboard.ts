import Clipboard from '@react-native-clipboard/clipboard';
import {AppState} from 'react-native';

export const CLIPBOARD_TTL_MS = 60_000;

/** Only clear text that this app still owns; never erase a later user copy. */
export function createClipboardGuard(
  clipboard: {setString: (text: string) => void; getString: () => Promise<string>},
  now: () => number = Date.now,
  canRead: () => boolean = () => true,
) {
  let owned: {value: string; expiresAt: number} | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const clear = async (): Promise<void> => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    const candidate = owned;
    if (!candidate) {
      return;
    }
    candidate.expiresAt = 0;
    if (!canRead()) {
      return;
    }
    try {
      const current = await clipboard.getString();
      if (owned !== candidate) {
        return;
      }
      if (current === candidate.value) {
        clipboard.setString('');
      }
      owned = null;
    } catch {
      // Android can deny clipboard access in the background. Retry when
      // foregrounded; the OS may also have already cleared the clipboard.
    }
  };

  return {
    copy(value: string): void {
      if (timer !== null) {
        clearTimeout(timer);
      }
      clipboard.setString(value);
      owned = {value, expiresAt: now() + CLIPBOARD_TTL_MS};
      timer = setTimeout(() => {
        void clear();
      }, CLIPBOARD_TTL_MS);
    },
    clear,
    async clearExpired(): Promise<void> {
      if (owned && now() >= owned.expiresAt) {
        await clear();
      }
    },
  };
}

export const sensitiveClipboard = createClipboardGuard(Clipboard, Date.now, () => AppState.currentState === 'active');
