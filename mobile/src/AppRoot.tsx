import React, {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {AppState, Keyboard, StatusBar, StyleSheet, Text, View} from 'react-native';
import {SafeAreaProvider} from 'react-native-safe-area-context';
import {SignInScreen} from './screens/SignInScreen';
import {VaultScreen} from './screens/VaultScreen';
import {SettingsScreen} from './screens/SettingsScreen';
import {GeneratorScreen} from './screens/GeneratorScreen';
import {EntryEditorScreen, type EditorRoute} from './screens/EntryEditorScreen';
import {NewsprintButton} from './components/NewsprintButton';
import {verifyBiometricEnrollment} from './auth/verifyBiometricEnrollment';
import {SessionController} from './auth/session';
import {colors} from './theme/colors';
import {fonts, letterSpacing, typeScale} from './theme/typography';
import {InteractionContext} from './privacy/interaction';
import {sensitiveClipboard} from './privacy/clipboard';
import {DEFAULT_SERVER_URL} from './config';
import {nativeRememberedLoginStore} from './auth/nativeRememberedLoginStore';
import type {RememberedCredentials, RememberedLoginSnapshot, RememberedLoginStore} from './auth/rememberedLogin';

type Route = {name: 'signin'} | {name: 'vault'} | {name: 'generator'} | {name: 'editor'; editor: EditorRoute};

interface AppRootProps {
  rememberedLoginStore?: RememberedLoginStore;
}

const EMPTY_REMEMBERED_LOGIN: RememberedLoginSnapshot = {
  serverUrl: null,
  credentials: null,
};

/**
 * App root. Navigation is a tiny explicit state machine (three top-level
 * screens, no tabs) so Android Back can be controlled exactly per design:
 * the forced-change phase cannot be bypassed, the generator sheet closes
 * first, dirty edits prompt before being dropped.
 *
 * Foreground resume: whenever the app returns from the background while a
 * session exists, sensitive content is masked first and the session is
 * revalidated; content returns only after the server confirms the session.
 */
export function AppRoot({rememberedLoginStore = nativeRememberedLoginStore}: AppRootProps = {}): React.JSX.Element {
  const sessionRef = useRef<SessionController | null>(null);
  if (sessionRef.current === null) {
    sessionRef.current = new SessionController();
  }
  const session = sessionRef.current;

  const [biometricRequested, setBiometricRequested] = useState(false);
  const autoBiometricPending = useRef(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [route, setRoute] = useState<Route>({name: 'signin'});
  const [signOutNotice, setSignOutNotice] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [editorNotice, setEditorNotice] = useState<string | null>(null);
  const [masked, setMasked] = useState(false);
  const [resumeUnreachable, setResumeUnreachable] = useState(false);
  const [rememberedLogin, setRememberedLogin] = useState(EMPTY_REMEMBERED_LOGIN);
  const credentialsRevisionRef = useRef(0);
  const [preferencesReady, setPreferencesReady] = useState(false);
  const [persistenceNotice, setPersistenceNotice] = useState<string | null>(null);
  const [snap, setSnap] = useState(() => session.getSnapshot());
  const maskedRef = useRef(false);
  const appStateRef = useRef(AppState.currentState);
  const resumeAttemptRef = useRef(0);
  const pendingResumeRef = useRef<number | null>(null);

  useEffect(() => {
    let current = true;
    void rememberedLoginStore
      .load()
      .then(snapshot => {
        if (current) {
          autoBiometricPending.current = Boolean(snapshot.biometricEnabled);
          setRememberedLogin(snapshot);
        }
      })
      .catch(() => {
        if (current) {
          setRememberedLogin(EMPTY_REMEMBERED_LOGIN);
          setPersistenceNotice('无法读取本地登录设置，请重新输入登录信息');
        }
      })
      .finally(() => {
        if (current) {
          setPreferencesReady(true);
        }
      });
    return () => {
      current = false;
    };
  }, [rememberedLoginStore]);

  const saveRememberedServerUrl = useCallback(
    (serverUrl: string): void => {
      setRememberedLogin(previous => ({...previous, serverUrl}));
      void rememberedLoginStore.saveServerUrl(serverUrl).catch(() => {
        setPersistenceNotice('服务器地址未能保存，请下次重新输入');
      });
    },
    [rememberedLoginStore],
  );

  const saveRememberedCredentials = useCallback(
    async (credentials: RememberedCredentials, biometric = false): Promise<void> => {
      const revision = ++credentialsRevisionRef.current;
      try {
        await rememberedLoginStore.saveCredentials(credentials, biometric);
      } catch {
        if (revision !== credentialsRevisionRef.current) { return; }
        setBiometricRequested(false);
        setRememberedLogin(previous => ({...previous, credentials: null, biometricEnabled: false}));
        setEditorNotice('登录成功，但本地登录凭据未能保存，下次请使用密码登录并重新启用');
        setPersistenceNotice('本地登录凭据未能保存，下次请使用密码登录并重新启用');
        return;
      }
      if (revision !== credentialsRevisionRef.current) { return; }
      setPersistenceNotice(null);
      setBiometricRequested(false);
      setRememberedLogin(previous => ({...previous,
        credentials: biometric ? null : credentials, biometricEnabled: biometric,
      }));
    },
    [rememberedLoginStore],
  );

  const clearRememberedCredentials = useCallback(async (): Promise<void> => {
    credentialsRevisionRef.current += 1;
    setBiometricRequested(false);
    autoBiometricPending.current = false;
    setRememberedLogin(previous => ({...previous, credentials: null, biometricEnabled: false}));
    try {
      await rememberedLoginStore.clearCredentials();
    } catch {
      setPersistenceNotice('本地登录凭据未能清除，请检查设备安全设置');
    }
  }, [rememberedLoginStore]);

  // Every phase update refreshes the API context, including rotated CSRF.
  useEffect(
    () =>
      session.subscribe(() => {
        const next = session.getSnapshot();
        setSnap(next);
        if (next.phase === 'signed-out' || next.phase === 'unconfigured') {
          setSettingsOpen(false);
          setRoute({name: 'signin'});
          setEditorNotice(null);
          void sensitiveClipboard.clear();
        } else if (next.phase === 'must-change' || next.phase === 'confirming-change') {
          setSettingsOpen(false);
          setRoute({name: 'signin'});
        }
      }),
    [session],
  );

  const reveal = useCallback(() => {
    maskedRef.current = false;
    setMasked(false);
    setResumeUnreachable(false);
  }, []);

  const validateResume = useCallback(async () => {
    if (appStateRef.current !== 'active' || pendingResumeRef.current !== null) {
      return;
    }
    const attempt = ++resumeAttemptRef.current;
    pendingResumeRef.current = attempt;
    setResumeUnreachable(false);
    const outcome = await session.validateOnResume();
    if (attempt !== resumeAttemptRef.current || appStateRef.current !== 'active') {
      return;
    }
    pendingResumeRef.current = null;
    if (outcome === 'unreachable') {
      setResumeUnreachable(true);
    } else {
      reveal();
    }
  }, [session, reveal]);

  useEffect(() => {
    const subscription = AppState.addEventListener('change', state => {
      appStateRef.current = state;
      if (state !== 'active') {
        resumeAttemptRef.current += 1;
        pendingResumeRef.current = null;
        maskedRef.current = true;
        setMasked(true);
        Keyboard.dismiss();
      } else {
        void sensitiveClipboard.clearExpired();
        if (maskedRef.current) {
          void validateResume();
        }
      }
    });
    return () => {
      subscription.remove();
      resumeAttemptRef.current += 1;
    };
  }, [validateResume]);

  const onActivity = useCallback(() => {
    if (!maskedRef.current && appStateRef.current === 'active') {
      void session.touchActivity();
    }
  }, [session]);
  const interaction = useMemo(() => ({hidden: masked, onActivity}), [masked, onActivity]);

  const handleSignOutFromVault = useCallback(() => {
    const signingOutApi = session.getApi();
    void clearRememberedCredentials();
    void sensitiveClipboard.clear();
    void session.signOut().then(result => {
      if (session.getApi() !== signingOutApi || session.getSnapshot().phase !== 'signed-out') {
        return;
      }
      setSignOutNotice(result.notice);
      setRoute({name: 'signin'});
      // Refresh pre-auth context for the next login.
      void session.preflight();
    });
  }, [clearRememberedCredentials, session]);

  const closeEditor = useCallback((mutation?: 'created' | 'trashed' | 'updated') => {
    setRoute({name: 'vault'});
    if (mutation === 'trashed') {
      setEditorNotice('已移入回收站；可在保留期内通过 Web 回收站恢复。');
      setRefreshKey(k => k + 1);
    } else if (mutation === 'created') {
      setEditorNotice('条目已创建。');
      setRefreshKey(k => k + 1);
    } else if (mutation === 'updated') {
      setRefreshKey(k => k + 1);
    }
  }, []);
  const changeBiometric = async (enabled: boolean, password?: string): Promise<void> => {
    if (!enabled) {
      // Surface deletion failures to settings rather than claiming the switch is off.
      ++credentialsRevisionRef.current;
      await rememberedLoginStore.clearCredentials();
      setBiometricRequested(false);
      autoBiometricPending.current = false;
      setRememberedLogin(previous => ({...previous, credentials: null, biometricEnabled: false}));
      return;
    }
    const snapshot = session.getSnapshot();
    if (snapshot.phase !== 'authenticated') {
      setBiometricRequested(true);
      return;
    }
    if (!password || !snapshot.serverUrl || !snapshot.principal) {
      throw new Error('请输入当前账号的密码');
    }
    const revision = ++credentialsRevisionRef.current;
    const credentials = {username: snapshot.principal.username, password};
    const stillCurrent = () => revision === credentialsRevisionRef.current
      && session.getSnapshot().phase === 'authenticated'
      && session.getSnapshot().serverUrl === snapshot.serverUrl
      && session.getSnapshot().principal?.user_id === snapshot.principal?.user_id;
    await verifyBiometricEnrollment(snapshot.serverUrl, credentials, snapshot.principal.user_id);
    if (!stillCurrent()) { throw new Error('登录状态已变化，请重新登录后启用'); }
    await rememberedLoginStore.saveServerUrl(snapshot.serverUrl);
    if (!stillCurrent()) { throw new Error('登录状态已变化，请重新登录后启用'); }
    try {
      await rememberedLoginStore.saveCredentials(credentials, true);
    } catch {
      if (stillCurrent()) {
        setRememberedLogin(previous => ({...previous, credentials: null, biometricEnabled: false}));
      }
      throw new Error('指纹登录未能启用，请重试或使用密码登录');
    }
    if (!stillCurrent()) {
      if (revision === credentialsRevisionRef.current) { await rememberedLoginStore.clearCredentials(); }
      throw new Error('登录状态已变化，请重新登录后启用');
    }
    setRememberedLogin(previous => ({...previous, credentials: null, biometricEnabled: true}));
    setBiometricRequested(false);
  };

  const api = session.getApi();

  return (
    <SafeAreaProvider>
      <InteractionContext.Provider value={interaction}>
        <StatusBar barStyle="dark-content" />
        <View style={styles.root} onTouchStart={onActivity}>
          <View
            style={styles.root}
            pointerEvents={masked ? 'none' : 'auto'}
            accessibilityElementsHidden={masked}
            importantForAccessibility={masked ? 'no-hide-descendants' : 'auto'}
          >
            {!preferencesReady ? (
              <View style={styles.loading}>
                <Text style={styles.loadingBrand}>tiny-password</Text>
                <Text style={styles.loadingText}>正在读取登录设置…</Text>
              </View>
            ) : route.name === 'signin' || snap.phase === 'signed-out' || snap.phase === 'unconfigured' ? (
              <SignInScreen
                session={session}
                initialServerUrl={rememberedLogin.serverUrl ?? DEFAULT_SERVER_URL}
                rememberedCredentials={rememberedLogin.credentials}
                biometricEnrollmentRequested={biometricRequested}
                autoBiometricLogin={!settingsOpen && autoBiometricPending.current}
                onAutoBiometricAttempt={() => { autoBiometricPending.current = false; }}
                biometricEnabled={rememberedLogin.biometricEnabled}
                onBiometricUnlock={rememberedLoginStore.unlockCredentials}
                initialNotice={signOutNotice}
                persistenceNotice={persistenceNotice}
                onRememberedServerUrlSaved={saveRememberedServerUrl}
                onRememberedCredentialsSaved={saveRememberedCredentials}
                onRememberedCredentialsCleared={clearRememberedCredentials}
                onAuthenticated={() => {
                  setSignOutNotice(null);
                  setRoute({name: 'vault'});
                }}
                onActivity={onActivity}
                onOpenSettings={() => { Keyboard.dismiss(); setSettingsOpen(true); }}
              />
            ) : route.name === 'generator' ? (
              api ? (
                <GeneratorScreen
                  api={api}
                  csrfToken={session.csrfToken}
                  onClose={() => setRoute({name: 'vault'})}
                  onActivity={onActivity}
                />
              ) : null
            ) : route.name === 'editor' ? (
              api ? (
                <EntryEditorScreen
                  session={session}
                  api={api}
                  route={route.editor}
                  onClose={closeEditor}
                  onActivity={onActivity}
                />
              ) : null
            ) : null}
            {preferencesReady && route.name !== 'signin' && snap.phase === 'authenticated' && api ? (
              <View
                style={[styles.root, route.name !== 'vault' && styles.hiddenScreen]}
                pointerEvents={route.name === 'vault' ? 'auto' : 'none'}
                importantForAccessibility={route.name === 'vault' ? 'auto' : 'no-hide-descendants'}
              >
                <VaultScreen
                  session={session}
                  api={api}
                  active={route.name === 'vault'}
                  refreshKey={refreshKey}
                  notice={editorNotice}
                  onOpenEntry={itemId => setRoute({name: 'editor', editor: {mode: 'detail', itemId}})}
                  onAddEntry={() => setRoute({name: 'editor', editor: {mode: 'create'}})}
                  onOpenGenerator={() => setRoute({name: 'generator'})}
                  onSignOut={handleSignOutFromVault}
                  onOpenSettings={() => { Keyboard.dismiss(); setSettingsOpen(true); }}
                  onActivity={onActivity}
                />
              </View>
            ) : null}
          </View>

          {settingsOpen ? (
            <SettingsScreen
              onClose={() => setSettingsOpen(false)}
              biometricAvailable={Boolean(rememberedLogin.biometricAvailable)}
              biometricEnabled={Boolean(rememberedLogin.biometricEnabled || biometricRequested)}
              authenticated={snap.phase === 'authenticated'}
              onBiometricChange={changeBiometric}
            />
          ) : null}

          {masked ? (
            <View accessibilityViewIsModal style={styles.mask} testID="resume-mask">
              <Text style={styles.maskBrand}>tiny-password</Text>
              <Text style={styles.maskText}>内容已隐藏</Text>
              <Text style={styles.maskHint}>
                {resumeUnreachable ? '无法连接服务器，暂时无法确认会话。请检查网络后重试。' : '正在校验会话…'}
              </Text>
              {resumeUnreachable ? (
                <View style={styles.maskRetry}>
                  <NewsprintButton
                    label="RETRY"
                    variant="secondary"
                    onPress={() => {
                      void validateResume();
                    }}
                  />
                </View>
              ) : null}
            </View>
          ) : null}
        </View>
      </InteractionContext.Provider>
    </SafeAreaProvider>
  );
}

const styles = StyleSheet.create({
  hiddenScreen: {...StyleSheet.absoluteFill, opacity: 0},
  root: {
    flex: 1,
    backgroundColor: colors.background,
  },
  loading: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 32,
    backgroundColor: colors.background,
  },
  loadingBrand: {
    ...typeScale.hero,
    fontFamily: fonts.displayHeavy,
    color: colors.foreground,
  },
  loadingText: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
    marginTop: 12,
  },
  mask: {
    ...StyleSheet.absoluteFill,
    backgroundColor: colors.background,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 32,
  },
  maskBrand: {
    ...typeScale.hero,
    fontFamily: fonts.displayHeavy,
    color: colors.foreground,
  },
  maskText: {
    ...typeScale.h3,
    fontFamily: fonts.display,
    color: colors.foreground,
    marginTop: 16,
  },
  maskRetry: {
    marginTop: 24,
    width: 200,
  },
  maskHint: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
    textAlign: 'center',
    marginTop: 12,
    letterSpacing: letterSpacing.label,
  },
});
