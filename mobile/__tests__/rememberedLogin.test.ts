import {
  createRememberedLoginStore,
  type KeychainLike,
  type StorageLike,
} from '../src/auth/rememberedLogin';

function setup() {
  const values = new Map<string, string>();
  const storage: StorageLike = {
    getItem: jest.fn(async key => values.get(key) ?? null),
    setItem: jest.fn(async (key, value) => {
      values.set(key, value);
    }),
    removeItem: jest.fn(async key => {
      values.delete(key);
    }),
  };
  let secure: {username: string; password: string} | null = null;
  const keychain: KeychainLike = {
    get: jest.fn(async () => secure),
    set: jest.fn(async (username, password) => {
      secure = {username, password};
    }),
    clear: jest.fn(async () => {
      secure = null;
    }),
  };
  return {store: createRememberedLoginStore({storage, keychain}), storage, keychain};
}

test('loads empty values when nothing is persisted', async () => {
  await expect(setup().store.load()).resolves.toEqual({serverUrl: null, credentials: null});
});

test('persists the server URL independently from credentials', async () => {
  const {store} = setup();
  await store.saveServerUrl('https://vault.example.com');
  await expect(store.loadServerUrl()).resolves.toBe('https://vault.example.com');
});

test('persists and loads username and password through the secure adapter', async () => {
  const {store} = setup();
  await store.saveCredentials({username: 'alice', password: 'correct horse'});
  await expect(store.loadCredentials()).resolves.toEqual({username: 'alice', password: 'correct horse'});
});

test('clearCredentials removes both username and password but not the server URL', async () => {
  const {store} = setup();
  await store.saveServerUrl('https://vault.example.com');
  await store.saveCredentials({username: 'alice', password: 'secret'});
  await store.clearCredentials();
  await expect(store.load()).resolves.toEqual({serverUrl: 'https://vault.example.com', credentials: null});
});

test('treats a keychain miss as no remembered credentials', async () => {
  const {store, keychain} = setup();
  (keychain.get as jest.Mock).mockResolvedValueOnce(null);
  await expect(store.loadCredentials()).resolves.toBeNull();
});

test('propagates storage errors so the UI can show a warning', async () => {
  const {store, storage} = setup();
  (storage.setItem as jest.Mock).mockRejectedValueOnce(new Error('storage unavailable'));
  await expect(store.saveServerUrl('https://vault.example.com')).rejects.toThrow('storage unavailable');
});
