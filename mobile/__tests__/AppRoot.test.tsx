import React from 'react';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {AppState, FlatList, Modal} from 'react-native';
import {AppRoot} from '../src/AppRoot';
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

async function login() {
  const store: RememberedLoginStore = {
    load: async () => ({serverUrl: 'https://vault.example.com', credentials: null}),
    loadServerUrl: async () => null,
    loadCredentials: async () => null,
    saveServerUrl: async () => {},
    saveCredentials: async () => {},
    clearCredentials: async () => {},
  };
  await act(async () => {
    tree = renderer.create(<AppRoot rememberedLoginStore={store} />);
  });
  act(() => {
    tree.root.findByProps({testID: 'username-input'}).props.onChangeText('alice');
    tree.root.findByProps({testID: 'password-input'}).props.onChangeText('dummy-password');
  });
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
  expect(unlock).not.toHaveBeenCalled();
  expect(tree.root.findByProps({testID: 'password-input'}).props.value).toBe('');
  await act(async () => { tree.root.findByProps({testID: 'biometric-sign-in'}).props.onPress(); });
  expect(tree.root.findByType(VaultScreen)).toBeTruthy();
  expect(store.saveCredentials).not.toHaveBeenCalled();
  expect(requests.mock.calls.some(([url, init]) => url.endsWith('/auth/login') &&
    JSON.parse(init.body).password === 'protected-secret')).toBe(true);
  await act(async () => { tree.root.findByType(VaultScreen).props.onSignOut(); });
  expect(clear).toHaveBeenCalledTimes(1);
  expect(tree.root.findAllByProps({testID: 'biometric-sign-in'})).toHaveLength(0);
  expect(tree.root.findByProps({testID: 'password-input'}).props.value).toBe('');
});
