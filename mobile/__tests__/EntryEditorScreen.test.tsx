import React from 'react';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {Text} from 'react-native';
import {sensitiveClipboard} from '../src/privacy/clipboard';
import Clipboard from '@react-native-clipboard/clipboard';
import {EntryEditorScreen} from '../src/screens/EntryEditorScreen';
import type {TinyPasswordApi} from '../src/api/client';
import type {SessionController} from '../src/auth/session';
import type {ItemDetail} from '../src/api/types';
import {ConfirmDialog} from '../src/components/ConfirmDialog';

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
    tree = createTree(
      <EntryEditorScreen
        session={session}
        api={api}
        route={{mode: 'detail', itemId: 'shared-1'}}
        onClose={() => undefined}
        onActivity={() => undefined}
      />,
    );
  });

  expect(api.getItem).toHaveBeenCalledWith('shared-1', expect.anything());
  expect(tree.root.findAllByType(Text).some(node => node.props.children === 'SHARED · READ ONLY · REV 1')).toBe(true);
  expect(tree.root.findAllByProps({testID: 'view-title'}).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({testID: 'view-username'}).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({testID: 'view-password'}).length).toBeGreaterThan(0);
  expect(tree.root.findAllByProps({testID: 'editor-edit'})).toHaveLength(0);
  expect(tree.root.findAllByProps({testID: 'move-to-trash'})).toHaveLength(0);
});

test('copies the whole identity address block in one tap', async () => {
  const identityDetail: ItemDetail = {
    id: 'identity-1',
    title: '张伟',
    item_type: 'identity',
    vault_scope: 'personal',
    owner_id: 'owner-1',
    creator_id: 'owner-1',
    favorite: false,
    revision: 3,
    created_at: '2026-09-16T00:00:00Z',
    updated_at: '2026-09-16T00:00:00Z',
    payload: {
      name: '张伟',
      full_name: '张伟',
      phone: '13800000000',
      country: '中国',
      state: '北京市',
      city: '北京市',
      district: '海淀区',
      address_line: '中关村大街 1 号',
      postal_code: '100000',
    },
  };
  const api = {
    getItem: jest.fn(async () => ({kind: 'success' as const, status: 200, data: identityDetail})),
  } as unknown as TinyPasswordApi;
  const session = {csrfToken: 'csrf-1', currentPrincipal: null} as unknown as SessionController;
  let tree!: ReactTestRenderer;

  await act(async () => {
    tree = createTree(
      <EntryEditorScreen
        session={session}
        api={api}
        route={{mode: 'detail', itemId: 'identity-1'}}
        onClose={() => undefined}
        onActivity={() => undefined}
      />,
    );
  });

  const button = tree.root
    .findAllByProps({testID: 'copy-identity-address'})
    .find(node => typeof node.props.onPress === 'function')!;
  await act(async () => {
    button.props.onPress();
  });

  expect(Clipboard.setString).toHaveBeenCalledWith(
    ['姓名：张伟', '地址：北京市 北京市 海淀区 中关村大街 1 号 100000', '联系电话：13800000000'].join('\n'),
  );
  expect(tree.root.findAllByType(Text).some(node => node.props.children === 'COPIED')).toBe(true);
});

test('hides the identity block copy when there is nothing to copy', async () => {
  const emptyDetail: ItemDetail = {
    id: 'identity-2',
    title: 'Empty',
    item_type: 'identity',
    vault_scope: 'personal',
    owner_id: 'owner-1',
    creator_id: 'owner-1',
    favorite: false,
    revision: 1,
    created_at: '2026-09-16T00:00:00Z',
    updated_at: '2026-09-16T00:00:00Z',
    payload: {name: 'Empty', notes: 'n/a'},
  };
  const api = {
    getItem: jest.fn(async () => ({kind: 'success' as const, status: 200, data: emptyDetail})),
  } as unknown as TinyPasswordApi;
  const session = {csrfToken: 'csrf-1', currentPrincipal: null} as unknown as SessionController;
  let tree!: ReactTestRenderer;

  await act(async () => {
    tree = createTree(
      <EntryEditorScreen
        session={session}
        api={api}
        route={{mode: 'detail', itemId: 'identity-2'}}
        onClose={() => undefined}
        onActivity={() => undefined}
      />,
    );
  });

  expect(tree.root.findAllByProps({testID: 'copy-identity-address'})).toHaveLength(0);
});

test('returns to sign in when loading a detail receives 401', async () => {
  const api = {getItem: jest.fn(async () => ({kind: 'http-error', status: 401, error: {code: 'UNAUTHORIZED', message: 'expired'}}))} as unknown as TinyPasswordApi;
  const invalidateLocally = jest.fn();
  const session = {csrfToken: 'csrf', invalidateLocally} as unknown as SessionController;
  await act(async () => {
    createTree(<EntryEditorScreen session={session} api={api} route={{mode: 'detail', itemId: 'test'}} onClose={jest.fn()} onActivity={jest.fn()} />);
  });
  expect(invalidateLocally).toHaveBeenCalledTimes(1);
});

test('switching types preserves the draft until the user confirms', async () => {
  let tree!: ReactTestRenderer;
  await act(async () => {
    tree = createTree(<EntryEditorScreen session={{csrfToken: 'csrf'} as SessionController} api={{} as TinyPasswordApi} route={{mode: 'create'}} onClose={jest.fn()} onActivity={jest.fn()} />);
  });
  act(() => tree.root.findByProps({testID: 'field-title'}).props.onChangeText('unsaved draft'));
  act(() => tree.root.findByProps({testID: 'type-secure_note'}).props.onPress());
  expect(tree.root.findByProps({testID: 'field-title'}).props.value).toBe('unsaved draft');
  const dialog = () => tree.root.findAllByType(ConfirmDialog).find(node => node.props.testID === 'switch-type-confirm')!;
  expect(dialog().props.visible).toBe(true);
  act(() => dialog().props.onCancel());
  expect(tree.root.findByProps({testID: 'field-title'}).props.value).toBe('unsaved draft');
  act(() => tree.root.findByProps({testID: 'type-secure_note'}).props.onPress());
  act(() => dialog().props.onConfirm());
  expect(tree.root.findByProps({testID: 'field-title'}).props.value).toBe('');
  expect(tree.root.findByProps({testID: 'type-secure_note'}).props.accessibilityState.selected).toBe(true);
});

test('blocks leaving during save and ignores a late result after unmount', async () => {
  let resolveCreate!: (value: unknown) => void;
  const api = {createItem: jest.fn(() => new Promise(resolve => {resolveCreate = resolve;}))} as unknown as TinyPasswordApi;
  const onClose = jest.fn();
  let tree!: ReactTestRenderer;
  await act(async () => {
    tree = createTree(<EntryEditorScreen session={{csrfToken: 'csrf'} as SessionController} api={api} route={{mode: 'create'}} onClose={onClose} onActivity={jest.fn()} />);
  });
  act(() => tree.root.findByProps({testID: 'field-title'}).props.onChangeText('draft'));
  act(() => tree.root.findByProps({testID: 'field-password'}).props.onChangeText('dummy-password'));
  let saving!: Promise<void>;
  act(() => {saving = tree.root.findByProps({testID: 'editor-save'}).props.onPress();});
  expect(api.createItem).toHaveBeenCalledTimes(1);
  act(() => tree.root.findByProps({testID: 'editor-header'}).props.onBack());
  expect(onClose).not.toHaveBeenCalled();
  act(() => tree.unmount());
  const signal = (api.createItem as jest.Mock).mock.calls[0][4] as AbortSignal;
  expect(signal.aborted).toBe(true);
  await act(async () => {
    resolveCreate({kind: 'success', status: 201, data: {}});
    await saving;
  });
  expect(onClose).not.toHaveBeenCalled();
});

const renderedTrees: ReactTestRenderer[] = [];
function createTree(element: React.ReactElement): ReactTestRenderer {
  const tree = renderer.create(element);
  renderedTrees.push(tree);
  return tree;
}
afterEach(async () => {
  act(() => renderedTrees.splice(0).forEach(tree => tree.unmount()));
  await sensitiveClipboard.clear();
});
