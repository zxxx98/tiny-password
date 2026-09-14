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
  setGenericPassword: jest.fn(),
  resetGenericPassword: jest.fn(),
  ACCESSIBLE: {WHEN_UNLOCKED: 'WHEN_UNLOCKED'},
}));

import {nativeRememberedLoginStore} from '../src/auth/nativeRememberedLoginStore';

const mockKeychain = jest.requireMock('react-native-keychain') as {
  getGenericPassword: jest.Mock;
  setGenericPassword: jest.Mock;
  resetGenericPassword: jest.Mock;
};

beforeEach(() => {
  jest.clearAllMocks();
  mockKeychain.getGenericPassword.mockResolvedValue(false);
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
