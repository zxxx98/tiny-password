import AsyncStorage from '@react-native-async-storage/async-storage';
import {
  ACCESSIBLE,
  ACCESS_CONTROL,
  STORAGE_TYPE,
  getSupportedBiometryType,
  hasGenericPassword,
  getGenericPassword,
  resetGenericPassword,
  setGenericPassword,
} from 'react-native-keychain';
import {createRememberedLoginStore, type KeychainLike, type RememberedLoginStore} from './rememberedLogin';

const KEYCHAIN_SERVICE = 'com.tinypassword.remembered-login';

const keychain: KeychainLike = {
  async get() {
    const result = await getGenericPassword({service: KEYCHAIN_SERVICE});
    if (!result) {
      return null;
    }
    return {username: result.username, password: result.password};
  },
  async set(username, password) {
    await setGenericPassword(username, password, {
      service: KEYCHAIN_SERVICE,
      accessible: ACCESSIBLE.WHEN_UNLOCKED,
    });
  },
  async clear() {
    await resetGenericPassword({service: KEYCHAIN_SERVICE});
  },
};

const ordinaryStore = createRememberedLoginStore({
  storage: AsyncStorage,
  keychain,
});

const BIOMETRIC_SERVICE = 'com.tinypassword.biometric-login';
const prompt = {title: '验证身份以登录 Tiny Password', cancel: '使用密码'};
// Serialize writes so a pending enrollment cannot restore credentials after logout.
let pending: Promise<unknown> = Promise.resolve();
function serialize<T>(operation: () => Promise<T>): Promise<T> {
  const result = pending.then(operation);
  pending = result.catch(() => undefined);
  return result;
}

export const nativeRememberedLoginStore: RememberedLoginStore = {
  ...ordinaryStore,
  async load() {
    await pending;
    const serverUrl = await ordinaryStore.loadServerUrl();
    const biometricEnabled = await hasGenericPassword({service: BIOMETRIC_SERVICE});
    const biometricAvailable = Boolean(await getSupportedBiometryType().catch(() => null));
    // Never read biometric secrets or display a system prompt during startup.
    const credentials = biometricEnabled ? null : await ordinaryStore.loadCredentials();
    return {serverUrl, credentials, biometricAvailable, biometricEnabled};
  },
  async loadCredentials() {
    await pending;
    return await hasGenericPassword({service: BIOMETRIC_SERVICE})
      ? null : ordinaryStore.loadCredentials();
  },
  saveServerUrl(serverUrl) {
    return serialize(() => ordinaryStore.saveServerUrl(serverUrl));
  },
  saveCredentials(credentials, biometric = false) {
    return serialize(async () => {
      if (!biometric) {
        await resetGenericPassword({service: BIOMETRIC_SERVICE});
        await ordinaryStore.saveCredentials(credentials);
        return;
      }
      // Remove the unprotected copy even if enrollment subsequently fails.
      await ordinaryStore.clearCredentials();
      await resetGenericPassword({service: BIOMETRIC_SERVICE});
      const serverUrl = await ordinaryStore.loadServerUrl();
      if (!serverUrl || !await getSupportedBiometryType()) {
        throw new Error('Biometric authentication unavailable');
      }
      const saved = await setGenericPassword(credentials.username, JSON.stringify({
        serverUrl, password: credentials.password,
      }), {
        service: BIOMETRIC_SERVICE,
        accessible: ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
        accessControl: ACCESS_CONTROL.BIOMETRY_CURRENT_SET,
        storage: STORAGE_TYPE.AES_GCM,
        authenticationPrompt: prompt,
      });
      if (!saved) {
        throw new Error('Biometric enrollment failed');
      }
    });
  },
  async unlockCredentials(serverUrl) {
    await pending;
    if (await ordinaryStore.loadServerUrl() !== serverUrl) {
      return null;
    }
    const result = await getGenericPassword({
      service: BIOMETRIC_SERVICE,
      accessControl: ACCESS_CONTROL.BIOMETRY_CURRENT_SET,
      authenticationPrompt: prompt,
    });
    if (!result) {
      return null;
    }
    const saved = JSON.parse(result.password);
    if (saved.serverUrl !== serverUrl || typeof saved.password !== 'string') {
      return null;
    }
    return {username: result.username, password: saved.password};
  },
  clearCredentials() {
    return serialize(async () => {
      const results = await Promise.allSettled([
        ordinaryStore.clearCredentials(),
        resetGenericPassword({service: BIOMETRIC_SERVICE}),
      ]);
      if (results.some(result => result.status === 'rejected')) {
        throw new Error('Credential removal failed');
      }
    });
  },
};
