import React from 'react';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {SignInScreen} from '../src/screens/SignInScreen';
import type {SessionController, SessionSnapshot} from '../src/auth/session';

jest.mock('react-native-safe-area-context', () => ({
  useSafeAreaInsets: () => ({top: 0, right: 0, bottom: 0, left: 0}),
}));

type FakeSession = {
  session: SessionController;
  setPhase: (phase: SessionSnapshot['phase']) => void;
};

function makeFakeSession(
  initialPhase: SessionSnapshot['phase'] = 'signed-out',
  loginMustChange = false,
): FakeSession {
  let snapshot: SessionSnapshot = {
    phase: initialPhase,
    principal: null,
    confirmError: null,
    serverUrl: null,
  };
  const listeners = new Set<() => void>();
  const emit = () => listeners.forEach(listener => listener());
  const setPhase = (phase: SessionSnapshot['phase']) => {
    snapshot = {
      ...snapshot,
      phase,
      principal: null,
    };
    emit();
  };

  const fake = {
    getSnapshot: jest.fn(() => snapshot),
    subscribe: jest.fn((listener: () => void) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    }),
    setServerUrl: jest.fn((url: string) => {
      snapshot = {...snapshot, serverUrl: url, phase: 'signed-out'};
      emit();
      return true;
    }),
    preflight: jest.fn(async () => true),
    login: jest.fn(async () => {
      setPhase(loginMustChange ? 'must-change' : 'authenticated');
      return {ok: true, error: null};
    }),
    changePassword: jest.fn(async () => ({ok: true, error: null})),
    confirmSession: jest.fn(async () => {
      setPhase('authenticated');
      return 'confirmed' as const;
    }),
    signOut: jest.fn(async () => ({notice: null})),
    getApi: jest.fn(() => null),
    touchActivity: jest.fn(async () => undefined),
  } as unknown as SessionController & {serverConfigured: boolean};

  Object.defineProperty(fake, 'serverConfigured', {
    get: () => snapshot.serverUrl !== null,
  });
  return {session: fake, setPhase};
}

function renderSignIn(options?: {
  session?: SessionController;
  initialServerUrl?: string;
  rememberedCredentials?: {username: string; password: string} | null;
  onRememberedCredentialsSaved?: jest.Mock;
  onRememberedCredentialsCleared?: jest.Mock;
}): ReactTestRenderer {
  const fake = options?.session ?? makeFakeSession().session;
  let tree!: ReactTestRenderer;
  act(() => {
    tree = renderer.create(
      <SignInScreen
        session={fake}
        initialServerUrl={options?.initialServerUrl ?? ''}
        rememberedCredentials={options?.rememberedCredentials ?? null}
        onRememberedCredentialsSaved={options?.onRememberedCredentialsSaved}
        onRememberedCredentialsCleared={options?.onRememberedCredentialsCleared}
        onAuthenticated={jest.fn()}
        onActivity={jest.fn()}
      />,
    );
  });
  return tree;
}

test('uses remembered server and credentials as the initial sign-in values', () => {
  const tree = renderSignIn({
    initialServerUrl: 'https://vault.example.com',
    rememberedCredentials: {username: 'alice', password: 'secret'},
  });

  act(() => {
    tree.root.findByProps({accessibilityLabel: '配置服务器地址'}).props.onPress();
  });
  expect(tree.root.findByProps({testID: 'server-input'}).props.value).toBe('https://vault.example.com');
  expect(tree.root.findByProps({testID: 'username-input'}).props.value).toBe('alice');
  expect(tree.root.findByProps({testID: 'password-input'}).props.value).toBe('secret');
  expect(tree.root.findByProps({testID: 'remember-password'}).props.accessibilityState).toEqual({checked: true});
});

test('unchecking remember password immediately clears persisted credentials', async () => {
  const onClear = jest.fn();
  const tree = renderSignIn({
    rememberedCredentials: {username: 'alice', password: 'secret'},
    onRememberedCredentialsCleared: onClear,
  });

  await act(async () => {
    tree.root.findByProps({testID: 'remember-password'}).props.onPress();
  });
  expect(tree.root.findByProps({testID: 'remember-password'}).props.accessibilityState).toEqual({checked: false});

  expect(onClear).toHaveBeenCalledTimes(1);
});

test('switching servers clears remembered credentials and the password field', async () => {
  const onClear = jest.fn();
  const tree = renderSignIn({
    initialServerUrl: 'https://old.example.com',
    rememberedCredentials: {username: 'alice', password: 'secret'},
    onRememberedCredentialsCleared: onClear,
  });

  await act(async () => {
    tree.root.findByProps({accessibilityLabel: '配置服务器地址'}).props.onPress();
  });
  await act(async () => {
    tree.root.findByProps({testID: 'server-input'}).props.onChangeText('https://new.example.com');
  });
  await act(async () => {
    await tree.root.findByProps({testID: 'apply-server'}).props.onPress();
  });

  expect(onClear).toHaveBeenCalledTimes(1);
  expect(tree.root.findByProps({testID: 'password-input'}).props.value).toBe('');
});

test('saves credentials only after an authenticated login', async () => {
  const onSave = jest.fn();
  const fake = makeFakeSession();
  const tree = renderSignIn({
    session: fake.session,
    initialServerUrl: 'https://vault.example.com',
    onRememberedCredentialsSaved: onSave,
  });

  await act(async () => {
    tree.root.findByProps({testID: 'username-input'}).props.onChangeText('alice');
    tree.root.findByProps({testID: 'password-input'}).props.onChangeText('secret');
    tree.root.findByProps({testID: 'remember-password'}).props.onPress();
  });
  expect(tree.root.findByProps({testID: 'remember-password'}).props.accessibilityState).toEqual({checked: true});
  await act(async () => {
    await tree.root.findByProps({testID: 'sign-in-submit'}).props.onPress();
  });

  expect(onSave).toHaveBeenCalledWith({username: 'alice', password: 'secret'});
});

test('saves the new password after confirmed forced password change', async () => {
  const onSave = jest.fn();
  const fake = makeFakeSession('signed-out', true);
  const tree = renderSignIn({
    session: fake.session,
    initialServerUrl: 'https://vault.example.com',
    onRememberedCredentialsSaved: onSave,
  });

  await act(async () => {
    tree.root.findByProps({testID: 'username-input'}).props.onChangeText('alice');
    tree.root.findByProps({testID: 'password-input'}).props.onChangeText('old-secret');
    tree.root.findByProps({testID: 'remember-password'}).props.onPress();
  });
  expect(tree.root.findByProps({testID: 'remember-password'}).props.accessibilityState).toEqual({checked: true});
  await act(async () => {
    await tree.root.findByProps({testID: 'sign-in-submit'}).props.onPress();
  });
  expect(onSave).not.toHaveBeenCalled();

  await act(async () => {
    tree.root.findByProps({accessibilityLabel: '当前密码'}).props.onChangeText('old-secret');
    tree.root.findByProps({accessibilityLabel: '新密码'}).props.onChangeText('new-secret-123');
    tree.root.findByProps({accessibilityLabel: '确认新密码'}).props.onChangeText('new-secret-123');
  });
  await act(async () => {
    await tree.root.findByProps({testID: 'change-password-submit'}).props.onPress();
  });

  expect((fake.session.changePassword as jest.Mock)).toHaveBeenCalledWith('old-secret', 'new-secret-123');
  expect(onSave).toHaveBeenCalledTimes(1);
  expect(onSave).toHaveBeenCalledWith({username: 'alice', password: 'new-secret-123'});
});

test('signing out from forced password change clears the remembered-password choice', async () => {
  const onClear = jest.fn();
  const fake = makeFakeSession('signed-out', true);
  const tree = renderSignIn({
    session: fake.session,
    initialServerUrl: 'https://vault.example.com',
    rememberedCredentials: {username: 'alice', password: 'old-secret'},
    onRememberedCredentialsCleared: onClear,
  });

  await act(async () => {
    tree.root.findByProps({testID: 'sign-in-submit'}).props.onPress();
  });
  expect(tree.root.findByProps({testID: 'change-password-signout'})).toBeTruthy();

  await act(async () => {
    tree.root.findByProps({testID: 'change-password-signout'}).props.onPress();
  });
  const signOutButtons = tree.root.findAllByProps({accessibilityLabel: 'SIGN OUT'});
  const confirmButton = signOutButtons.find(
    button => button.props.testID !== 'change-password-signout' && typeof button.props.onPress === 'function',
  );
  expect(confirmButton).toBeDefined();
  await act(async () => {
    confirmButton!.props.onPress();
  });

  expect(onClear).toHaveBeenCalledTimes(1);
  expect(tree.root.findByProps({testID: 'remember-password'}).props.accessibilityState).toEqual({checked: false});
});
