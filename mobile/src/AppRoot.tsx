import React, {useCallback, useEffect, useRef, useState} from 'react';
import {AppState, StatusBar, StyleSheet, Text, View} from 'react-native';
import {SafeAreaProvider} from 'react-native-safe-area-context';
import {SignInScreen} from './screens/SignInScreen';
import {VaultScreen} from './screens/VaultScreen';
import {GeneratorScreen} from './screens/GeneratorScreen';
import {EntryEditorScreen, type EditorRoute} from './screens/EntryEditorScreen';
import {NewsprintButton} from './components/NewsprintButton';
import {SessionController} from './auth/session';
import {colors} from './theme/colors';
import {fonts, letterSpacing, typeScale} from './theme/typography';
import {DEFAULT_SERVER_URL} from './config';
import {nativeRememberedLoginStore} from './auth/nativeRememberedLoginStore';
import type {
  RememberedCredentials,
  RememberedLoginSnapshot,
  RememberedLoginStore,
} from './auth/rememberedLogin';

type Route =
  | {name: 'signin'}
  | {name: 'vault'}
  | {name: 'generator'}
  | {name: 'editor'; editor: EditorRoute};

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

  const [route, setRoute] = useState<Route>({name: 'signin'});
  const [signOutNotice, setSignOutNotice] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [editorNotice, setEditorNotice] = useState<string | null>(null);
  const [masked, setMasked] = useState(false);
  const [resumeUnreachable, setResumeUnreachable] = useState(false);
  const [rememberedLogin, setRememberedLogin] = useState(EMPTY_REMEMBERED_LOGIN);
  const [preferencesReady, setPreferencesReady] = useState(false);
  const [persistenceNotice, setPersistenceNotice] = useState<string | null>(null);
  const resumeValidationRef = useRef(false);

  useEffect(() => {
    let current = true;
    void rememberedLoginStore
      .load()
      .then(snapshot => {
        if (current) {
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
    (credentials: RememberedCredentials): void => {
      setRememberedLogin(previous => ({...previous, credentials}));
      void rememberedLoginStore.saveCredentials(credentials).catch(() => {
        setPersistenceNotice('记住密码未能保存，请重试');
      });
    },
    [rememberedLoginStore],
  );

  const clearRememberedCredentials = useCallback(async (): Promise<void> => {
    setRememberedLogin(previous => ({...previous, credentials: null}));
    try {
      await rememberedLoginStore.clearCredentials();
    } catch {
      setPersistenceNotice('已退出登录，但记住密码未能清除');
    }
  }, [rememberedLoginStore]);

  // Central reaction to session phase changes.
  useEffect(() => {
    const unsubscribe = session.subscribe(() => {
      const snap = session.getSnapshot();
      if (
        (snap.phase === 'signed-out' || snap.phase === 'unconfigured') &&
        route.name !== 'signin'
      ) {
        setRoute({name: 'signin'});
        setMasked(false);
      } else if (snap.phase === 'authenticated' && route.name === 'signin') {
        setSignOutNotice(null);
        setRoute({name: 'vault'});
      }
    });
    return unsubscribe;
  }, [session, route.name]);

  // Foreground resume: mask, revalidate, then reveal.
  useEffect(() => {
    const subscription = AppState.addEventListener('change', state => {
      if (state === 'background') {
        if (session.getSnapshot().phase === 'authenticated') {
          setMasked(true);
        }
        return;
      }
      if (state === 'active' && masked && !resumeValidationRef.current) {
        resumeValidationRef.current = true;
        void session
          .validateOnResume()
          .then(outcome => {
            if (outcome === 'ok') {
              setMasked(false);
            } else if (outcome === 'signed-out') {
              setMasked(false); // phase subscription routes to sign-in
            } else {
              // unreachable: keep the mask, show retry affordance
              setResumeUnreachable(true);
            }
          })
          .finally(() => {
            resumeValidationRef.current = false;
          });
      }
    });
    return () => subscription.remove();
  }, [session, masked]);

  const onActivity = useCallback(() => {
    void session.touchActivity();
  }, [session]);

  const handleSignOutFromVault = useCallback(() => {
    void session.signOut().then(async result => {
      await clearRememberedCredentials();
      setSignOutNotice(result.notice);
      setRoute({name: 'signin'});
      // Refresh pre-auth context for the next login.
      void session.preflight();
    });
  }, [clearRememberedCredentials, session]);

  const closeEditor = useCallback(
    (mutation?: 'created' | 'trashed') => {
      setRoute({name: 'vault'});
      if (mutation === 'trashed') {
        setEditorNotice('已移入回收站；可在保留期内通过 Web 回收站恢复。');
        setRefreshKey(k => k + 1);
      } else if (mutation === 'created') {
        setEditorNotice('条目已创建。');
        setRefreshKey(k => k + 1);
      }
    },
    [],
  );

  const snap = session.getSnapshot();
  const api = session.getApi();

  return (
    <SafeAreaProvider>
      <StatusBar barStyle="dark-content" />
      <View style={styles.root}>
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
            initialNotice={signOutNotice}
            persistenceNotice={persistenceNotice}
            onRememberedServerUrlSaved={saveRememberedServerUrl}
            onRememberedCredentialsSaved={saveRememberedCredentials}
            onRememberedCredentialsCleared={clearRememberedCredentials}
            onAuthenticated={() => setRoute({name: 'vault'})}
            onActivity={onActivity}
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
        ) : (
          api && (
            <VaultScreen
              session={session}
              api={api}
              refreshKey={refreshKey}
              notice={editorNotice}
              onOpenEntry={itemId => setRoute({name: 'editor', editor: {mode: 'detail', itemId}})}
              onAddEntry={() => setRoute({name: 'editor', editor: {mode: 'create'}})}
              onOpenGenerator={() => setRoute({name: 'generator'})}
              onSignOut={handleSignOutFromVault}
              onActivity={onActivity}
            />
          )
        )}

        {masked ? (
          <View accessibilityViewIsModal style={styles.mask} testID="resume-mask">
            <Text style={styles.maskBrand}>tiny-password</Text>
            <Text style={styles.maskText}>内容已隐藏</Text>
            <Text style={styles.maskHint}>
              {resumeUnreachable
                ? '无法连接服务器，暂时无法确认会话。请检查网络后重试。'
                : '正在校验会话…'}
            </Text>
            {resumeUnreachable ? (
              <View style={styles.maskRetry}>
                <NewsprintButton
                  label="RETRY"
                  variant="secondary"
                  onPress={() => {
                    // Keep the mask up until the server confirms the session.
                    setResumeUnreachable(false);
                    void session.validateOnResume().then(outcome => {
                      if (outcome === 'unreachable') {
                        setResumeUnreachable(true);
                      } else {
                        setMasked(false);
                      }
                    });
                  }}
                />
              </View>
            ) : null}
          </View>
        ) : null}
      </View>
    </SafeAreaProvider>
  );
}

const styles = StyleSheet.create({
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
