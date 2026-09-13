import React, {useState} from 'react';
import {KeyboardAvoidingView, Platform, Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';
import {NewsprintInput} from './NewsprintInput';
import {NewsprintButton} from './NewsprintButton';
import {Banner} from './Banner';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {pageMargin, spacing} from '../theme/spacing';
import {validateNewPassword} from '../vault/validation';

interface ChangePasswordFormProps {
  username: string;
  submitting: boolean;
  errorMessage: string | null;
  onSubmit: (current: string, next: string) => void;
  onSignOut: () => void;
  testID?: string;
}

/**
 * Forced first-login password change, rendered inside SignInScreen. Back
 * cannot reach the Vault from here (handled by the screen); the only exits
 * are SAVE AND CONTINUE and SIGN OUT. Confirm-password is validated locally
 * only and never sent. Inputs are never persisted.
 */
export function ChangePasswordForm({
  username,
  submitting,
  errorMessage,
  onSubmit,
  onSignOut,
  testID,
}: ChangePasswordFormProps): React.JSX.Element {
  // The current password is retyped by the user; the login plaintext is
  // never carried over or auto-filled (design §4.5).
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [showCurrent, setShowCurrent] = useState(false);
  const [showNext, setShowNext] = useState(false);
  const [showConfirm, setShowConfirm] = useState(false);
  const [clientError, setClientError] = useState<string | null>(null);

  const submit = () => {
    if (submitting) {
      return;
    }
    const error = validateNewPassword(current, next, confirm);
    setClientError(error);
    if (error) {
      return;
    }
    onSubmit(current, next);
  };

  const combinedError = clientError ?? errorMessage ?? '';

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'android' ? undefined : 'padding'}
      style={styles.flex}>
      <ScrollView contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled" testID={testID}>
        <Text style={styles.heading}>CHANGE PASSWORD</Text>
        <Text style={styles.explain}>首次登录需要设置新密码，完成后其他设备的旧会话将失效。</Text>

        <NewsprintInput label="USERNAME" value={username} editable={false} mono />

        <NewsprintInput
          label="CURRENT PASSWORD"
          value={current}
          onChangeText={setCurrent}
          secure={!showCurrent}
          autoCapitalize="none"
          autoCorrect={false}
          textContentType="password"
          editable={!submitting}
          accessibilityLabel="当前密码"
        />
        <ShowToggle
          label={showCurrent ? 'HIDE' : 'SHOW'}
          accessibilityLabel={showCurrent ? '隐藏当前密码' : '显示当前密码'}
          onPress={() => setShowCurrent(v => !v)}
        />

        <NewsprintInput
          label="NEW PASSWORD"
          value={next}
          onChangeText={setNext}
          secure={!showNext}
          autoCapitalize="none"
          autoCorrect={false}
          textContentType="newPassword"
          editable={!submitting}
          accessibilityLabel="新密码"
        />
        <ShowToggle
          label={showNext ? 'HIDE' : 'SHOW'}
          accessibilityLabel={showNext ? '隐藏新密码' : '显示新密码'}
          onPress={() => setShowNext(v => !v)}
        />

        <NewsprintInput
          label="CONFIRM NEW PASSWORD"
          value={confirm}
          onChangeText={setConfirm}
          secure={!showConfirm}
          autoCapitalize="none"
          autoCorrect={false}
          textContentType="newPassword"
          editable={!submitting}
          accessibilityLabel="确认新密码"
        />
        <ShowToggle
          label={showConfirm ? 'HIDE' : 'SHOW'}
          accessibilityLabel={showConfirm ? '隐藏确认密码' : '显示确认密码'}
          onPress={() => setShowConfirm(v => !v)}
        />

        <Banner kind="error" text={combinedError} testID="change-password-error" />

        <NewsprintButton
          label="SAVE AND CONTINUE"
          onPress={submit}
          disabled={submitting}
          loading={submitting}
          testID="change-password-submit"
          accessibilityLabel="保存新密码并继续"
        />
        <NewsprintButton
          label="SIGN OUT"
          variant="secondary"
          onPress={onSignOut}
          disabled={submitting}
          style={styles.signOut}
          testID="change-password-signout"
        />
        <Text style={styles.hint}>新密码至少 12 个字符，最多 1024 字节；确认密码仅在本地校验。</Text>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

function ShowToggle({
  label,
  accessibilityLabel,
  onPress,
}: {
  label: string;
  accessibilityLabel: string;
  onPress: () => void;
}): React.JSX.Element {
  return (
    <View style={styles.showRow}>
      <Pressable
        accessibilityRole="button"
        accessibilityLabel={accessibilityLabel}
        onPress={onPress}
        hitSlop={8}
        style={styles.showButton}>
        <Text style={styles.showText}>{label}</Text>
      </Pressable>
    </View>
  );
}

const styles = StyleSheet.create({
  flex: {
    flex: 1,
  },
  content: {
    paddingHorizontal: pageMargin,
    paddingBottom: spacing.xl,
  },
  heading: {
    ...typeScale.h2,
    fontFamily: fonts.display,
    color: colors.foreground,
    marginTop: spacing.md,
  },
  explain: {
    ...typeScale.body,
    fontFamily: fonts.body,
    color: colors.foreground,
    marginVertical: spacing.md,
  },
  showRow: {
    flexDirection: 'row',
    justifyContent: 'flex-end',
    marginTop: -10,
    marginBottom: spacing.sm,
  },
  showButton: {
    minHeight: 36,
    justifyContent: 'center',
    paddingHorizontal: spacing.xs,
  },
  showText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  signOut: {
    marginTop: spacing.md,
  },
  hint: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
    textAlign: 'center',
    marginTop: spacing.md,
  },
});
