import React from 'react';
import {Linking, NativeModules, Platform} from 'react-native';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {CheckUpdateButton} from '../src/components/CheckUpdateButton';
import {NewsprintButton} from '../src/components/NewsprintButton';
import {ConfirmDialog} from '../src/components/ConfirmDialog';
import {Banner} from '../src/components/Banner';
import {checkForUpdate} from '../src/updates/checkForUpdate';

jest.mock('../src/updates/checkForUpdate', () => ({checkForUpdate: jest.fn()}));
const check = jest.mocked(checkForUpdate);
let tree: ReactTestRenderer;
const originalPlatform = Platform.OS;
beforeEach(() => {
  Platform.OS = 'android';
  NativeModules.AppVersion = {versionName: '1.3.0', versionCode: 8};
  check.mockReset();
  act(() => { tree = renderer.create(<CheckUpdateButton />); });
});
afterEach(() => {
  act(() => tree.unmount());
  Platform.OS = originalPlatform;
  delete NativeModules.AppVersion;
  jest.restoreAllMocks();
});
async function pressCheck() {
  await act(async () => { tree.root.findByType(NewsprintButton).props.onPress(); });
}

test('shows up-to-date status using installed APK version', async () => {
  check.mockResolvedValue(null);
  await pressCheck();
  expect(check).toHaveBeenCalledWith({versionName: '1.3.0', versionCode: 8}, expect.anything());
  expect(tree.root.findByType(Banner).props.text).toContain('最新版本');
});

test('offers an update, only opens download on confirmation, and reports browser failures', async () => {
  const downloadUrl = 'https://github.com/zxxx98/tiny-password/releases/download/app-v1.4.0-9/app-release.apk';
  check.mockResolvedValue({versionName: '1.4.0', versionCode: 9, notes: '改进', downloadUrl});
  const open = jest.spyOn(Linking, 'openURL').mockRejectedValue(new Error('No browser'));
  await pressCheck();
  const dialog = tree.root.findByType(ConfirmDialog);
  expect(dialog.props.visible).toBe(true);
  expect(dialog.props.message).toContain('改进');
  expect(open).not.toHaveBeenCalled();
  await act(async () => { dialog.props.onConfirm(); });
  expect(open).toHaveBeenCalledWith(downloadUrl);
  expect(tree.root.findByType(Banner).props.text).toContain('无法打开');
});

test('allows retry after a failed check', async () => {
  check.mockRejectedValueOnce(new Error('网络不可用')).mockResolvedValueOnce(null);
  await pressCheck();
  expect(tree.root.findByType(Banner).props.kind).toBe('error');
  await pressCheck();
  expect(tree.root.findByType(Banner).props.kind).toBe('info');
});

test('prevents duplicate requests and aborts on unmount', async () => {
  check.mockImplementation(() => new Promise(() => {}));
  await pressCheck();
  await pressCheck();
  expect(check).toHaveBeenCalledTimes(1);
  expect(tree.root.findByType(NewsprintButton).props.loading).toBe(true);
  const signal = check.mock.calls[0][1];
  act(() => tree.unmount());
  expect(signal?.aborted).toBe(true);
});

test('does not show the Android updater on iOS', () => {
  Platform.OS = 'ios';
  act(() => tree.update(<CheckUpdateButton />));
  expect(tree.toJSON()).toBeNull();
});
