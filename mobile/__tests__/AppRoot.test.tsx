import React from 'react';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {AppState, FlatList, Modal} from 'react-native';
import {AppRoot} from '../src/AppRoot';
import {SettingsScreen} from '../src/screens/SettingsScreen';
import {CheckUpdateButton} from '../src/components/CheckUpdateButton';
import {NewsprintHeader} from '../src/components/NewsprintHeader';
import {VaultScreen} from '../src/screens/VaultScreen';
import type {RememberedLoginStore} from '../src/auth/rememberedLogin';
import type {FetchInit} from '../src/api/client';
import {sensitiveClipboard} from '../src/privacy/clipboard';

jest.mock('react-native-safe-area-context', () => ({
  SafeAreaProvider: ({children}: {children: React.ReactNode}) => children,
  useSafeAreaInsets: () => ({top: 0, right: 0, bottom: 24, left: 0}),
}));
jest.mock('@react-native-clipboard/clipboard', () => ({
  __esModule: true,
  default: {setString: jest.fn(), getString: jest.fn(async () => '')},
}));
jest.mock('../src/auth/nativeRememberedLoginStore', () => ({nativeRememberedLoginStore: {}}));

let tree: ReactTestRenderer;
let changeAppState: (state: 'active' | 'background' | 'inactive') => void;
let sessionResponse: () => Promise<unknown>;
let requests: jest.Mock;
const realFetch = global.fetch;
const principal = {
  user_id: 'user',
  username: 'alice',
  role: 'member',
  must_change_password: false,
  idle_timeout_minutes: 5,
};
const item = {
  id: 'item',
  item_type: 'login',
  vault_scope: 'personal',
  title: 'GitHub',
  revision: 1,
  payload: {name: 'GitHub', username: 'alice', password: 'dummy-secret'},
};

beforeEach(() => {
  jest.useFakeTimers();
  Object.defineProperty(AppState, 'currentState', {value: 'active', writable: true, configurable: true});
  jest.spyOn(AppState, 'addEventListener').mockImplementation((_event, callback) => {
    changeAppState = state => {
      AppState.currentState = state;
      callback(state);
    };
    return {remove: jest.fn()};
  });
  const response = (body: unknown) => ({
    status: 200,
    headers: {get: () => null},
    text: async () => JSON.stringify(body),
  });
  sessionResponse = async () => response({user: principal, csrf_token: 'csrf'});
  requests = jest.fn(async (url: string, init: FetchInit) => {
    if (url.endsWith('/auth/session')) {
      return sessionResponse();
    }
    if (url.endsWith('/auth/login')) {
      return response({user: principal, csrf_token: 'csrf'});
    }
    if (url.endsWith('/csrf')) {
      return response({csrf_token: 'preauth'});
    }
    if (url.endsWith('/items/item')) {
      return response(item);
    }
    if (url.endsWith('/generators/password')) {
      return response({value: 'generated-secret'});
    }
    if (url.endsWith('/items/search')) {
      const {cursor} = JSON.parse(init.body!);
      return response({
        items: [cursor ? {...item, id: 'second', title: 'GitLab'} : item],
        next_cursor: cursor ? null : 'next',
      });
    }
    return response({items: [item], next_cursor: null});
  });
  global.fetch = requests;
});

afterEach(async () => {
  if (tree) {
    act(() => tree.unmount());
  }
  await sensitiveClipboard.clear();
  global.fetch = realFetch;
  jest.restoreAllMocks();
  jest.useRealTimers();
});

async function login(authenticate = true, overrides: Partial<RememberedLoginStore> = {}) {
  const store: RememberedLoginStore = {
    load: async () => ({serverUrl: 'https://vault.example.com', credentials: null}),
    loadServerUrl: async () => null,
    loadCredentials: async () => null,
    saveServerUrl: async () => {},
    saveCredentials: async () => {},
    clearCredentials: async () => {},
    ...overrides,
  };
  await act(async () => {
    tree = renderer.create(<AppRoot rememberedLoginStore={store} />);
  });
  act(() => {
    tree.root.findByProps({testID: 'username-input'}).props.onChangeText('alice');
    tree.root.findByProps({testID: 'password-input'}).props.onChangeText('dummy-password');
  });
  if (!authenticate) { return; }
  await act(async () => {
    await tree.root.findByProps({testID: 'sign-in-submit'}).props.onPress();
  });
  expect(tree.root.findByType(VaultScreen)).toBeTruthy();
}

test('returning from a detail preserves the search, loaded pages and native list instance', async () => {
  await login();
  act(() => tree.root.findByProps({testID: 'vault-search'}).props.onChangeText('Git'));
  await act(async () => {
    jest.advanceTimersByTime(300);
  });
  await act(async () => {
    tree.root.findByProps({testID: 'load-more'}).props.onPress();
  });
  const list = tree.root.findByType(FlatList);
  expect(list.props.data).toHaveLength(2);
  await act(async () => {
    tree.root.findByProps({testID: 'vault-item-item'}).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({testID: 'editor-header'}).props.onBack();
  });
  expect(tree.root.findByType(FlatList)).toBe(list);
  expect(list.props.data).toHaveLength(2);
  expect(tree.root.findByProps({testID: 'vault-search'}).props.value).toBe('Git');
});

test('backgrounding hides native dialogs and stays masked when resume validation fails', async () => {
  await login();
  await act(async () => {
    tree.root.findByProps({testID: 'vault-item-item'}).props.onPress();
  });
  act(() => tree.root.findByProps({testID: 'editor-edit'}).props.onPress());
  await act(async () => {
    tree.root.findByProps({testID: 'open-generator', label: 'GENERATE'}).props.onPress();
  });
  expect(tree.root.findAllByType(Modal).some(modal => modal.props.visible)).toBe(true);
  act(() => changeAppState('background'));
  expect(tree.root.findAllByType(Modal).every(modal => !modal.props.visible)).toBe(true);
  expect(tree.root.findByProps({testID: 'resume-mask'})).toBeTruthy();
  sessionResponse = async () => {
    throw new Error('offline');
  };
  await act(async () => changeAppState('active'));
  expect(tree.root.findByProps({testID: 'resume-mask'})).toBeTruthy();
  expect(tree.root.findAllByType(Modal).every(modal => !modal.props.visible)).toBe(true);
});

test('a late resume result cannot reveal content after another background transition', async () => {
  await login();
  let resolveSession!: (value: unknown) => void;
  sessionResponse = () =>
    new Promise(resolve => {
      resolveSession = resolve;
    });
  act(() => changeAppState('background'));
  act(() => changeAppState('active'));
  act(() => changeAppState('background'));
  await act(async () => {
    resolveSession({
      status: 200,
      headers: {get: () => null},
      text: async () => JSON.stringify({user: principal, csrf_token: 'csrf'}),
    });
  });
  expect(tree.root.findByProps({testID: 'resume-mask'})).toBeTruthy();
});

test('cold-start biometric login reaches the vault and sign-out removes the biometric entry point', async () => {
  const unlock = jest.fn(async () => ({username: 'alice', password: 'protected-secret'}));
  const clear = jest.fn(async () => {});
  const store: RememberedLoginStore = {
    load: async () => ({serverUrl: 'https://vault.example.com', credentials: null,
      biometricAvailable: true, biometricEnabled: true}),
    loadServerUrl: async () => 'https://vault.example.com',
    loadCredentials: async () => null,
    saveServerUrl: async () => {},
    saveCredentials: jest.fn(async () => {}),
    clearCredentials: clear,
    unlockCredentials: unlock,
  };
  await act(async () => { tree = renderer.create(<AppRoot rememberedLoginStore={store} />); });
  expect(unlock).toHaveBeenCalledTimes(1);
  expect(tree.root.findByType(VaultScreen)).toBeTruthy();
  expect(store.saveCredentials).not.toHaveBeenCalled();
  expect(requests.mock.calls.some(([url, init]) => url.endsWith('/auth/login') &&
    JSON.parse(init.body).password === 'protected-secret')).toBe(true);
  await act(async () => { tree.root.findByType(VaultScreen).props.onSignOut(); });
  expect(clear).toHaveBeenCalledTimes(1);
  expect(tree.root.findAllByProps({testID: 'biometric-sign-in'})).toHaveLength(0);
  expect(tree.root.findByProps({testID: 'password-input'}).props.value).toBe('');
});


test('settings is the only update UI and returning preserves login inputs', async () => {
  await login(false);
  expect(tree.root.findAllByType(CheckUpdateButton)).toHaveLength(0);
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  const settings = tree.root.findByType(SettingsScreen);
  expect(settings.findAllByType(CheckUpdateButton)).toHaveLength(1);
  // Android's hardware back is delivered through the full-screen Modal.
  act(() => settings.findAllByType(Modal)[0].props.onRequestClose());
  expect(tree.root.findAllByType(SettingsScreen)).toHaveLength(0);
  expect(tree.root.findByProps({testID: 'username-input'}).props.value).toBe('alice');
  expect(tree.root.findByProps({testID: 'password-input'}).props.value).toBe('dummy-password');
});

test('settings preserves the vault list and hides while the app is in the background', async () => {
  await login();
  act(() => tree.root.findByProps({testID: 'vault-search'}).props.onChangeText('Git'));
  await act(async () => { jest.advanceTimersByTime(300); });
  const list = tree.root.findByType(FlatList).instance;
  expect(tree.root.findAllByType(CheckUpdateButton)).toHaveLength(0);
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  expect(tree.root.findAllByType(CheckUpdateButton)).toHaveLength(1);
  act(() => changeAppState('background'));
  expect(tree.root.findByType(SettingsScreen).findAllByType(Modal)[0].props.visible).toBe(false);
  await act(async () => changeAppState('active'));
  const settings = tree.root.findByType(SettingsScreen);
  expect(settings.findAllByType(Modal)[0].props.visible).toBe(true);
  act(() => settings.findByType(NewsprintHeader).props.onBack());
  expect(tree.root.findAllByType(SettingsScreen)).toHaveLength(0);
  expect(tree.root.findByProps({testID: 'vault-search'}).props.value).toBe('Git');
  expect(tree.root.findByType(FlatList).instance).toBe(list);
});


function biometricStore(enabled = false): RememberedLoginStore {
  return {
    load: async () => ({serverUrl: 'https://vault.example.com', credentials: null,
      biometricAvailable: true, biometricEnabled: enabled}),
    loadServerUrl: async () => 'https://vault.example.com',
    loadCredentials: async () => null,
    saveServerUrl: jest.fn(async () => {}),
    saveCredentials: jest.fn(async () => {}),
    clearCredentials: jest.fn(async () => {}),
    unlockCredentials: jest.fn(async () => ({username: 'alice', password: 'protected-secret'})),
  };
}

test('canceled startup verification prompts only once, sends no login, and allows password fallback', async () => {
  const store = biometricStore(true);
  (store.unlockCredentials as jest.Mock).mockRejectedValue(new Error('Canceled'));
  await act(async () => { tree = renderer.create(<AppRoot rememberedLoginStore={store} />); });
  expect(store.unlockCredentials).toHaveBeenCalledTimes(1);
  expect(requests.mock.calls.filter(([url]) => url.endsWith('/auth/login'))).toHaveLength(0);
  act(() => changeAppState('background'));
  await act(async () => changeAppState('active'));
  expect(store.unlockCredentials).toHaveBeenCalledTimes(1);
  act(() => {
    tree.root.findByProps({testID: 'username-input'}).props.onChangeText('alice');
    tree.root.findByProps({testID: 'password-input'}).props.onChangeText('correct-password');
  });
  await act(async () => tree.root.findByProps({testID: 'sign-in-submit'}).props.onPress());
  expect(tree.root.findByType(VaultScreen)).toBeTruthy();
});

test('settings opt-in before login enrolls only after password login succeeds', async () => {
  const store = biometricStore();
  await login(false, store);
  expect(tree.root.findAllByProps({testID: 'enable-biometric'})).toHaveLength(0);
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  await act(async () => tree.root.findByProps({testID: 'enable-biometric'}).props.onPress());
  expect(store.saveCredentials).not.toHaveBeenCalled();
  expect(tree.root.findByProps({testID: 'enable-biometric'}).props.accessibilityState.checked).toBe(true);
  act(() => tree.root.findByType(SettingsScreen).props.onClose());
  await act(async () => tree.root.findByProps({testID: 'sign-in-submit'}).props.onPress());
  expect(store.saveCredentials).toHaveBeenCalledWith({username: 'alice', password: 'dummy-password'}, true);
  expect(tree.root.findByType(VaultScreen)).toBeTruthy();
});

test('authenticated enrollment verifies the password, revokes the temporary session and preserves the vault', async () => {
  const store = biometricStore();
  await login(true, store);
  const list = tree.root.findByType(FlatList).instance;
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  act(() => tree.root.findByProps({testID: 'enable-biometric'}).props.onPress());
  expect(store.saveCredentials).not.toHaveBeenCalled();
  act(() => tree.root.findByProps({testID: 'biometric-password'}).props.onChangeText('confirmed-password'));
  await act(async () => tree.root.findByProps({testID: 'confirm-biometric'}).props.onPress());
  expect(store.saveCredentials).toHaveBeenCalledWith({username: 'alice', password: 'confirmed-password'}, true);
  expect(requests.mock.calls.filter(([url]) => url.endsWith('/auth/logout'))).toHaveLength(1);
  expect(tree.root.findByProps({testID: 'enable-biometric'}).props.accessibilityState.checked).toBe(true);
  expect(tree.root.findByType(FlatList).instance).toBe(list);
});

test('incorrect enrollment password does not save credentials or replace the active session', async () => {
  const store = biometricStore();
  await login(true, store);
  const original = requests.getMockImplementation()!;
  requests.mockImplementation((url: string, init: FetchInit) => url.endsWith('/auth/login')
    ? Promise.resolve({status: 401, headers: {get: () => null}, text: async () => JSON.stringify({error: {code: 'INVALID_CREDENTIALS'}})})
    : original(url, init));
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  act(() => tree.root.findByProps({testID: 'enable-biometric'}).props.onPress());
  act(() => tree.root.findByProps({testID: 'biometric-password'}).props.onChangeText('wrong-password'));
  await act(async () => tree.root.findByProps({testID: 'confirm-biometric'}).props.onPress());
  expect(store.saveCredentials).not.toHaveBeenCalled();
  expect(tree.root.findByProps({testID: 'enable-biometric'}).props.accessibilityState.checked).toBe(false);
  expect(tree.root.findByProps({testID: 'biometric-password'}).props.value).toBe('');
  expect(tree.root.findByType(VaultScreen)).toBeTruthy();
});

test('disabling biometrics removes saved credentials and does not prompt again on resume', async () => {
  const store = biometricStore(true);
  await act(async () => { tree = renderer.create(<AppRoot rememberedLoginStore={store} />); });
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  await act(async () => tree.root.findByProps({testID: 'enable-biometric'}).props.onPress());
  expect(store.clearCredentials).toHaveBeenCalledTimes(1);
  expect(tree.root.findByProps({testID: 'enable-biometric'}).props.accessibilityState.checked).toBe(false);
  act(() => changeAppState('background'));
  await act(async () => changeAppState('active'));
  expect(store.unlockCredentials).toHaveBeenCalledTimes(1);
});

test('an expired session during enrollment cannot save late verified credentials', async () => {
  const store = biometricStore();
  await login(true, store);
  const original = requests.getMockImplementation()!;
  let finish!: () => void;
  requests.mockImplementation((url: string, init: FetchInit) => url.endsWith('/auth/login')
    ? new Promise(resolve => { finish = () => resolve(original(url, init)); }) : original(url, init));
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  act(() => tree.root.findByProps({testID: 'enable-biometric'}).props.onPress());
  act(() => tree.root.findByProps({testID: 'biometric-password'}).props.onChangeText('secret'));
  await act(async () => tree.root.findByProps({testID: 'confirm-biometric'}).props.onPress());
  sessionResponse = async () => ({status: 401, headers: {get: () => null}, text: async () => '{}'});
  act(() => changeAppState('background'));
  await act(async () => changeAppState('active'));
  await act(async () => finish());
  expect(store.saveCredentials).not.toHaveBeenCalled();
  expect(tree.root.findAllByType(SettingsScreen)).toHaveLength(0);
  expect(tree.root.findByProps({testID: 'password-input'})).toBeTruthy();
});

test('credential deletion failure keeps the biometric setting enabled', async () => {
  const store = biometricStore(true);
  (store.clearCredentials as jest.Mock).mockRejectedValue(new Error('无法清除本地凭据'));
  await act(async () => { tree = renderer.create(<AppRoot rememberedLoginStore={store} />); });
  act(() => tree.root.findByProps({testID: 'open-settings'}).props.onPress());
  await act(async () => tree.root.findByProps({testID: 'enable-biometric'}).props.onPress());
  expect(tree.root.findByProps({testID: 'enable-biometric'}).props.accessibilityState.checked).toBe(true);
});
