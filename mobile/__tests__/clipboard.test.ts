import {createClipboardGuard, CLIPBOARD_TTL_MS} from '../src/privacy/clipboard';

jest.mock('@react-native-clipboard/clipboard', () => ({
  __esModule: true,
  default: {setString: jest.fn(), getString: jest.fn(async () => '')},
}));

beforeEach(() => jest.useFakeTimers());
afterEach(() => jest.useRealTimers());

function setup() {
  let value = '';
  let readable = true;
  const clipboard = {
    getString: jest.fn(async () => value),
    setString: jest.fn((text: string) => {
      value = text;
    }),
  };
  const guard = createClipboardGuard(
    clipboard,
    () => Date.now(),
    () => readable,
  );
  return {
    clipboard,
    guard,
    getValue: () => value,
    setReadable: (next: boolean) => {
      readable = next;
    },
  };
}

test('expires a copied secret after sixty seconds', async () => {
  const {guard, getValue} = setup();
  guard.copy('secret');
  await jest.advanceTimersByTimeAsync(CLIPBOARD_TTL_MS - 1);
  expect(getValue()).toBe('secret');
  await jest.advanceTimersByTimeAsync(1);
  expect(getValue()).toBe('');
});

test('logout clears an owned secret but preserves a later external copy', async () => {
  const {guard, clipboard, getValue} = setup();
  guard.copy('secret');
  await guard.clear();
  expect(getValue()).toBe('');
  guard.copy('secret');
  clipboard.setString('other app text');
  await guard.clear();
  expect(getValue()).toBe('other app text');
});

test('an expired background copy is cleared on returning to the foreground', async () => {
  const {guard, getValue, setReadable} = setup();
  guard.copy('secret');
  setReadable(false);
  await jest.advanceTimersByTimeAsync(CLIPBOARD_TTL_MS);
  expect(getValue()).toBe('secret');
  setReadable(true);
  await guard.clearExpired();
  expect(getValue()).toBe('');
});

test('an old asynchronous cleanup cannot clear a newer copy', async () => {
  const {guard, clipboard, getValue} = setup();
  guard.copy('old');
  let finishRead!: (value: string) => void;
  clipboard.getString.mockImplementationOnce(
    () =>
      new Promise(resolve => {
        finishRead = resolve;
      }),
  );
  const pending = guard.clear();
  guard.copy('new');
  finishRead('old');
  await pending;
  expect(getValue()).toBe('new');
  await guard.clear();
});
