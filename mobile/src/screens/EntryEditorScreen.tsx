import React, {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {
  ActivityIndicator,
  BackHandler,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import {useSafeAreaInsets} from 'react-native-safe-area-context';
import {NewsprintHeader} from '../components/NewsprintHeader';
import {NewsprintInput} from '../components/NewsprintInput';
import {NewsprintButton} from '../components/NewsprintButton';
import {CopyValueRow} from '../components/CopyValueRow';
import {Banner} from '../components/Banner';
import {ConfirmDialog} from '../components/ConfirmDialog';
import {UrlListField} from '../components/UrlListField';
import {PasswordGeneratorSheet} from '../components/PasswordGeneratorSheet';
import {colors} from '../theme/colors';
import {fonts, typeScale} from '../theme/typography';
import {borderHeavy, pageMargin, spacing} from '../theme/spacing';
import type {ItemDetail} from '../api/types';
import type {TinyPasswordApi} from '../api/client';
import {LatestTracker} from '../api/async';
import {
  editsFromDetail,
  emptyEdits,
  mergeLoginPayload,
  normalizedUrls,
  validateLoginEdits,
  type FieldErrors,
  type LoginEdits,
} from '../vault/payloadMerge';
import {createIdempotencyKeyManager, canonicalCreateContent} from '../vault/idempotency';
import type {SessionController} from '../auth/session';

export type EditorRoute =
  | {mode: 'create'}
  | {mode: 'detail'; itemId: string};

interface EntryEditorScreenProps {
  session: SessionController;
  api: TinyPasswordApi;
  route: EditorRoute;
  onClose: (mutation?: 'created' | 'trashed') => void;
  onActivity: () => void;
}

type LoadState = 'loading' | 'ready' | 'error';

/**
 * Entry detail / edit / create — one screen for all three (design §6).
 * View mode shows every URL with copy; edit mode merges changes onto the
 * ORIGINAL payload so unshown date fields survive; saves carry the read
 * revision and a REVISION_CONFLICT keeps the draft on screen.
 */
export function EntryEditorScreen({
  session,
  api,
  route,
  onClose,
  onActivity,
}: EntryEditorScreenProps): React.JSX.Element {
  const insets = useSafeAreaInsets();
  const isCreate = route.mode === 'create';
  const csrf = session.csrfToken;

  const [detail, setDetail] = useState<ItemDetail | null>(null);
  const [loadState, setLoadState] = useState<LoadState>(isCreate ? 'ready' : 'loading');
  const [loadError, setLoadError] = useState<string | null>(null);

  const [mode, setMode] = useState<'view' | 'edit'>(isCreate ? 'edit' : 'view');
  const [edits, setEdits] = useState<LoginEdits>(emptyEdits());
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [initialSnapshot, setInitialSnapshot] = useState<string>(() => JSON.stringify(emptyEdits()));
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<{currentRevision: number} | null>(null);
  const [reloadConfirmVisible, setReloadConfirmVisible] = useState(false);
  const [discardConfirmVisible, setDiscardConfirmVisible] = useState(false);
  const [deleteConfirmVisible, setDeleteConfirmVisible] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [showPassword, setShowPassword] = useState(false);
  const [generatorVisible, setGeneratorVisible] = useState(false);
  const keyManager = useRef(createIdempotencyKeyManager());
  const [loadTracker] = useState(() => new LatestTracker());

  const detailRef = useRef<ItemDetail | null>(null);
  detailRef.current = detail;

  // --- detail loading ---------------------------------------------------------

  const loadDetail = useCallback(async () => {
    if (route.mode !== 'detail') {
      return;
    }
    const myId = loadTracker.next();
    setLoadState('loading');
    setLoadError(null);
    const result = await api.getItem(route.itemId);
    if (!loadTracker.isCurrent(myId)) {
      return;
    }
    if (result.kind === 'success') {
      const d = result.data;
      // Personal login items only: anything else must not open in this editor.
      if (d.item_type !== 'login' || d.vault_scope !== 'personal') {
        setLoadState('error');
        setLoadError('该条目不是个人登录类型，请通过 Web 使用。');
        return;
      }
      const principal = session.currentPrincipal;
      if (principal && d.owner_id && d.owner_id !== principal.user_id) {
        setLoadState('error');
        setLoadError('该条目不属于当前用户，请通过 Web 使用。');
        return;
      }
      setDetail(d);
      const e = editsFromDetail(d);
      setEdits(e);
      setInitialSnapshot(JSON.stringify(e));
      setLoadState('ready');
      setMode('view');
    } else if (result.kind === 'http-error') {
      setLoadState('error');
      setLoadError(
        result.status === 404
          ? '条目不存在或已移入回收站。'
          : result.status === 403
            ? '没有查看该条目的权限。'
            : result.error.message,
      );
    } else {
      setLoadState('error');
      setLoadError(result.kind === 'network-error' ? '无法连接服务器。' : '加载失败。');
    }
  }, [api, route, session, loadTracker]);

  useEffect(() => {
    void loadDetail();
    return () => {
      loadTracker.next();
    };
  }, [loadDetail, loadTracker]);

  // --- dirty tracking ---------------------------------------------------------

  const dirty = useMemo(() => JSON.stringify(edits) !== initialSnapshot, [edits, initialSnapshot]);

  // --- Android Back -----------------------------------------------------------

  const tryClose = useCallback(() => {
    if (mode === 'edit' && dirty) {
      setDiscardConfirmVisible(true);
      return;
    }
    onClose();
  }, [mode, dirty, onClose]);

  const tryCloseRef = useRef(tryClose);
  tryCloseRef.current = tryClose;
  useEffect(() => {
    const handler = () => {
      if (generatorVisible) {
        setGeneratorVisible(false);
        return true;
      }
      if (reloadConfirmVisible || discardConfirmVisible || deleteConfirmVisible) {
        return true;
      }
      tryCloseRef.current();
      return true;
    };
    const subscription = BackHandler.addEventListener('hardwareBackPress', handler);
    return () => subscription.remove();
  }, [generatorVisible, reloadConfirmVisible, discardConfirmVisible, deleteConfirmVisible]);

  // --- save (create / update) -------------------------------------------------

  const buildPayloadEdits = (): LoginEdits => ({
    ...edits,
    urls: normalizedUrls(edits),
  });

  const save = async () => {
    if (saving) {
      return;
    }
    onActivity();
    setSaveError(null);
    setConflict(null);
    const withUrls = buildPayloadEdits();
    const errors = validateLoginEdits(withUrls);
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) {
      return;
    }
    if (!csrf) {
      setSaveError('会话上下文缺失，请返回重试');
      return;
    }
    setSaving(true);
    if (isCreate) {
      const payload = mergeLoginPayload(
        {name: '', username: '', password: ''}, // no original payload exists
        withUrls,
      );
      const content = canonicalCreateContent(payload as unknown as Record<string, unknown>, {
        item_type: 'login',
        vault_scope: 'personal',
      });
      const key = keyManager.current.keyFor(content);
      const result = await api.createItem(payload, key, csrf);
      setSaving(false);
      if (result.kind === 'success') {
        keyManager.current.reset();
        onClose('created');
        return;
      }
      if (result.kind === 'http-error') {
        if (result.status === 401) {
          session.invalidateLocally();
          return;
        }
        setSaveError(result.error.message || '创建失败');
        return;
      }
      setSaveError(result.kind === 'network-error' ? '网络异常，尚未保存至服务端；草稿已保留，可重试。' : '创建失败');
      return;
    }

    const d = detailRef.current;
    if (!d) {
      setSaving(false);
      return;
    }
    const payload = mergeLoginPayload(d.payload, withUrls);
    const result = await api.updateItem(d.id, d.revision, payload, csrf);
    setSaving(false);
    if (result.kind === 'success') {
      const fresh = result.data;
      setDetail(fresh);
      const e = editsFromDetail(fresh);
      setEdits(e);
      setInitialSnapshot(JSON.stringify(e));
      setShowPassword(false); // §6.5: back to masked after leaving edit mode
      setMode('view');
      return;
    }
    if (result.kind === 'http-error') {
      if (result.status === 401) {
        session.invalidateLocally();
        return;
      }
      if (result.error.code === 'REVISION_CONFLICT') {
        setConflict({currentRevision: result.error.currentRevision ?? 0});
        return;
      }
      if (result.error.code === 'FORBIDDEN') {
        // Refresh the detail and re-assess, but never overwrite the draft
        // without confirmation (same rule as the 409 reload).
        setSaveError('没有修改该条目的权限。可重新加载服务端内容；当前修改将被覆盖。');
        setReloadConfirmVisible(true);
        return;
      }
      if (result.status === 404) {
        setSaveError('条目已不存在（可能已移入回收站）。');
        return;
      }
      setSaveError(result.error.message || '保存失败');
      return;
    }
    setSaveError(result.kind === 'network-error' ? '网络异常，尚未保存至服务端；草稿已保留，可重试。' : '保存失败');
  };

  // --- conflict resolution ----------------------------------------------------

  const reloadServerContent = async () => {
    setReloadConfirmVisible(false);
    setConflict(null);
    await loadDetail();
  };

  // --- delete (move to trash) -------------------------------------------------

  const doDelete = async () => {
    setDeleteConfirmVisible(false);
    if (!detail) {
      return;
    }
    if (!csrf) {
      setDeleteError('会话上下文缺失，请返回重试');
      return;
    }
    setDeleting(true);
    setDeleteError(null);
    const result = await api.trashItem(detail.id, csrf);
    setDeleting(false);
    if (result.kind === 'success') {
      onClose('trashed');
      return;
    }
    if (result.kind === 'http-error') {
      if (result.status === 401) {
        session.invalidateLocally();
        return;
      }
      setDeleteError(
        result.status === 404
          ? '条目已不存在，可能已被移入回收站。'
          : result.error.message || '移入回收站失败',
      );
      return;
    }
    setDeleteError('网络异常，移入回收站失败，请重试。');
  };

  // --- audit helpers (best effort; server records field category only) --------

  const audit = (kind: 'copy' | 'reveal') => {
    if (!detail || !csrf) {
      return;
    }
    void (kind === 'copy' ? api.auditCopy(detail.id, csrf) : api.auditReveal(detail.id, csrf));
  };

  // --- render -----------------------------------------------------------------

  if (loadState === 'loading') {
    return (
      <View style={[styles.root, {paddingTop: insets.top}]}>
        <NewsprintHeader title="ENTRY" kicker="PERSONAL VAULT" backLabel="Back" onBack={onClose} />
        <View style={styles.centered}>
          <ActivityIndicator size="small" color={colors.foreground} />
        </View>
      </View>
    );
  }

  if (loadState === 'error' || (!isCreate && !detail)) {
    return (
      <View style={[styles.root, {paddingTop: insets.top}]}>
        <NewsprintHeader title="ENTRY" kicker="PERSONAL VAULT" backLabel="Back" onBack={onClose} />
        <View style={styles.centered}>
          <Banner kind="error" text={loadError ?? '条目不可用'} />
          <NewsprintButton label="RETRY" variant="secondary" onPress={() => void loadDetail()} />
        </View>
      </View>
    );
  }

  const title = isCreate ? 'NEW ENTRY' : mode === 'edit' ? 'EDIT ENTRY' : detail!.payload.name || detail!.title || 'ENTRY';

  const headerActions = () => {
    if (isCreate || mode === 'edit') {
      return (
        <View style={styles.headerActions}>
          <View style={styles.headerActionButton}>
            <NewsprintButton
              label="CANCEL"
              variant="secondary"
              onPress={tryClose}
              disabled={saving}
              testID="editor-cancel"
            />
          </View>
          <View style={styles.headerActionButton}>
            <NewsprintButton
              label={saving ? 'SAVING…' : 'SAVE'}
              onPress={save}
              disabled={saving}
              loading={saving}
              testID="editor-save"
            />
          </View>
        </View>
      );
    }
    return (
      <View style={styles.headerActions}>
        <View style={styles.headerActionButton}>
          <NewsprintButton
            label="EDIT"
            variant="secondary"
            onPress={() => {
              onActivity();
              const e = editsFromDetail(detailRef.current!);
              setEdits(e);
              setInitialSnapshot(JSON.stringify(e));
              setMode('edit');
            }}
            testID="editor-edit"
          />
        </View>
      </View>
    );
  };

  return (
    <View style={[styles.root, {paddingBottom: insets.bottom}]}>
      <NewsprintHeader
        title={title}
        kicker={isCreate ? 'PERSONAL VAULT / CREATE' : `LOGIN / PERSONAL · REV ${detail!.revision}`}
        backLabel="Back"
        onBack={tryClose}
        testID="editor-header"
      />
      <View style={styles.headerActionsBar}>{headerActions()}</View>
      <View style={styles.rule} />

      <KeyboardAvoidingView
        behavior={Platform.OS === 'android' ? undefined : 'padding'}
        style={styles.flex}>
        <ScrollView
          contentContainerStyle={styles.content}
          keyboardShouldPersistTaps="handled">

          {conflict ? (
            <View style={styles.conflictBox} testID="conflict-box">
              <Banner
                kind="error"
                text="条目已在其他设备更新（REVISION_CONFLICT）。当前编辑已保留。"
                testID="conflict-banner"
              />
              <View style={styles.conflictActions}>
                <View style={styles.conflictAction}>
                  <NewsprintButton
                    label="CONTINUE EDITING"
                    variant="secondary"
                    onPress={() => setConflict(null)}
                  />
                </View>
                <View style={styles.conflictAction}>
                  <NewsprintButton
                    label="RELOAD SERVER CONTENT"
                    onPress={() => setReloadConfirmVisible(true)}
                    testID="conflict-reload"
                  />
                </View>
              </View>
            </View>
          ) : null}

          <Banner kind="error" text={saveError ?? ''} testID="save-error" />
          <Banner
            kind="info"
            text={mode === 'edit' && dirty && !conflict ? '有未保存的修改。' : ''}
          />

          {isCreate || mode === 'edit' ? (
            <View>
              <NewsprintInput
                label="TITLE"
                value={edits.name}
                onChangeText={name => setEdits(prev => ({...prev, name}))}
                error={fieldErrors.name}
                autoCapitalize="none"
                mono={false}
                maxLength={300}
                accessibilityLabel="标题"
                testID="field-title"
              />
              <NewsprintInput
                label="USERNAME"
                value={edits.username}
                onChangeText={username => setEdits(prev => ({...prev, username}))}
                error={fieldErrors.username}
                autoCapitalize="none"
                autoCorrect={false}
                accessibilityLabel="用户名"
                testID="field-username"
              />
              <View style={styles.passwordEditRow}>
                <View style={styles.passwordEditField}>
                  <NewsprintInput
                    label="PASSWORD"
                    value={edits.password}
                    onChangeText={password => setEdits(prev => ({...prev, password}))}
                    error={fieldErrors.password}
                    secure={!showPassword}
                    autoCapitalize="none"
                    autoCorrect={false}
                    textContentType="newPassword"
                    accessibilityLabel="密码"
                    testID="field-password"
                  />
                </View>
                <View style={styles.passwordEditActions}>
                  <NewsprintButton
                    label="GENERATE"
                    variant="secondary"
                    onPress={() => {
                      onActivity();
                      setGeneratorVisible(true);
                    }}
                    testID="open-generator"
                  />
                  <NewsprintButton
                    label={showPassword ? 'HIDE' : 'SHOW'}
                    variant="ghost"
                    onPress={() => setShowPassword(v => !v)}
                    accessibilityLabel={showPassword ? '隐藏密码' : '显示密码'}
                    style={styles.smallButton}
                  />
                </View>
              </View>
              <UrlListField
                urls={edits.urls}
                onChange={urls => setEdits(prev => ({...prev, urls}))}
                error={fieldErrors.urls}
              />
              <NewsprintInput
                label="NOTES"
                value={edits.notes}
                onChangeText={notes => setEdits(prev => ({...prev, notes}))}
                error={fieldErrors.notes}
                multiline
                mono={false}
                style={styles.notesInput}
                accessibilityLabel="备注"
                testID="field-notes"
              />
            </View>
          ) : (
            <View>
              <CopyValueRow
                label="TITLE"
                value={detail!.payload.name || detail!.title || ''}
                copyable={false}
                testID="view-title"
              />
              <CopyValueRow label="USERNAME" value={detail!.payload.username || ''} testID="view-username" />
              <View style={styles.viewPasswordRow}>
                <View style={styles.flex1}>
                  <CopyValueRow
                    label="PASSWORD"
                    value={detail!.payload.password || ''}
                    masked
                    visible={showPassword}
                    onToggleVisibility={() => {
                      const nextVisible = !showPassword;
                      setShowPassword(nextVisible);
                      if (nextVisible) {
                        audit('reveal');
                      }
                    }}
                    testID="view-password"
                  />
                </View>
              </View>
              {(detail!.payload.urls ?? []).length === 0 ? (
                <CopyValueRow label="URLS" value={null} copyable={false} />
              ) : (
                (detail!.payload.urls ?? []).map((url, index) => (
                  <CopyValueRow key={`${index}-${url}`} label={`URL ${index + 1}`} value={url} />
                ))
              )}
              <CopyValueRow label="NOTES" value={detail!.payload.notes || ''} copyable={false} />
            </View>
          )}

          {!isCreate ? (
            <View style={styles.dangerZone}>
              {deleteError ? <Banner kind="error" text={deleteError} actionLabel="重试" onAction={() => setDeleteConfirmVisible(true)} /> : null}
              <NewsprintButton
                label="MOVE TO TRASH"
                variant="danger"
                loading={deleting}
                onPress={() => {
                  setDeleteError(null);
                  setDeleteConfirmVisible(true);
                }}
                testID="move-to-trash"
              />
              <Text style={styles.dangerHint}>
                移入回收站并非永久删除；可在保留期内通过 Web 回收站恢复。
              </Text>
            </View>
          ) : null}
        </ScrollView>
      </KeyboardAvoidingView>

      <PasswordGeneratorSheet
        visible={generatorVisible}
        api={api}
        csrfToken={csrf}
        onClose={() => setGeneratorVisible(false)}
        onUse={value => {
          setEdits(prev => ({...prev, password: value}));
          setGeneratorVisible(false);
        }}
      />

      <ConfirmDialog
        visible={reloadConfirmVisible}
        title="重新加载服务端内容？"
        message="重新加载将覆盖当前未保存的修改。该操作无法撤销。"
        confirmLabel="RELOAD"
        cancelLabel="CANCEL"
        destructive
        onConfirm={() => void reloadServerContent()}
        onCancel={() => setReloadConfirmVisible(false)}
      />
      <ConfirmDialog
        visible={discardConfirmVisible}
        title="放弃修改？"
        message="当前修改尚未保存，返回将丢弃这些改动。"
        confirmLabel="DISCARD"
        cancelLabel="KEEP EDITING"
        destructive
        onConfirm={() => {
          setDiscardConfirmVisible(false);
          if (isCreate) {
            onClose();
          } else if (dirty) {
            // View mode: reload the pristine values from the loaded detail.
            const e = editsFromDetail(detailRef.current!);
            setEdits(e);
            setInitialSnapshot(JSON.stringify(e));
            setMode('view');
          }
        }}
        onCancel={() => setDiscardConfirmVisible(false)}
      />
      <ConfirmDialog
        visible={deleteConfirmVisible}
        title={`将 ${detail!.payload.name || detail!.title || '该条目'} 移入回收站？`}
        message={
          mode === 'edit' && dirty
            ? '未保存的修改将被丢弃。条目将移入回收站，可通过 Web 回收站在保留期内恢复。'
            : '条目将移入回收站，可通过 Web 回收站在保留期内恢复。'
        }
        confirmLabel="MOVE TO TRASH"
        cancelLabel="CANCEL"
        destructive
        onConfirm={() => void doDelete()}
        onCancel={() => setDeleteConfirmVisible(false)}
      />
    </View>
  );
}


const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.background,
  },
  flex: {
    flex: 1,
  },
  headerActionsBar: {
    paddingHorizontal: pageMargin,
    paddingTop: spacing.sm,
  },
  headerActions: {
    flexDirection: 'row',
    gap: spacing.sm,
  },
  headerActionButton: {
    flex: 1,
  },
  rule: {
    height: borderHeavy,
    backgroundColor: colors.foreground,
    marginHorizontal: pageMargin,
    marginTop: spacing.sm,
  },
  content: {
    paddingHorizontal: pageMargin,
    paddingBottom: spacing.xl,
  },
  centered: {
    flex: 1,
    justifyContent: 'center',
    paddingHorizontal: pageMargin,
  },
  conflictBox: {
    marginBottom: spacing.sm,
  },
  conflictActions: {
    flexDirection: 'row',
    gap: spacing.sm,
  },
  conflictAction: {
    flex: 1,
  },
  passwordEditRow: {
    flexDirection: 'row',
    alignItems: 'flex-end',
  },
  passwordEditField: {
    flex: 1,
  },
  passwordEditActions: {
    width: 130,
    gap: spacing.xs,
    paddingBottom: spacing.md,
  },
  smallButton: {
    minHeight: 44,
  },
  notesInput: {
    minHeight: 120,
  },
  viewPasswordRow: {
    flexDirection: 'row',
  },
  flex1: {
    flex: 1,
  },
  dangerZone: {
    marginTop: spacing.xl,
    paddingTop: spacing.md,
    borderTopWidth: borderHeavy,
    borderTopColor: colors.foreground,
  },
  dangerHint: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
    textAlign: 'center',
    marginTop: spacing.sm,
  },
});
