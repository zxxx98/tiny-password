import AsyncStorage from '@react-native-async-storage/async-storage';
import {
  ACCESSIBLE,
  getGenericPassword,
  resetGenericPassword,
  setGenericPassword,
} from 'react-native-keychain';
import {createRememberedLoginStore, type KeychainLike} from './rememberedLogin';

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

export const nativeRememberedLoginStore = createRememberedLoginStore({
  storage: AsyncStorage,
  keychain,
});
