import {checkForUpdate, parseAppRelease} from '../src/updates/checkForUpdate';

const current = {versionName: '1.3.0', versionCode: 8};
function release(code = 9, name = '1.3.0') {
  const tag = `app-v${name}-${code}`;
  return {
    tag_name: tag, draft: false, prerelease: false, body: '修复和改进',
    assets: [{name: 'app-release.apk', state: 'uploaded', size: 100,
      browser_download_url: `https://github.com/zxxx98/tiny-password/releases/download/${tag}/app-release.apk`}],
  };
}
const originalFetch = global.fetch;
const fetchMock = jest.fn();
beforeEach(() => { global.fetch = fetchMock; fetchMock.mockReset(); });
afterEach(() => { global.fetch = originalFetch; jest.useRealTimers(); });
function respond(data: unknown) {
  fetchMock.mockResolvedValueOnce({ok: true, json: async () => data});
}

test('uses versionCode, including rebuilds with the same versionName, and chooses highest code', async () => {
  respond([release(9), release(11, '1.4.0'), release(10)]);
  expect(await checkForUpdate(current)).toMatchObject({versionCode: 11});
  respond([release(9)]);
  expect(await checkForUpdate(current)).toMatchObject({versionCode: 9});
});

test.each([7, 8])('does not offer an equal or older version: %s', async code => {
  respond([release(code)]);
  expect(await checkForUpdate(current)).toBeNull();
});

test('skips server versions, drafts, previews, absent/incomplete APKs and foreign download URLs', () => {
  const valid = release();
  for (const invalid of [null, {}, {...valid, tag_name: 'v2.0.0'}, {...valid, draft: true},
    {...valid, prerelease: true}, {...valid, tag_name: 'app-v1.4.0-beta-9'}, {...valid, assets: []},
    {...valid, assets: [{...valid.assets[0], state: 'new'}]},
    {...valid, assets: [{...valid.assets[0], browser_download_url: 'https://example.com/app-release.apk'}]}]) {
    expect(parseAppRelease(invalid)).toBeNull();
  }
});

test('paginates past server releases without sending credentials', async () => {
  respond(Array.from({length: 100}, () => ({tag_name: 'v2.0.0'})));
  respond([release()]);
  expect(await checkForUpdate(current)).toMatchObject({versionCode: 9});
  expect(fetchMock).toHaveBeenLastCalledWith(expect.stringContaining('page=2'), expect.objectContaining({credentials: 'omit'}));
});

test('reports no usable release instead of falsely claiming the app is up to date', async () => {
  respond([]);
  await expect(checkForUpdate(current)).rejects.toThrow('暂未找到');
});

test('reports malformed response, rate limiting and network errors', async () => {
  respond({message: 'bad'});
  await expect(checkForUpdate(current)).rejects.toThrow('格式异常');
  fetchMock.mockResolvedValueOnce({ok: false, status: 403});
  await expect(checkForUpdate(current)).rejects.toThrow('受限');
  fetchMock.mockRejectedValueOnce(new TypeError('Network request failed'));
  await expect(checkForUpdate(current)).rejects.toThrow('检查网络');
});

test('aborts requests after the timeout', async () => {
  jest.useFakeTimers();
  fetchMock.mockImplementationOnce((_url, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new Error('Aborted')));
  }));
  const result = expect(checkForUpdate(current)).rejects.toThrow('超时');
  await jest.advanceTimersByTimeAsync(15000);
  await result;
});

test('propagates cancellation to the request', async () => {
  const controller = new AbortController();
  fetchMock.mockImplementationOnce((_url, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new Error('Aborted')));
  }));
  const result = expect(checkForUpdate(current, controller.signal)).rejects.toThrow('取消');
  controller.abort();
  await result;
});
