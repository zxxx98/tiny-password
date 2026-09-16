import React from 'react';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {Text} from 'react-native';
import {EntryEditorScreen} from '../src/screens/EntryEditorScreen';
import type {TinyPasswordApi} from '../src/api/client';
import type {SessionController} from '../src/auth/session';
import type {ItemDetail} from '../src/api/types';

jest.mock('@react-native-clipboard/clipboard', () => ({
  __esModule: true,
  default: {setString: jest.fn()},
}));

jest.mock('react-native-safe-area-context', () => ({
  useSafeAreaInsets: () => ({top: 0, right: 0, bottom: 0, left: 0}),
}));

test('renders a readable shared item as a read-only detail', async () => {
  const sharedDetail: ItemDetail = {
    id: 'shared-1',
    title: 'Shared GitHub',
    item_type: 'login',
    vault_scope: 'shared',
    owner_id: 'owner-1',
    creator_id: 'owner-1',
    favorite: false,
    revision: 1,
    created_at: '2026-09-16T00:00:00Z',
    updated_at: '2026-09-16T00:00:00Z',
    payload: {
      name: 'Shared GitHub',
      username: 'shared@example.com',
      password: 'shared-secret-1',
      notes: 'read-only shared credential',
    },
  };
  const api = {
    getItem: jest.fn(async () => ({
      kind: 'success' as const,
      status: 200,
      data: sharedDetail,
    })),
  } as unknown as TinyPasswordApi;
  const session = {csrfToken: 'csrf-1', currentPrincipal: null} as unknown as SessionController;
  let tree!: ReactTestRenderer;

  await act(async () => {
    tree = renderer.create(
      <EntryEditorScreen
        session={session}
        api={api}
        route={{mode: 'detail', itemId: 'shared-1'}}
        onClose={() => undefined}
        onActivity={() => undefined}
      />,
    );
  });

  expect(api.getItem).toHaveBeenCalledWith('shared-1');
  expect(tree.root.findAllByType(Text).some(node => node.props.children === 'SHARED · READ ONLY · REV 1')).toBe(true);
  expect(tree.root.findAllByProps({testID: 'view-title'}).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({testID: 'view-username'}).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({testID: 'view-password'}).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({testID: 'editor-edit'})).toHaveLength(0);
  expect(tree.root.findAllByProps({testID: 'move-to-trash'})).toHaveLength(0);
});
