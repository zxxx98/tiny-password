import React, {useEffect, useRef, useState} from 'react';
import {Modal, Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';
import {useSafeAreaInsets} from 'react-native-safe-area-context';
import {NewsprintHeader} from '../components/NewsprintHeader';
import {NewsprintInput} from '../components/NewsprintInput';
import {NewsprintButton} from '../components/NewsprintButton';
import {Banner} from '../components/Banner';
import {CheckUpdateButton} from '../components/CheckUpdateButton';
import {useInteraction} from '../privacy/interaction';
import {colors} from '../theme/colors';
import {fonts, typeScale} from '../theme/typography';
import {pageMargin, spacing} from '../theme/spacing';

interface SettingsScreenProps {
  onClose: () => void;
  biometricAvailable: boolean;
  biometricEnabled: boolean;
  authenticated: boolean;
  onBiometricChange: (enabled: boolean, password?: string) => Promise<void>;
}

/** A full-screen settings page keeps the underlying form/list state intact. */
export function SettingsScreen({onClose, biometricAvailable, biometricEnabled, authenticated, onBiometricChange}: SettingsScreenProps): React.JSX.Element {
  const {hidden, onActivity} = useInteraction();
  const insets = useSafeAreaInsets();
  const [enrolling, setEnrolling] = useState(false);
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const busyRef = useRef(false);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const close = () => { if (!busyRef.current) { onClose(); } };
  async function change(enabled: boolean): Promise<void> {
    if (busyRef.current) { return; }
    busyRef.current = true;
    setBusy(true);
    setError('');
    try {
      await onBiometricChange(enabled, password);
      if (mounted.current) { setEnrolling(false); }
    } catch (failure) {
      if (mounted.current) { setError(failure instanceof Error ? failure.message : '设置未能保存，请重试'); }
    } finally {
      busyRef.current = false;
      if (mounted.current) { setBusy(false); setPassword(''); }
    }
  }
  return (
    <Modal visible={!hidden} animationType="slide" onRequestClose={close}>
      <View style={[styles.root, {paddingBottom: insets.bottom}]} onTouchStart={onActivity}>
        <NewsprintHeader title="SETTINGS" kicker="tiny-password" backLabel="返回" onBack={close} />
        <ScrollView contentContainerStyle={styles.content}>
          <Text style={styles.title}>登录与安全</Text>
          <Pressable
            accessibilityRole="checkbox"
            accessibilityLabel="启动时使用指纹登录"
            accessibilityState={{checked: biometricEnabled, disabled: busy || (!biometricAvailable && !biometricEnabled)}}
            disabled={busy || (!biometricAvailable && !biometricEnabled)}
            style={styles.biometricRow}
            testID="enable-biometric"
            onPress={() => {
              onActivity();
              setError('');
              if (biometricEnabled) { void change(false); }
              else if (authenticated) { setEnrolling(true); }
              else { void change(true); }
            }}>
            <Text style={styles.checkbox}>{biometricEnabled ? '☑' : '☐'}</Text>
            <Text style={styles.label}>启动时使用指纹 / 生物识别登录</Text>
          </Pressable>
          <Text style={styles.description}>
            {!biometricAvailable ? '请先在系统设置中录入指纹或其他生物识别。'
              : authenticated ? '启用后，下次打开应用会自动验证指纹，通过后直接登录。'
                : '勾选后，请返回并使用密码登录一次以完成启用。以后打开应用会先验证指纹，通过后直接登录。'}
          </Text>
          {enrolling ? (
            <View>
              <NewsprintInput
                label="当前账号密码"
                value={password}
                onChangeText={setPassword}
                secure
                autoCapitalize="none"
                autoCorrect={false}
                editable={!busy}
                testID="biometric-password"
              />
              <NewsprintButton label="验证并启用" loading={busy} disabled={!password} onPress={() => void change(true)} testID="confirm-biometric" />
              <NewsprintButton label="取消" variant="ghost" disabled={busy} onPress={() => { setEnrolling(false); setPassword(''); }} />
            </View>
          ) : null}
          {error ? <Banner kind="error" text={error} /> : null}
          <Text style={styles.title}>应用更新</Text>
          <Text style={styles.description}>查看当前版本，手动检查是否有新版本。</Text>
          <CheckUpdateButton />
        </ScrollView>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  biometricRow: {minHeight: 48, flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginTop: spacing.sm},
  checkbox: {fontSize: 24, color: colors.foreground},
  label: {...typeScale.ui, fontFamily: fonts.ui, color: colors.foreground, flex: 1},
  root: {flex: 1, backgroundColor: colors.background},
  content: {padding: pageMargin, paddingBottom: spacing.xl},
  title: {...typeScale.h3, fontFamily: fonts.display, color: colors.foreground},
  description: {...typeScale.body, fontFamily: fonts.body, color: colors.neutral600, marginTop: spacing.sm, marginBottom: spacing.lg},
});
