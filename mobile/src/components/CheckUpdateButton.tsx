import React, {useEffect, useRef, useState} from 'react';
import {Linking, NativeModules, Platform, StyleSheet, Text, View} from 'react-native';
import {checkForUpdate, type AppRelease, type AppVersion} from '../updates/checkForUpdate';
import {useInteraction} from '../privacy/interaction';
import {colors} from '../theme/colors';
import {fonts, typeScale} from '../theme/typography';
import {NewsprintButton} from './NewsprintButton';
import {Banner} from './Banner';
import {ConfirmDialog} from './ConfirmDialog';

export function CheckUpdateButton(): React.JSX.Element | null {
  const version = NativeModules.AppVersion as AppVersion | undefined;
  const {onActivity} = useInteraction();
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [failed, setFailed] = useState(false);
  const [release, setRelease] = useState<AppRelease | null>(null);
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => { pending.current?.abort(); }, []);

  if (Platform.OS !== 'android') { return null; }

  async function check(): Promise<void> {
    if (pending.current) { return; }
    onActivity();
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setNotice('');
    setFailed(false);
    setRelease(null);
    try {
      if (!version) { throw new Error('无法读取当前应用版本，请重新安装正式版后重试。'); }
      const update = await checkForUpdate(version, controller.signal);
      if (!controller.signal.aborted) {
        setRelease(update);
        if (!update) { setNotice('当前已是最新版本。'); }
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        setFailed(true);
        setNotice(error instanceof Error ? error.message : '检查更新失败，请重试。');
      }
    } finally {
      if (!controller.signal.aborted) { setBusy(false); }
      pending.current = null;
    }
  }

  return (
    <View>
      {version ? <Text style={styles.version}>版本 {version.versionName} ({version.versionCode})</Text> : null}
      <NewsprintButton
        label={busy ? '正在检查更新…' : '检查更新'}
        variant="ghost"
        loading={busy}
        onPress={() => void check()}
        testID="check-update"
      />
      {notice ? <Banner kind={failed ? 'error' : 'info'} text={notice} testID="update-notice" /> : null}
      <ConfirmDialog
        visible={release !== null}
        title={`发现新版本 ${release?.versionName ?? ''}`}
        message={`当前版本：${version?.versionName} (${version?.versionCode})\n新版本：${release?.versionName} (${release?.versionCode})\n\n${release?.notes || '新版本已发布。'}\n\n将在浏览器中下载 APK，下载完成后请手动安装。`}
        confirmLabel="下载更新"
        cancelLabel="稍后再说"
        onCancel={() => setRelease(null)}
        onConfirm={() => {
          if (!release) { return; }
          const url = release.downloadUrl;
          setRelease(null);
          void Linking.openURL(url).catch(() => {
            setFailed(true);
            setNotice('无法打开下载链接，请检查浏览器设置后重试。');
          });
        }}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  version: {...typeScale.meta, fontFamily: fonts.mono, color: colors.neutral600, textAlign: 'center'},
});
