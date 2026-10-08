const REPOSITORY = 'zxxx98/tiny-password';
const RELEASES_URL = `https://api.github.com/repos/${REPOSITORY}/releases`;

export interface AppVersion {
  versionName: string;
  versionCode: number;
}

export interface AppRelease extends AppVersion {
  downloadUrl: string;
  notes: string;
}

/** Ignore server releases, previews and incomplete APK uploads. */
export function parseAppRelease(value: unknown): AppRelease | null {
  if (!value || typeof value !== 'object') { return null; }
  const release = value as Record<string, unknown>;
  if (release.draft !== false || release.prerelease !== false || typeof release.tag_name !== 'string') {
    return null;
  }
  const match = /^app-v(\d+(?:\.\d+)+)-(\d+)$/.exec(release.tag_name);
  if (!match || !Array.isArray(release.assets)) { return null; }
  const versionCode = Number(match[2]);
  if (!Number.isSafeInteger(versionCode) || versionCode <= 0) { return null; }
  const downloadUrl = `https://github.com/${REPOSITORY}/releases/download/${release.tag_name}/app-release.apk`;
  const apk = release.assets.find(asset => asset && asset.name === 'app-release.apk' &&
    asset.state === 'uploaded' && asset.size > 0 && asset.browser_download_url === downloadUrl);
  if (!apk) { return null; }
  return {
    versionName: match[1], versionCode, downloadUrl,
    notes: typeof release.body === 'string' ? release.body.trim().slice(0, 4000) : '',
  };
}

export async function checkForUpdate(current: AppVersion, signal?: AbortSignal): Promise<AppRelease | null> {
  if (!Number.isSafeInteger(current.versionCode) || current.versionCode <= 0) {
    throw new Error('无法读取当前应用版本，请重新安装正式版后重试。');
  }
  const controller = new AbortController();
  const cancel = () => controller.abort();
  signal?.addEventListener('abort', cancel);
  if (signal?.aborted) { cancel(); }
  const timeout = setTimeout(cancel, 15000);
  let latest: AppRelease | null = null;
  try {
    // Releases also contains server builds. Walk pages rather than using /latest.
    for (let page = 1; page <= 10; page++) {
      const response = await fetch(`${RELEASES_URL}?per_page=100&page=${page}`, {
        headers: {Accept: 'application/vnd.github+json'},
        credentials: 'omit',
        signal: controller.signal,
      });
      if (!response.ok) {
        throw new Error(response.status === 403 || response.status === 429
          ? '更新服务暂时受限，请稍后重试。'
          : '无法获取更新信息，请稍后重试。');
      }
      const releases: unknown = await response.json();
      if (!Array.isArray(releases)) { throw new Error('更新信息格式异常，请稍后重试。'); }
      for (const value of releases) {
        const release = parseAppRelease(value);
        if (release && (!latest || release.versionCode > latest.versionCode)) { latest = release; }
      }
      if (releases.length < 100) {
        if (!latest) { throw new Error('暂未找到可用的安卓正式版本，请稍后重试。'); }
        return latest.versionCode > current.versionCode ? latest : null;
      }
    }
    throw new Error('更新记录过多，暂时无法确认最新版本。');
  } catch (error) {
    if (controller.signal.aborted) { throw new Error('检查更新已取消或超时，请重试。'); }
    if (error instanceof TypeError) { throw new Error('无法连接更新服务，请检查网络后重试。'); }
    throw error;
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener('abort', cancel);
  }
}
