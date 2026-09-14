export const SERVER_URL_STORAGE_KEY = '@tiny-password/server-url';

export interface RememberedCredentials {
  username: string;
  password: string;
}

export interface RememberedLoginSnapshot {
  serverUrl: string | null;
  credentials: RememberedCredentials | null;
}

export interface StorageLike {
  getItem(key: string): Promise<string | null>;
  setItem(key: string, value: string): Promise<void>;
  removeItem(key: string): Promise<void>;
}

export interface KeychainLike {
  get(): Promise<RememberedCredentials | null>;
  set(username: string, password: string): Promise<void>;
  clear(): Promise<void>;
}

export interface RememberedLoginStore {
  load(): Promise<RememberedLoginSnapshot>;
  loadServerUrl(): Promise<string | null>;
  saveServerUrl(serverUrl: string): Promise<void>;
  loadCredentials(): Promise<RememberedCredentials | null>;
  saveCredentials(credentials: RememberedCredentials): Promise<void>;
  clearCredentials(): Promise<void>;
}

export function createRememberedLoginStore(deps: {
  storage: StorageLike;
  keychain: KeychainLike;
}): RememberedLoginStore {
  const loadServerUrl = async (): Promise<string | null> => {
    const value = await deps.storage.getItem(SERVER_URL_STORAGE_KEY);
    const trimmed = value?.trim() ?? '';
    return trimmed.length > 0 ? trimmed : null;
  };

  const loadCredentials = (): Promise<RememberedCredentials | null> => deps.keychain.get();

  return {
    async load() {
      const [serverUrl, credentials] = await Promise.all([loadServerUrl(), loadCredentials()]);
      return {serverUrl, credentials};
    },
    loadServerUrl,
    saveServerUrl(serverUrl) {
      return deps.storage.setItem(SERVER_URL_STORAGE_KEY, serverUrl);
    },
    loadCredentials,
    saveCredentials(credentials) {
      return deps.keychain.set(credentials.username, credentials.password);
    },
    clearCredentials() {
      return deps.keychain.clear();
    },
  };
}
