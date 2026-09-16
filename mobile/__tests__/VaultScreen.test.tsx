import React from 'react';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {Text} from 'react-native';
import {VaultScreen} from '../src/screens/VaultScreen';
import type {TinyPasswordApi} from '../src/api/client';
import type {SessionController} from '../src/auth/session';

jest.mock('react-native-safe-area-context', () => ({
  useSafeAreaInsets: () => ({top: 0, right: 0, bottom: 0, left: 0}),
}));

test('labels the mixed personal and shared list as the readable vault', async () => {
  const api = {
    listItems: jest.fn(async () => ({
      kind: 'success' as const,
      status: 200,
      data: {items: [], next_cursor: null},
    })),
  } as unknown as TinyPasswordApi;
  const session = {csrfToken: null} as unknown as SessionController;
  let tree!: ReactTestRenderer;

  await act(async () => {
    tree = renderer.create(
      <VaultScreen
        session={session}
        api={api}
        refreshKey={0}
        notice={null}
        onOpenEntry={() => undefined}
        onAddEntry={() => undefined}
        onOpenGenerator={() => undefined}
        onSignOut={() => undefined}
        onActivity={() => undefined}
      />,
    );
  });

  expect(tree.root.findAllByType(Text).some(node => node.props.children === 'READABLE VAULT')).toBe(true);
});
