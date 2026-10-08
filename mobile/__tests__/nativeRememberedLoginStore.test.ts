jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: {
    getItem: jest.fn(),
    setItem: jest.fn(),
    removeItem: jest.fn(),
  },
}));

jest.mock('react-native-keychain', () => ({
  __esModule: true,
  getGenericPassword: jest.fn(),
  hasGenericPassword: jest.fn(async () => false),
  setGenericPassword: jest.fn(),
  resetGenericPassword: jest.fn(),
  getSupportedBiometryType: jest.fn(async () => 'Fingerprint'),
  ACCESSIBLE: {WHEN_UNLOCKED: 'WHEN_UNLOCKED', WHEN_UNLOCKED_THIS_DEVICE_ONLY: 'DEVICE_ONLY'},
  ACCESS_CONTROL: {BIOMETRY_CURRENT_SET: 'CURRENT_SET'},
  STORAGE_TYPE: {AES_GCM: 'AES_GCM'},
}));

import {nativeRememberedLoginStore} from '../src/auth/nativeRememberedLoginStore';

const mockKeychain = jest.requireMock('react-native-keychain') as {
  getGenericPassword: jest.Mock;
  hasGenericPassword: jest.Mock;
  getSupportedBiometryType: jest.Mock;
  setGenericPassword: jest.Mock;
  resetGenericPassword: jest.Mock;
};

beforeEach(() => {
  jest.clearAllMocks();
  mockKeychain.getGenericPassword.mockResolvedValue(false);
  mockKeychain.hasGenericPassword.mockResolvedValue(false);
  mockKeychain.getSupportedBiometryType.mockResolvedValue('Fingerprint');
  jest.requireMock('@react-native-async-storage/async-storage').default.getItem.mockResolvedValue('https://vault.example.com');
  mockKeychain.setGenericPassword.mockResolvedValue({service: 'com.tinypassword.remembered-login'});
  mockKeychain.resetGenericPassword.mockResolvedValue(true);
});

test('loads no credentials when Android Keystore has no generic password', async () => {
  await expect(nativeRememberedLoginStore.loadCredentials()).resolves.toBeNull();
  expect(mockKeychain.getGenericPassword).toHaveBeenCalledWith({
    service: 'com.tinypassword.remembered-login',
  });
});

test('saves credentials with the app service and unlocked accessibility', async () => {
  await nativeRememberedLoginStore.saveCredentials({username: 'alice', password: 'secret'});
  expect(mockKeychain.setGenericPassword).toHaveBeenCalledWith('alice', 'secret', {
    service: 'com.tinypassword.remembered-login',
    accessible: 'WHEN_UNLOCKED',
  });
});

test('clears credentials from the secure store', async () => {
  await nativeRememberedLoginStore.clearCredentials();
  expect(mockKeychain.resetGenericPassword).toHaveBeenCalledWith({
    service: 'com.tinypassword.remembered-login',
  });
});

const biometricService = 'com.tinypassword.biometric-login';

test('startup discovers protected credentials without reading passwords or prompting', async () => {
  mockKeychain.hasGenericPassword.mockResolvedValue(true);
  await expect(nativeRememberedLoginStore.load()).resolves.toEqual({
    serverUrl: 'https://vault.example.com', credentials: null,
    biometricAvailable: true, biometricEnabled: true,
  });
  expect(mockKeychain.getGenericPassword).not.toHaveBeenCalled();
  await expect(nativeRememberedLoginStore.loadCredentials()).resolves.toBeNull();
});

test('enrollment removes the ordinary copy and binds protected credentials to the server', async () => {
  await nativeRememberedLoginStore.saveCredentials({username: 'alice', password: 'secret'}, true);
  expect(mockKeychain.resetGenericPassword).toHaveBeenCalledWith({service: 'com.tinypassword.remembered-login'});
  expect(mockKeychain.setGenericPassword).toHaveBeenCalledWith('alice',
    JSON.stringify({serverUrl: 'https://vault.example.com', password: 'secret'}), {
      service: biometricService, accessible: 'DEVICE_ONLY',
      accessControl: 'CURRENT_SET', storage: 'AES_GCM',
      authenticationPrompt: expect.objectContaining({cancel: '使用密码'}),
    });
});

test('unlock uses biometric access control and rejects credentials from another server', async () => {
  mockKeychain.getGenericPassword.mockResolvedValue({username: 'alice', password: JSON.stringify({
    serverUrl: 'https://vault.example.com', password: 'secret',
  })});
  await expect(nativeRememberedLoginStore.unlockCredentials!('https://vault.example.com'))
    .resolves.toEqual({username: 'alice', password: 'secret'});
  expect(mockKeychain.getGenericPassword).toHaveBeenCalledWith(expect.objectContaining({
    service: biometricService, accessControl: 'CURRENT_SET',
  }));
  mockKeychain.getGenericPassword.mockClear();
  await expect(nativeRememberedLoginStore.unlockCredentials!('https://other.example.com')).resolves.toBeNull();
  expect(mockKeychain.getGenericPassword).not.toHaveBeenCalled();
  mockKeychain.getGenericPassword.mockResolvedValue({username: 'alice', password: JSON.stringify({
    serverUrl: 'https://other.example.com', password: 'secret',
  })});
  await expect(nativeRememberedLoginStore.unlockCredentials!('https://vault.example.com')).resolves.toBeNull();
});

test('failed enrollment never falls back to unprotected password storage', async () => {
  mockKeychain.setGenericPassword.mockResolvedValue(false);
  await expect(nativeRememberedLoginStore.saveCredentials({username: 'alice', password: 'secret'}, true)).rejects.toThrow();
  expect(mockKeychain.setGenericPassword).toHaveBeenCalledTimes(1);
});

test('logout queued during enrollment deletes both copies after the save completes', async () => {
  let finish!: (value: unknown) => void;
  mockKeychain.setGenericPassword.mockReturnValue(new Promise(resolve => { finish = resolve; }));
  const saving = nativeRememberedLoginStore.saveCredentials({username: 'alice', password: 'secret'}, true);
  // Allow the serialized save to reach the native prompt.
  for (let i = 0; i < 10; i++) { await Promise.resolve(); }
  const clearing = nativeRememberedLoginStore.clearCredentials();
  finish({service: biometricService});
  await Promise.all([saving, clearing]);
  expect(mockKeychain.resetGenericPassword).toHaveBeenLastCalledWith({service: biometricService});
});
