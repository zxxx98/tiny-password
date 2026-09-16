import React, {useCallback, useEffect, useRef, useState} from 'react';
import {
  ActivityIndicator,
  FlatList,
  Keyboard,
  Pressable,
  RefreshControl,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import {useSafeAreaInsets} from 'react-native-safe-area-context';
import {NewsprintButton} from '../components/NewsprintButton';
import {Banner} from '../components/Banner';
import {ConfirmDialog} from '../components/ConfirmDialog';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {
  borderHeavy,
  borderWidth,
  minTouchTarget,
  pageMargin,
  rowPaddingV,
  spacing,
} from '../theme/spacing';
import type {ItemMeta} from '../api/types';
import type {TinyPasswordApi} from '../api/client';
import {LatestTracker} from '../api/async';
import {searchQueryError} from '../vault/validation';
import type {SessionController} from '../auth/session';

interface VaultScreenProps {
  session: SessionController;
  api: TinyPasswordApi;
  refreshKey: number;
  /** One-shot info banner, e.g. “已移入回收站” after a trash action. */
  notice: string | null;
  onOpenEntry: (itemId: string) => void;
  onAddEntry: () => void;
  onOpenGenerator: () => void;
  onSignOut: () => void;
  onActivity: () => void;
}

const SEARCH_DEBOUNCE_MS = 300;
const PAGE_SIZE = 50;

type ListStatus = 'loading' | 'ready' | 'error';

/**
 * Readable vault: entry list built purely from server Meta, server-side
 * search with debounce + stale-response isolation, cursor pagination, and
 * sign out. The server supplies this user's personal items and readable
 * shared items across every supported type.
 */
export function VaultScreen({
  session,
  api,
  refreshKey,
  notice,
  onOpenEntry,
  onAddEntry,
  onOpenGenerator,
  onSignOut,
  onActivity,
}: VaultScreenProps): React.JSX.Element {
  const insets = useSafeAreaInsets();
  const [query, setQuery] = useState('');
  const [mode, setMode] = useState<'list' | 'search'>('list');
  const [items, setItems] = useState<ItemMeta[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [status, setStatus] = useState<ListStatus>('loading');
  const [errorText, setErrorText] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [moreError, setMoreError] = useState<string | null>(null);
  const [signOutConfirm, setSignOutConfirm] = useState(false);
  const tracker = useRef(new LatestTracker());
  const abortRef = useRef<AbortController | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const searchSeq = useRef(0);

  const clearRequest = () => {
    if (abortRef.current) {
      abortRef.current.abort();
      abortRef.current = null;
    }
    if (debounceRef.current !== null) {
      clearTimeout(debounceRef.current);
      debounceRef.current = null;
    }
  };

  const load = useCallback(
    async (opts: {query: string; cursor?: string | null; isRefresh?: boolean}) => {
      const id = tracker.current.next();
      if (opts.isRefresh) {
        setRefreshing(true);
        setErrorText(null);
      } else if (!opts.cursor) {
        setStatus('loading');
        setErrorText(null);
      } else {
        setLoadingMore(true);
        setMoreError(null);
      }
      if (abortRef.current) {
        abortRef.current.abort();
      }
      const controller = new AbortController();
      abortRef.current = controller;
      const trimmed = opts.query.trim();
      const result = trimmed
        ? await api.searchItems(trimmed, opts.cursor ?? null, session.csrfToken ?? '', controller.signal, PAGE_SIZE)
        : await api.listItems(opts.cursor ?? null, PAGE_SIZE, controller.signal);
      if (!tracker.current.isCurrent(id)) {
        setRefreshing(false);
        return; // stale: a newer query or pagination step superseded this
      }
      if (result.kind === 'success') {
        setMode(trimmed ? 'search' : 'list');
        setItems(prev => (opts.cursor ? [...prev, ...result.data.items] : result.data.items));
        setCursor(result.data.next_cursor ?? null);
        setStatus('ready');
        setErrorText(null);
      } else if (result.kind === 'http-error' && result.status === 401) {
        setRefreshing(false);
        session.invalidateLocally(); // App routes back to sign-in
        return;
      } else if (opts.cursor) {
        setMoreError(
          result.kind === 'network-error' ? '加载更多失败，请重试' : result.kind === 'http-error' ? result.error.message : '',
        );
      } else {
        setStatus('error');
        setErrorText(
          result.kind === 'network-error'
            ? '无法连接服务器，请检查网络'
            : result.kind === 'http-error'
              ? result.error.message
              : '加载失败',
        );
      }
      setLoadingMore(false);
      setRefreshing(false);
    },
    [api, session],
  );

  const queryRef = useRef(query);
  queryRef.current = query;

  // First load + reload after external changes (create/delete elsewhere).
  useEffect(() => {
    clearRequest();
    tracker.current.next(); // invalidate any pending response
    setItems([]);
    setCursor(null);
    const current = queryRef.current.trim();
    void load({query: current});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshKey]);

  // Debounced search on query change.
  const changeQuery = (text: string) => {
    setQuery(text);
    if (debounceRef.current !== null) {
      clearTimeout(debounceRef.current);
    }
    const trimmed = text.trim();
    if (trimmed.length === 0) {
      // Pure-whitespace or empty query restores the plain list.
      tracker.current.next();
      setMode('list');
      setItems([]);
      setCursor(null);
      void load({query: ''});
      return;
    }
    const limitError = searchQueryError(trimmed);
    if (limitError) {
      setStatus('error');
      setErrorText(limitError);
      return;
    }
    searchSeq.current += 1;
    const seq = searchSeq.current;
    debounceRef.current = setTimeout(() => {
      if (seq === searchSeq.current) {
        void load({query: trimmed});
      }
    }, SEARCH_DEBOUNCE_MS);
  };

  const clearSearch = () => {
    setQuery('');
    changeQuery('');
    Keyboard.dismiss();
  };

  const loadMore = () => {
    if (loadingMore || !cursor || status !== 'ready') {
      return;
    }
    onActivity();
    void load({query: queryRef.current, cursor});
  };

  const retryFirstLoad = () => {
    onActivity();
    const trimmed = queryRef.current.trim();
    const limitError = searchQueryError(trimmed);
    if (limitError) {
      setStatus('error');
      setErrorText(limitError);
      return;
    }
    void load({query: trimmed});
  };

  useEffect(() => {
    const trackerRef = tracker.current;
    return () => {
      clearRequest();
      trackerRef.next();
    };
  }, []);

  const isLoadingFirst = status === 'loading';
  const showCount = status === 'ready' && items.length > 0;

  return (
    <View style={[styles.root, {paddingBottom: insets.bottom}]}>
      <View style={[styles.header, {paddingTop: insets.top}]}>
        <View style={styles.headerRow}>
          <View style={styles.flex}>
            <Text style={styles.brand}>tiny-password</Text>
            <Text style={styles.kicker}>PERSONAL VAULT</Text>
          </View>
          <View style={styles.headerActions}>
            <Pressable
              accessibilityRole="button"
              accessibilityLabel="打开独立密码生成器"
              onPress={onOpenGenerator}
              hitSlop={8}
              style={styles.signOutButton}
              testID="open-generator">
              <Text style={styles.signOutText}>GENERATOR</Text>
            </Pressable>
            <Pressable
              accessibilityRole="button"
              accessibilityLabel="退出登录"
              onPress={() => setSignOutConfirm(true)}
              hitSlop={8}
              style={styles.signOutButton}
              testID="sign-out">
              <Text style={styles.signOutText}>SIGN OUT</Text>
            </Pressable>
          </View>
        </View>
        <View style={styles.rule} />
        <View style={styles.searchRow}>
          <TextInput
            style={styles.searchInput}
            value={query}
            onChangeText={changeQuery}
            placeholder="Search entries..."
            placeholderTextColor={colors.neutral400}
            autoCapitalize="none"
            autoCorrect={false}
            returnKeyType="search"
            accessibilityLabel="搜索密码条目"
            testID="vault-search"
          />
          {query.length > 0 ? (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel="清空搜索"
              onPress={clearSearch}
              hitSlop={8}
              style={styles.clearButton}
              testID="search-clear">
              <Text style={styles.clearText}>CLEAR</Text>
            </Pressable>
          ) : null}
        </View>
      </View>

      {notice ? <Banner kind="info" text={notice} /> : null}

      {isLoadingFirst ? (
        <View style={styles.centered}>
          <ActivityIndicator size="small" color={colors.foreground} />
          <Text style={styles.loadingText}>LOADING VAULT…</Text>
        </View>
      ) : status === 'error' ? (
        <View style={styles.centered}>
          <Banner kind="error" text={errorText ?? '加载失败'} />
          <NewsprintButton label="RETRY" variant="secondary" onPress={retryFirstLoad} testID="vault-retry" />
        </View>
      ) : (
        <FlatList
          data={items}
          keyExtractor={item => item.id}
          contentContainerStyle={styles.listContent}
          keyboardShouldPersistTaps="handled"
          refreshControl={
            <RefreshControl
              refreshing={refreshing}
              onRefresh={() => {
                onActivity();
                void load({query: queryRef.current.trim(), isRefresh: true});
              }}
              tintColor={colors.foreground}
              titleColor={colors.foreground}
            />
          }
          ListEmptyComponent={
            <View style={styles.empty}>
              <Text style={styles.emptyTitle}>{mode === 'search' ? 'NO MATCHES' : 'NO ENTRIES'}</Text>
              <Text style={styles.emptyText}>
                {mode === 'search' ? '没有匹配的可见条目。' : '可见保险库中还没有条目，点击下方新增。'}
              </Text>
            </View>
          }
          renderItem={({item}) => (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`打开条目 ${item.title ?? item.id}`}
              onPress={() => {
                onActivity();
                onOpenEntry(item.id);
              }}
              android_ripple={{color: colors.muted}}
              style={styles.row}
              testID={`vault-item-${item.id}`}>
              <Text style={styles.rowTitle} numberOfLines={1}>
                {item.title || '（无标题）'}
              </Text>
              <Text style={styles.rowMeta}>
                {item.item_type.toUpperCase()} / {item.vault_scope.toUpperCase()}
              </Text>
            </Pressable>
          )}
          ListFooterComponent={
            <>
              {showCount ? (
                <Text style={styles.count}>已加载 {items.length} 条</Text>
              ) : null}
              {cursor && status === 'ready' ? (
                <View style={styles.loadMoreWrap}>
                  {moreError ? <Banner kind="error" text={moreError} actionLabel="重试" onAction={loadMore} /> : null}
                  <NewsprintButton
                    label={loadingMore ? 'LOADING…' : 'LOAD MORE'}
                    variant="secondary"
                    onPress={loadMore}
                    disabled={loadingMore}
                    testID="load-more"
                  />
                </View>
              ) : null}
            </>
          }
        />
      )}

      <View style={styles.addRow}>
        <NewsprintButton label="+ ADD ENTRY" onPress={() => {
          onActivity();
          onAddEntry();
        }} testID="add-entry" />
      </View>

      <ConfirmDialog
        visible={signOutConfirm}
        title="退出登录？"
        message="将撤销当前会话并返回登录页。"
        confirmLabel="SIGN OUT"
        cancelLabel="CANCEL"
        onConfirm={() => {
          setSignOutConfirm(false);
          onSignOut();
        }}
        onCancel={() => setSignOutConfirm(false)}
      />
    </View>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.background,
  },
  header: {
    paddingHorizontal: pageMargin,
    backgroundColor: colors.background,
  },
  headerRow: {
    flexDirection: 'row',
    alignItems: 'center',
    minHeight: minTouchTarget,
    marginTop: spacing.sm,
  },
  flex: {
    flex: 1,
  },
  brand: {
    ...typeScale.h2,
    fontFamily: fonts.display,
    color: colors.foreground,
  },
  kicker: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.wide,
    color: colors.neutral600,
    marginTop: 2,
  },
  signOutButton: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    paddingHorizontal: spacing.sm,
  },
  headerActions: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.xs,
  },
  signOutText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
    textDecorationLine: 'underline',
  },
  rule: {
    height: borderHeavy,
    backgroundColor: colors.foreground,
    marginTop: spacing.sm,
  },
  searchRow: {
    flexDirection: 'row',
    alignItems: 'center',
    borderBottomWidth: borderHeavy,
    borderBottomColor: colors.foreground,
    marginVertical: spacing.md,
  },
  searchInput: {
    flex: 1,
    minHeight: minTouchTarget,
    fontFamily: fonts.mono,
    fontSize: 15,
    color: colors.foreground,
    paddingVertical: spacing.sm,
  },
  clearButton: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    paddingHorizontal: spacing.sm,
  },
  clearText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  listContent: {
    paddingBottom: spacing.lg,
  },
  row: {
    paddingHorizontal: pageMargin,
    paddingVertical: rowPaddingV,
    borderBottomWidth: borderWidth,
    borderBottomColor: colors.foreground,
    minHeight: minTouchTarget + 12,
    justifyContent: 'center',
  },
  rowTitle: {
    ...typeScale.h3,
    fontFamily: fonts.display,
    color: colors.foreground,
  },
  rowMeta: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.neutral600,
    marginTop: 4,
  },
  empty: {
    padding: spacing.xl,
    alignItems: 'center',
  },
  emptyTitle: {
    ...typeScale.h3,
    fontFamily: fonts.display,
    color: colors.foreground,
    marginBottom: spacing.sm,
  },
  emptyText: {
    ...typeScale.body,
    fontFamily: fonts.body,
    color: colors.neutral600,
    textAlign: 'center',
  },
  centered: {
    flex: 1,
    justifyContent: 'center',
    paddingHorizontal: pageMargin,
  },
  loadingText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.neutral600,
    textAlign: 'center',
    marginTop: spacing.md,
  },
  count: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
    textAlign: 'center',
    marginTop: spacing.md,
  },
  loadMoreWrap: {
    paddingHorizontal: pageMargin,
    marginTop: spacing.md,
  },
  addRow: {
    paddingHorizontal: pageMargin,
    paddingVertical: spacing.md,
    borderTopWidth: borderHeavy,
    borderTopColor: colors.foreground,
    backgroundColor: colors.background,
  },
});
