import React, {useCallback, useEffect, useRef, useState} from 'react';
import {
  BackHandler,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import {useSafeAreaInsets} from 'react-native-safe-area-context';
import {NewsprintInput} from '../components/NewsprintInput';
import {NewsprintButton} from '../components/NewsprintButton';
import {Banner} from '../components/Banner';
import {ChangePasswordForm} from '../components/ChangePasswordForm';
import {ConfirmDialog} from '../components/ConfirmDialog';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {borderSection, pageMargin, spacing} from '../theme/spacing';
import {normalizeServerUrl} from '../api/client';
import type {SessionController} from '../auth/session';

interface SignInScreenProps {
  session: SessionController;
  defaultServerUrl: string;
  /** One-shot banner, e.g. the sign-out notice from the Vault. */
  initialNotice?: string | null;
  onAuthenticated: () => void;
  onActivity: () => void;
}

type Phase = 'sign-in' | 'change-password';

/** How long the change-success banner holds the vault entry (ms). */
const SUCCESS_HOLD_MS = 1500;

/**
 * Refresh the pre-auth CSRF context before it expires: the server discards
 * pre-auth contexts after 15 minutes, so sign-in re-preflights at 10 to
 * never submit a dead token.
 */
const PREAUTH_REFRESH_MS = 10 * 60 * 1000;

/**
 * Sign in — and, on the same screen, the forced password change phase.
 * The screen owns the server-address control (collapsible; expanded when no
 * address is configured), the credential form and the change-password form.
 * No biometric button exists in V1; sessions never persist across cold
 * starts, so this screen is always the entry point.
 */
export function SignInScreen({
  session,
  defaultServerUrl,
  initialNotice,
  onAuthenticated,
  onActivity,
}: SignInScreenProps): React.JSX.Element {
  const insets = useSafeAreaInsets();
  // Prefill the configured server if there is one (e.g. right after sign
  // out), otherwise the build-time default — the input always reflects what
  // SIGN IN will actually use.
  const [serverUrl, setServerUrl] = useState(
    () => session.getSnapshot().serverUrl ?? defaultServerUrl,
  );
  const [serverExpanded, setServerExpanded] = useState(
    () => (session.getSnapshot().serverUrl ?? defaultServerUrl).trim().length === 0,
  );
  const [serverError, setServerError] = useState<string | null>(null);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [formError, setFormError] = useState<string | null>(initialNotice ?? null);
  const [phase, setPhase] = useState<Phase>('sign-in');
  const [submitting, setSubmitting] = useState(false);
  const [changeError, setChangeError] = useState<string | null>(null);
  const [changeSuccess, setChangeSuccess] = useState<string | null>(null);
  // Holds the subscription-driven vault entry for a beat after a successful
  // rotation so the success banner is actually visible before navigating.
  const holdNavigationRef = useRef(false);
  const successTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [signOutConfirmVisible, setSignOutConfirmVisible] = useState(false);
  const usernameRef = useRef(username);
  usernameRef.current = username;

  const applyServer = useCallback(
    (raw: string): boolean => {
      const normalized = normalizeServerUrl(raw);
      if (!normalized) {
        setServerError('请输入有效的 http(s) 服务器地址');
        return false;
      }
      setServerError(null);
      session.setServerUrl(normalized);
      return true;
    },
    [session],
  );

  // Configure the server (and fetch the pre-auth CSRF) on mount and whenever
  // the user applies a new address.
  useEffect(() => {
    if (serverUrl.trim().length > 0 && applyServer(serverUrl)) {
      void session.preflight();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Forced change password: session phase flips to must-change after login
  // or a session restore; mirror it into local UI phase.
  useEffect(() => {
    const unsubscribe = session.subscribe(() => {
      const snap = session.getSnapshot();
      if (snap.phase === 'must-change') {
        setPhase('change-password');
        setSubmitting(false);
      } else if (snap.phase === 'authenticated') {
        setSubmitting(false);
        if (holdNavigationRef.current) {
          return;
        }
        onAuthenticated();
      } else if (snap.phase === 'signed-out' || snap.phase === 'unconfigured') {
        setSubmitting(false);
        setPhase('sign-in');
      } else if (snap.phase === 'confirming-change') {
        setSubmitting(false);
      }
    });
    return unsubscribe;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session]);

  // During the confirming-change window, surface the retry error and poll
  // nothing — the user presses RETRY CONFIRM explicitly.
  useEffect(() => {
    if (phase !== 'change-password') {
      setChangeError(null);
    }
  }, [phase]);

  // Android Back: from the change-password phase there is no way around it;
  // leaving means signing out (with confirmation when fields are dirty).
  useEffect(() => {
    const handler = () => {
      if (phase === 'change-password') {
        if (signOutConfirmVisible) {
          return true;
        }
        setSignOutConfirmVisible(true);
        return true;
      }
      if (submitting) {
        return true;
      }
      return false; // sign-in phase: default Android behaviour
    };
    const subscription = BackHandler.addEventListener('hardwareBackPress', handler);
    return () => subscription.remove();
  }, [phase, signOutConfirmVisible, submitting]);

  const doSignIn = async () => {
    if (submitting) {
      return;
    }
    onActivity();
    setFormError(null);
    // If the input differs from the configured server (or nothing is
    // configured yet), apply it first so login never targets a stale server.
    const desired = normalizeServerUrl(serverUrl);
    if (!desired) {
      setServerError('请输入有效的 http(s) 服务器地址');
      setServerExpanded(true);
      return;
    }
    if (!session.serverConfigured || session.getSnapshot().serverUrl !== desired) {
      if (!applyServer(serverUrl)) {
        setServerExpanded(true);
        return;
      }
    }
    // A 401 (e.g. a failed forced password change) wipes the pre-auth context,
    // and an old context expires server-side after 15 minutes; re-fetch it so
    // a retry cannot dead-end on "缺少预认证上下文" or "安全校验失败".
    if (!session.preauthToken || session.preauthIsStale(PREAUTH_REFRESH_MS)) {
      const preflightOk = await session.preflight();
      if (!preflightOk) {
        setFormError('无法获取预认证上下文，请检查服务器地址');
        return;
      }
    }
    if (username.trim().length === 0 || password.length === 0) {
      setFormError('请输入用户名和密码');
      return;
    }
    setSubmitting(true);
    const result = await session.login(username.trim(), password);
    if (result.ok) {
      return; // phase subscription drives the next screen
    }
    setSubmitting(false);
    if (result.error) {
      setFormError(result.error);
    }
  };

  const doChangePassword = async (current: string, next: string) => {
    setChangeError(null);
    setChangeSuccess(null);
    setSubmitting(true);
    const result = await session.changePassword(current, next);
    if (result.ok) {
      // Confirm the session requirement is lifted; never resubmits the change.
      // Hold the subscription-driven navigation so the success banner shows.
      holdNavigationRef.current = true;
      const outcome = await session.confirmSession();
      if (outcome === 'confirmed') {
        setSubmitting(true); // stay disabled while the success banner holds
        setChangeSuccess('密码已修改成功，正在进入保险库…');
        successTimerRef.current = setTimeout(() => {
          holdNavigationRef.current = false;
          onAuthenticated();
        }, SUCCESS_HOLD_MS);
        return;
      }
      holdNavigationRef.current = false;
      if (outcome === 'still-required') {
        setSubmitting(false);
        setChangeError('服务端仍要求改密，请检查新密码是否满足策略');
        return;
      }
      if (outcome === 'unreachable') {
        setSubmitting(false);
        setChangeError('改密已提交，但确认会话失败。请点击“重试确认”，不会重复提交改密。');
        return;
      }
      // 'invalid': session controller already returned to sign-in.
      setSubmitting(false);
      setPhase('sign-in');
      setFormError('会话已失效，请重新登录');
      return;
    }
    setSubmitting(false);
    setChangeError(result.error);
    if (result.error === '认证失败，请重新登录') {
      setPhase('sign-in');
      setPassword('');
      // The bounce lands on the sign-in view, which renders formError —
      // changeError would never be visible here.
      setFormError('密码修改失败：' + result.error);
    }
  };

  const retryConfirm = async () => {
    setSubmitting(true);
    setChangeError(null);
    holdNavigationRef.current = true;
    const outcome = await session.confirmSession();
    if (outcome === 'confirmed') {
      setSubmitting(true); // stay disabled while the success banner holds
      setChangeSuccess('密码已修改成功，正在进入保险库…');
      successTimerRef.current = setTimeout(() => {
        holdNavigationRef.current = false;
        onAuthenticated();
      }, SUCCESS_HOLD_MS);
      return;
    }
    holdNavigationRef.current = false;
    setSubmitting(false);
    if (outcome === 'still-required') {
      setChangeError('服务端仍要求改密，请重新设置新密码');
      return;
    }
    if (outcome === 'invalid') {
      setPhase('sign-in');
      setFormError('会话已失效，请重新登录');
      return;
    }
    setChangeError('仍无法确认会话状态，请稍后重试。不会重复提交改密。');
  };

  const doSignOut = async () => {
    setSignOutConfirmVisible(false);
    if (successTimerRef.current !== null) {
      clearTimeout(successTimerRef.current);
      successTimerRef.current = null;
    }
    holdNavigationRef.current = false;
    setChangeSuccess(null);
    const result = await session.signOut();
    setPhase('sign-in');
    setChangeError(null);
    setPassword('');
    if (result.notice) {
      setFormError(result.notice);
    }
    // Refresh the pre-auth context for the next login attempt.
    void session.preflight();
  };

  if (phase === 'change-password') {
    const confirmDirty = true; // leaving the change phase always needs a prompt
    return (
      <View style={[styles.root, {paddingTop: insets.top, paddingBottom: insets.bottom}]}>
        <ChangePasswordForm
          username={usernameRef.current}
          submitting={submitting}
          errorMessage={changeError ?? session.getSnapshot().confirmError}
          onSubmit={doChangePassword}
          onSignOut={() => setSignOutConfirmVisible(true)}
        />
        {changeSuccess ? (
          <Banner kind="info" text={changeSuccess} testID="change-success" />
        ) : null}
        <Banner
          kind="warning"
          text={
            session.getSnapshot().confirmError
              ? '改密已提交；确认失败不会重复提交改密。'
              : ''
          }
        />
        {session.getSnapshot().confirmError ? (
          <View style={styles.retryWrap}>
            <NewsprintButton
              label="RETRY CONFIRM"
              variant="secondary"
              onPress={retryConfirm}
              disabled={submitting}
              testID="retry-confirm"
            />
          </View>
        ) : null}
        <ConfirmDialog
          visible={signOutConfirmVisible}
          title="退出登录？"
          message={
            confirmDirty
              ? '改密尚未完成，退出将放弃当前输入并返回登录页。'
              : '确定退出登录并返回登录页？'
          }
          confirmLabel="SIGN OUT"
          cancelLabel="CANCEL"
          destructive
          onConfirm={doSignOut}
          onCancel={() => setSignOutConfirmVisible(false)}
        />
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'android' ? undefined : 'padding'}
      style={[styles.root, {paddingTop: insets.top, paddingBottom: insets.bottom}]}>
      <ScrollView contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled">
        <View style={styles.masthead}>
          <Text style={styles.brand}>tiny-password</Text>
          <Text style={styles.kicker}>PRIVATE VAULT</Text>
          <View style={styles.rule} />
        </View>

        <Text style={styles.sectionTitle}>SIGN IN</Text>

        <View style={styles.serverBox}>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="配置服务器地址"
            accessibilityState={{expanded: serverExpanded}}
            onPress={() => setServerExpanded(v => !v)}
            style={styles.serverToggle}>
            <Text style={styles.serverToggleText}>
              {serverExpanded ? '▾ SERVER' : '▸ SERVER'}
            </Text>
            {!serverExpanded && serverUrl.trim().length > 0 ? (
              <Text style={styles.serverSummary} numberOfLines={1}>
                {normalizeServerUrl(serverUrl) ?? serverUrl}
              </Text>
            ) : null}
          </Pressable>
          {serverExpanded ? (
            <View style={styles.serverForm}>
              <NewsprintInput
                label="SERVER ADDRESS"
                value={serverUrl}
                onChangeText={setServerUrl}
                error={serverError}
                autoCapitalize="none"
                autoCorrect={false}
                keyboardType="url"
                placeholder="https://vault.example.com"
                accessibilityLabel="服务器地址"
                testID="server-input"
              />
              <NewsprintButton
                label="APPLY SERVER"
                variant="secondary"
                onPress={() => {
                  if (applyServer(serverUrl)) {
                    setServerExpanded(false);
                    void session.preflight();
                  }
                }}
                testID="apply-server"
              />
            </View>
          ) : null}
        </View>

        <NewsprintInput
          label="USERNAME"
          value={username}
          onChangeText={setUsername}
          autoCapitalize="none"
          autoCorrect={false}
          textContentType="username"
          autoComplete="username"
          importantForAutofill="yes"
          returnKeyType="next"
          accessibilityLabel="用户名"
          testID="username-input"
        />
        <View style={styles.passwordWrap}>
          <NewsprintInput
            label="PASSWORD"
            value={password}
            onChangeText={setPassword}
            secure={!showPassword}
            autoCapitalize="none"
            autoCorrect={false}
            textContentType="password"
            autoComplete="password"
            importantForAutofill="yes"
            returnKeyType="go"
            onSubmitEditing={doSignIn}
            accessibilityLabel="密码"
            testID="password-input"
          />
          <View style={styles.showRow}>
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={showPassword ? '隐藏密码' : '显示密码'}
              onPress={() => setShowPassword(v => !v)}
              hitSlop={8}
              style={styles.showButton}>
              <Text style={styles.showText}>{showPassword ? 'HIDE' : 'SHOW'}</Text>
            </Pressable>
          </View>
        </View>

        <Banner kind="error" text={formError ?? ''} testID="sign-in-error" />

        <NewsprintButton
          label="SIGN IN"
          onPress={doSignIn}
          disabled={submitting}
          loading={submitting}
          testID="sign-in-submit"
        />

        <View style={styles.footer}>
          <Text style={styles.footerMeta}>PRIVATE / SECURE</Text>
          <Text style={styles.footerMeta}>EDITION: MOBILE V1</Text>
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.background,
  },
  content: {
    paddingHorizontal: pageMargin,
    paddingBottom: spacing.xl,
  },
  masthead: {
    marginTop: spacing.lg,
  },
  brand: {
    ...typeScale.hero,
    fontFamily: fonts.displayHeavy,
    color: colors.foreground,
  },
  kicker: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.wide,
    color: colors.neutral600,
    marginTop: spacing.xs,
  },
  rule: {
    height: borderSection,
    backgroundColor: colors.foreground,
    marginTop: spacing.md,
  },
  sectionTitle: {
    ...typeScale.h2,
    fontFamily: fonts.display,
    color: colors.foreground,
    marginTop: spacing.xl,
    marginBottom: spacing.md,
  },
  serverBox: {
    borderWidth: 1,
    borderColor: colors.foreground,
    marginBottom: spacing.md,
  },
  serverToggle: {
    minHeight: 48,
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  serverToggleText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  serverSummary: {
    ...typeScale.monoSmall,
    fontFamily: fonts.mono,
    color: colors.neutral600,
    marginLeft: spacing.sm,
    flex: 1,
  },
  serverForm: {
    paddingHorizontal: spacing.md,
    paddingBottom: spacing.md,
  },
  passwordWrap: {},
  showRow: {
    flexDirection: 'row',
    justifyContent: 'flex-end',
    marginTop: -12,
    marginBottom: spacing.sm,
  },
  showButton: {
    minHeight: 40,
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
  footer: {
    marginTop: spacing.xl,
    flexDirection: 'row',
    justifyContent: 'space-between',
    borderTopWidth: 1,
    borderTopColor: colors.muted,
    paddingTop: spacing.md,
  },
  footerMeta: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.neutral600,
  },
  retryWrap: {
    paddingHorizontal: pageMargin,
    marginBottom: spacing.md,
  },
});
