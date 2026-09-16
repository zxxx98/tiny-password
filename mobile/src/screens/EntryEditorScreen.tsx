import React, {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {
  ActivityIndicator,
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
import {borderHeavy, borderWidth, minTouchTarget, pageMargin, spacing} from '../theme/spacing';
import type {AuditField} from '../api/client';
import type {
  CreditCardPayload,
  IdentityPayload,
  ItemDetail,
  ItemType,
  LoginPayload,
  SecureNotePayload,
  SecretPayload,
  SshKeyPayload,
} from '../api/types';
import type {TinyPasswordApi} from '../api/client';
import {LatestTracker} from '../api/async';
import {
  editsFromDetail,
  emptyEdits,
  mergePayload,
  payloadFromEdits,
  validateEdits,
  type CreditCardEdits,
  type EntryEdits,
  type FieldErrors,
  type IdentityEdits,
  type LoginEdits,
  type SecureNoteEdits,
  type SecretEdits,
  type SecretEntryEdits,
  type SshKeyEdits,
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

const ITEM_TYPES: {value: ItemType; label: string}[] = [
  {value: 'login', label: 'LOGIN'},
  {value: 'ssh_key', label: 'SSH KEY'},
  {value: 'credit_card', label: 'CREDIT CARD'},
  {value: 'identity', label: 'IDENTITY'},
  {value: 'secure_note', label: 'NOTE'},
  {value: 'secret', label: 'SECRET'},
];

const SSH_ALGORITHMS: {value: 'ed25519' | 'rsa4096'; label: string}[] = [
  {value: 'ed25519', label: 'ED25519'},
  {value: 'rsa4096', label: 'RSA4096'},
];

/**
 * Entry detail / edit / create — one screen for all item types (design §6).
 * Every supported type (login, ssh_key, credit_card, identity, secure_note,
 * secret) can be created and viewed here; personal items can also be edited.
 * Shared items remain read-only while sensitive fields are masked with audited
 * reveal/copy. Saves merge changes onto the ORIGINAL payload so unshown fields
 * (login password dates, card billing address reference) survive; saves carry
 * the read revision and a REVISION_CONFLICT keeps the draft on screen.
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

  const [itemType, setItemType] = useState<ItemType>('login');
  const [mode, setMode] = useState<'view' | 'edit'>(isCreate ? 'edit' : 'view');
  const [edits, setEdits] = useState<EntryEdits>(emptyEdits('login'));
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({});
  const [initialSnapshot, setInitialSnapshot] = useState<string>(() => JSON.stringify(emptyEdits('login')));
  const [showSecrets, setShowSecrets] = useState<Record<string, boolean>>({});
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<{currentRevision: number} | null>(null);
  const [reloadConfirmVisible, setReloadConfirmVisible] = useState(false);
  const [discardConfirmVisible, setDiscardConfirmVisible] = useState(false);
  const [deleteConfirmVisible, setDeleteConfirmVisible] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
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
      // The server has already authorized this detail read. Shared items are
      // displayed below as read-only; only personal items require the extra
      // owner check because a shared item's owner may be another user.
      const principal = session.currentPrincipal;
      if (d.vault_scope === 'personal' && principal && d.owner_id && d.owner_id !== principal.user_id) {
        setLoadState('error');
        setLoadError('该条目不属于当前用户，请通过 Web 使用。');
        return;
      }
      setDetail(d);
      setItemType(d.item_type);
      const e = editsFromDetail(d);
      setEdits(e);
      setInitialSnapshot(JSON.stringify(e));
      setLoadState('ready');
      setMode('view');
      setShowSecrets({});
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

  // --- edit state helpers ------------------------------------------------------

  const patch = (partial: Record<string, string | string[]>) => {
    setEdits(prev => ({...prev, ...partial}) as EntryEdits);
  };

  const patchEntry = (index: number, partial: Partial<SecretEntryEdits>) => {
    setEdits(prev => {
      const s = prev as SecretEdits;
      const entries = s.entries.map((e, i) => (i === index ? {...e, ...partial} : e));
      return {...s, entries} as EntryEdits;
    });
  };

  const switchType = (type: ItemType) => {
    if (type === itemType) {
      return;
    }
    onActivity();
    setItemType(type);
    setEdits(emptyEdits(type));
    setFieldErrors({});
    setSaveError(null);
  };

  const toggleReveal = (key: string, auditField?: AuditField) => {
    const nextVisible = !showSecrets[key];
    setShowSecrets(prev => ({...prev, [key]: nextVisible}));
    if (nextVisible && auditField) {
      audit(auditField, 'reveal');
    }
  };

  // --- save (create / update) -------------------------------------------------

  const save = async () => {
    if (saving) {
      return;
    }
    onActivity();
    setSaveError(null);
    setConflict(null);
    if (!isCreate && detailRef.current?.vault_scope !== 'personal') {
      setSaveError('共享条目仅可查看，修改请通过 Web 使用。');
      return;
    }
    const errors = validateEdits(itemType, edits);
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
      const payload = payloadFromEdits(itemType, edits);
      const content = canonicalCreateContent(payload as unknown as Record<string, unknown>, {
        item_type: itemType,
        vault_scope: 'personal',
      });
      const key = keyManager.current.keyFor(content);
      const result = await api.createItem(payload, itemType, key, csrf);
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
    const payload = mergePayload(itemType, d.payload, edits);
    const result = await api.updateItem(d.id, d.revision, payload, csrf);
    setSaving(false);
    if (result.kind === 'success') {
      const fresh = result.data;
      setDetail(fresh);
      const e = editsFromDetail(fresh);
      setEdits(e);
      setInitialSnapshot(JSON.stringify(e));
      setShowSecrets({}); // §6.5: back to masked after leaving edit mode
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
    if (detail.vault_scope !== 'personal') {
      setDeleteError('共享条目仅可查看，移入回收站请通过 Web 使用。');
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

  const audit = (field: AuditField, kind: 'copy' | 'reveal') => {
    if (!detail || !csrf) {
      return;
    }
    void (kind === 'copy' ? api.auditCopy(detail.id, field, csrf) : api.auditReveal(detail.id, field, csrf));
  };

  // --- render -----------------------------------------------------------------

  if (loadState === 'loading') {
    return (
      <View style={styles.root}>
        <NewsprintHeader title="ENTRY" kicker="READABLE VAULT" backLabel="Back" onBack={onClose} />
        <View style={styles.centered}>
          <ActivityIndicator size="small" color={colors.foreground} />
        </View>
      </View>
    );
  }

  if (loadState === 'error' || (!isCreate && !detail)) {
    return (
      <View style={styles.root}>
        <NewsprintHeader title="ENTRY" kicker="READABLE VAULT" backLabel="Back" onBack={onClose} />
        <View style={styles.centered}>
          <Banner kind="error" text={loadError ?? '条目不可用'} />
          <NewsprintButton label="RETRY" variant="secondary" onPress={() => void loadDetail()} />
        </View>
      </View>
    );
  }

  const typeLabel = ITEM_TYPES.find(t => t.value === itemType)?.label ?? itemType.toUpperCase();
  const isShared = !isCreate && detail!.vault_scope === 'shared';
  const title = isCreate
    ? 'NEW ENTRY'
    : mode === 'edit'
      ? 'EDIT ENTRY'
      : detail!.payload.name || detail!.title || 'ENTRY';

  const headerActions = () => {
    if (isCreate || (mode === 'edit' && !isShared)) {
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
    if (isShared) {
      return null;
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
              setFieldErrors({});
              setMode('edit');
            }}
            testID="editor-edit"
          />
        </View>
      </View>
    );
  };

  const renderEditFields = () => {
    switch (itemType) {
      case 'login': {
        const e = edits as LoginEdits;
        return (
          <View>
            <NewsprintInput
              label="TITLE"
              value={e.name}
              onChangeText={name => patch({name})}
              error={fieldErrors.name}
              autoCapitalize="none"
              mono={false}
              maxLength={300}
              accessibilityLabel="标题"
              testID="field-title"
            />
            <NewsprintInput
              label="USERNAME"
              value={e.username}
              onChangeText={username => patch({username})}
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
                  value={e.password}
                  onChangeText={password => patch({password})}
                  error={fieldErrors.password}
                  secure={!showSecrets['edit-password']}
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
                  label={showSecrets['edit-password'] ? 'HIDE' : 'SHOW'}
                  variant="ghost"
                  onPress={() => toggleReveal('edit-password')}
                  accessibilityLabel={showSecrets['edit-password'] ? '隐藏密码' : '显示密码'}
                  style={styles.smallButton}
                />
              </View>
            </View>
            <UrlListField
              urls={e.urls}
              onChange={urls => patch({urls})}
              error={fieldErrors.urls}
            />
            <NewsprintInput
              label="NOTES"
              value={e.notes}
              onChangeText={notes => patch({notes})}
              error={fieldErrors.notes}
              multiline
              mono={false}
              style={styles.notesInput}
              accessibilityLabel="备注"
              testID="field-notes"
            />
          </View>
        );
      }
      case 'ssh_key': {
        const e = edits as SshKeyEdits;
        return (
          <View>
            <NewsprintInput
              label="TITLE"
              value={e.name}
              onChangeText={name => patch({name})}
              error={fieldErrors.name}
              mono={false}
              maxLength={300}
              accessibilityLabel="标题"
              testID="field-title"
            />
            <View style={styles.choiceRow}>
              <Text style={styles.choiceLabel}>ALGORITHM</Text>
              <View style={styles.choiceOptions}>
                {SSH_ALGORITHMS.map(a => {
                  const selected = e.algorithm === a.value;
                  return (
                    <Pressable
                      key={a.value}
                      accessibilityRole="button"
                      accessibilityState={{selected}}
                      onPress={() => patch({algorithm: a.value})}
                      style={[styles.choiceChip, selected && styles.choiceChipSelected]}
                      testID={`field-algorithm-${a.value}`}>
                      <Text style={[styles.choiceChipText, selected && styles.choiceChipTextSelected]}>
                        {a.label}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
              {fieldErrors.algorithm ? <Text style={styles.fieldErrorText}>{fieldErrors.algorithm}</Text> : null}
            </View>
            <NewsprintInput
              label="PUBLIC KEY"
              value={e.public_key}
              onChangeText={public_key => patch({public_key})}
              error={fieldErrors.public_key}
              multiline
              autoCapitalize="none"
              autoCorrect={false}
              accessibilityLabel="公钥"
              testID="field-public-key"
            />
            <SecureEditField
              label="PRIVATE KEY"
              value={e.private_key}
              onChangeText={private_key => patch({private_key})}
              error={fieldErrors.private_key}
              secretKey="edit-private_key"
              showSecrets={showSecrets}
              onToggle={toggleReveal}
              accessibilityLabel="私钥"
              testID="field-private-key"
            />
            <SecureEditField
              label="PASSPHRASE"
              value={e.key_passphrase}
              onChangeText={key_passphrase => patch({key_passphrase})}
              error={fieldErrors.key_passphrase}
              secretKey="edit-key_passphrase"
              showSecrets={showSecrets}
              onToggle={toggleReveal}
              accessibilityLabel="私钥口令"
              testID="field-passphrase"
            />
            <NewsprintInput
              label="COMMENT"
              value={e.comment}
              onChangeText={comment => patch({comment})}
              error={fieldErrors.comment}
              autoCapitalize="none"
              accessibilityLabel="注释"
              testID="field-comment"
            />
            <NewsprintInput
              label="FINGERPRINT"
              value={e.fingerprint}
              onChangeText={fingerprint => patch({fingerprint})}
              error={fieldErrors.fingerprint}
              autoCapitalize="none"
              accessibilityLabel="指纹"
              testID="field-fingerprint"
            />
            <NewsprintInput
              label="NOTES"
              value={e.notes}
              onChangeText={notes => patch({notes})}
              error={fieldErrors.notes}
              multiline
              mono={false}
              style={styles.notesInput}
              accessibilityLabel="备注"
              testID="field-notes"
            />
          </View>
        );
      }
      case 'credit_card': {
        const e = edits as CreditCardEdits;
        return (
          <View>
            <NewsprintInput
              label="TITLE"
              value={e.name}
              onChangeText={name => patch({name})}
              error={fieldErrors.name}
              mono={false}
              maxLength={300}
              accessibilityLabel="标题"
              testID="field-title"
            />
            <NewsprintInput
              label="CARDHOLDER"
              value={e.cardholder}
              onChangeText={cardholder => patch({cardholder})}
              error={fieldErrors.cardholder}
              mono={false}
              accessibilityLabel="持卡人"
              testID="field-cardholder"
            />
            <SecureEditField
              label="CARD NUMBER"
              value={e.number}
              onChangeText={number => patch({number})}
              error={fieldErrors.number}
              secretKey="edit-number"
              showSecrets={showSecrets}
              onToggle={toggleReveal}
              keyboardType="number-pad"
              accessibilityLabel="卡号"
              testID="field-card-number"
            />
            <View style={styles.splitRow}>
              <View style={styles.splitField}>
                <NewsprintInput
                  label="EXP MONTH"
                  value={e.exp_month}
                  onChangeText={exp_month => patch({exp_month})}
                  error={fieldErrors.exp_month}
                  keyboardType="number-pad"
                  maxLength={4}
                  accessibilityLabel="有效月份"
                  testID="field-exp-month"
                />
              </View>
              <View style={styles.splitField}>
                <NewsprintInput
                  label="EXP YEAR"
                  value={e.exp_year}
                  onChangeText={exp_year => patch({exp_year})}
                  error={fieldErrors.exp_year}
                  keyboardType="number-pad"
                  maxLength={4}
                  accessibilityLabel="有效年份"
                  testID="field-exp-year"
                />
              </View>
            </View>
            <View style={styles.splitRow}>
              <View style={styles.splitField}>
                <SecureEditField
                  label="CVV"
                  value={e.cvv}
                  onChangeText={cvv => patch({cvv})}
                  error={fieldErrors.cvv}
                  secretKey="edit-cvv"
                  showSecrets={showSecrets}
                  onToggle={toggleReveal}
                  keyboardType="number-pad"
                  accessibilityLabel="CVV"
                  testID="field-cvv"
                />
              </View>
              <View style={styles.splitField}>
                <SecureEditField
                  label="PIN"
                  value={e.pin}
                  onChangeText={pin => patch({pin})}
                  error={fieldErrors.pin}
                  secretKey="edit-pin"
                  showSecrets={showSecrets}
                  onToggle={toggleReveal}
                  keyboardType="number-pad"
                  accessibilityLabel="PIN"
                  testID="field-pin"
                />
              </View>
            </View>
            <NewsprintInput
              label="NOTES"
              value={e.notes}
              onChangeText={notes => patch({notes})}
              error={fieldErrors.notes}
              multiline
              mono={false}
              style={styles.notesInput}
              accessibilityLabel="备注"
              testID="field-notes"
            />
          </View>
        );
      }
      case 'identity': {
        const e = edits as IdentityEdits;
        return (
          <View>
            <NewsprintInput
              label="TITLE"
              value={e.name}
              onChangeText={name => patch({name})}
              error={fieldErrors.name}
              mono={false}
              maxLength={300}
              accessibilityLabel="标题"
              testID="field-title"
            />
            <NewsprintInput
              label="FULL NAME"
              value={e.full_name}
              onChangeText={full_name => patch({full_name})}
              error={fieldErrors.full_name}
              mono={false}
              accessibilityLabel="姓名"
              testID="field-full-name"
            />
            <NewsprintInput
              label="COMPANY"
              value={e.company}
              onChangeText={company => patch({company})}
              error={fieldErrors.company}
              mono={false}
              accessibilityLabel="公司"
              testID="field-company"
            />
            <NewsprintInput
              label="PHONE"
              value={e.phone}
              onChangeText={phone => patch({phone})}
              error={fieldErrors.phone}
              keyboardType="phone-pad"
              accessibilityLabel="电话"
              testID="field-phone"
            />
            <NewsprintInput
              label="EMAIL"
              value={e.email}
              onChangeText={email => patch({email})}
              error={fieldErrors.email}
              autoCapitalize="none"
              keyboardType="email-address"
              accessibilityLabel="邮箱"
              testID="field-email"
            />
            <View style={styles.splitRow}>
              <View style={styles.splitField}>
                <NewsprintInput
                  label="COUNTRY"
                  value={e.country}
                  onChangeText={country => patch({country})}
                  error={fieldErrors.country}
                  mono={false}
                  accessibilityLabel="国家"
                  testID="field-country"
                />
              </View>
              <View style={styles.splitField}>
                <NewsprintInput
                  label="STATE"
                  value={e.state}
                  onChangeText={state => patch({state})}
                  error={fieldErrors.state}
                  mono={false}
                  accessibilityLabel="省份"
                  testID="field-state"
                />
              </View>
            </View>
            <View style={styles.splitRow}>
              <View style={styles.splitField}>
                <NewsprintInput
                  label="CITY"
                  value={e.city}
                  onChangeText={city => patch({city})}
                  error={fieldErrors.city}
                  mono={false}
                  accessibilityLabel="城市"
                  testID="field-city"
                />
              </View>
              <View style={styles.splitField}>
                <NewsprintInput
                  label="DISTRICT"
                  value={e.district}
                  onChangeText={district => patch({district})}
                  error={fieldErrors.district}
                  mono={false}
                  accessibilityLabel="区县"
                  testID="field-district"
                />
              </View>
            </View>
            <NewsprintInput
              label="ADDRESS"
              value={e.address_line}
              onChangeText={address_line => patch({address_line})}
              error={fieldErrors.address_line}
              mono={false}
              accessibilityLabel="地址"
              testID="field-address"
            />
            <NewsprintInput
              label="POSTAL CODE"
              value={e.postal_code}
              onChangeText={postal_code => patch({postal_code})}
              error={fieldErrors.postal_code}
              accessibilityLabel="邮编"
              testID="field-postal-code"
            />
            <NewsprintInput
              label="NOTES"
              value={e.notes}
              onChangeText={notes => patch({notes})}
              error={fieldErrors.notes}
              multiline
              mono={false}
              style={styles.notesInput}
              accessibilityLabel="备注"
              testID="field-notes"
            />
          </View>
        );
      }
      case 'secure_note': {
        const e = edits as SecureNoteEdits;
        return (
          <View>
            <NewsprintInput
              label="TITLE"
              value={e.name}
              onChangeText={name => patch({name})}
              error={fieldErrors.name}
              mono={false}
              maxLength={300}
              accessibilityLabel="标题"
              testID="field-title"
            />
            <NewsprintInput
              label="BODY"
              value={e.body}
              onChangeText={body => patch({body})}
              error={fieldErrors.body}
              multiline
              mono={false}
              style={styles.bodyInput}
              accessibilityLabel="正文"
              testID="field-body"
            />
          </View>
        );
      }
      case 'secret': {
        const e = edits as SecretEdits;
        return (
          <View>
            <NewsprintInput
              label="TITLE"
              value={e.name}
              onChangeText={name => patch({name})}
              error={fieldErrors.name}
              mono={false}
              maxLength={300}
              accessibilityLabel="标题"
              testID="field-title"
            />
            <Text style={styles.sectionLabel}>KEY-VALUE ENTRIES</Text>
            {e.entries.map((entry, index) => (
              <View key={index} style={styles.entryBlock}>
                <View style={styles.entryHeader}>
                  <Text style={styles.entryIndex}>{`#${index + 1}`}</Text>
                  {e.entries.length > 1 ? (
                    <Pressable
                      accessibilityRole="button"
                      accessibilityLabel={`删除第 ${index + 1} 组键值对`}
                      onPress={() =>
                        setEdits(prev => {
                          const s = prev as SecretEdits;
                          return {...s, entries: s.entries.filter((_, i) => i !== index)} as EntryEdits;
                        })
                      }
                      hitSlop={8}
                      style={styles.entryRemove}
                      testID={`remove-entry-${index}`}>
                      <Text style={styles.entryRemoveText}>REMOVE</Text>
                    </Pressable>
                  ) : null}
                </View>
                <NewsprintInput
                  label="KEY"
                  value={entry.key}
                  onChangeText={key => patchEntry(index, {key})}
                  error={fieldErrors[`entries.${index}.key`]}
                  autoCapitalize="none"
                  autoCorrect={false}
                  accessibilityLabel={`第 ${index + 1} 组键名`}
                  testID={`field-entry-key-${index}`}
                />
                <SecureEditField
                  label="VALUE"
                  value={entry.value}
                  onChangeText={value => patchEntry(index, {value})}
                  error={fieldErrors[`entries.${index}.value`]}
                  secretKey={`edit-entry-${index}`}
                  showSecrets={showSecrets}
                  onToggle={toggleReveal}
                  accessibilityLabel={`第 ${index + 1} 组值`}
                  testID={`field-entry-value-${index}`}
                />
              </View>
            ))}
            {fieldErrors.entries ? <Text style={styles.fieldErrorText}>{fieldErrors.entries}</Text> : null}
            <NewsprintButton
              label="+ ADD KEY-VALUE"
              variant="secondary"
              onPress={() =>
                setEdits(prev => {
                  const s = prev as SecretEdits;
                  return {...s, entries: [...s.entries, {key: '', value: ''}]} as EntryEdits;
                })
              }
              testID="add-entry-row"
            />
            <NewsprintInput
              label="NOTES"
              value={e.notes}
              onChangeText={notes => patch({notes})}
              error={fieldErrors.notes}
              multiline
              mono={false}
              style={styles.notesInput}
              accessibilityLabel="备注"
              testID="field-notes"
            />
          </View>
        );
      }
    }
  };

  const renderViewFields = () => {
    const p = detail!.payload;
    const revealRow = (
      label: string,
      value: string,
      secretKey: string,
      auditField?: AuditField,
      testID?: string,
    ) => (
      <CopyValueRow
        label={label}
        value={value}
        masked
        visible={!!showSecrets[secretKey]}
        onToggleVisibility={() => toggleReveal(secretKey, auditField)}
        onCopy={auditField ? () => audit(auditField, 'copy') : undefined}
        testID={testID}
      />
    );
    switch (itemType) {
      case 'login': {
        const l = p as LoginPayload;
        return (
          <View>
            <CopyValueRow
              label="TITLE"
              value={l.name || detail!.title || ''}
              copyable={false}
              testID="view-title"
            />
            <CopyValueRow label="USERNAME" value={l.username || ''} testID="view-username" />
            {revealRow('PASSWORD', l.password || '', 'password', 'password', 'view-password')}
            {(l.urls ?? []).length === 0 ? (
              <CopyValueRow label="URLS" value={null} copyable={false} />
            ) : (
              (l.urls ?? []).map((url, index) => (
                <CopyValueRow key={`${index}-${url}`} label={`URL ${index + 1}`} value={url} />
              ))
            )}
            <CopyValueRow label="NOTES" value={l.notes || ''} copyable={false} />
          </View>
        );
      }
      case 'ssh_key': {
        const k = p as SshKeyPayload;
        return (
          <View>
            <CopyValueRow label="TITLE" value={k.name || detail!.title || ''} copyable={false} testID="view-title" />
            <CopyValueRow label="ALGORITHM" value={k.algorithm} copyable={false} testID="view-algorithm" />
            <CopyValueRow label="PUBLIC KEY" value={k.public_key} testID="view-public-key" />
            {revealRow('PRIVATE KEY', k.private_key, 'private_key', 'private_key', 'view-private-key')}
            {revealRow('PASSPHRASE', k.key_passphrase || '', 'key_passphrase', 'key_passphrase', 'view-passphrase')}
            <CopyValueRow label="COMMENT" value={k.comment || ''} />
            <CopyValueRow label="FINGERPRINT" value={k.fingerprint || ''} />
            <CopyValueRow label="NOTES" value={k.notes || ''} copyable={false} />
          </View>
        );
      }
      case 'credit_card': {
        const c = p as CreditCardPayload;
        const expiry = `${String(c.exp_month).padStart(2, '0')}/${String(c.exp_year)}`;
        return (
          <View>
            <CopyValueRow label="TITLE" value={c.name || detail!.title || ''} copyable={false} testID="view-title" />
            <CopyValueRow label="CARDHOLDER" value={c.cardholder} testID="view-cardholder" />
            {revealRow('CARD NUMBER', c.number, 'number', 'number', 'view-card-number')}
            <CopyValueRow label="EXPIRES" value={expiry} testID="view-expiry" />
            {revealRow('CVV', c.cvv || '', 'cvv', 'cvv', 'view-cvv')}
            {revealRow('PIN', c.pin || '', 'pin', 'pin', 'view-pin')}
            <CopyValueRow label="NOTES" value={c.notes || ''} copyable={false} />
          </View>
        );
      }
      case 'identity': {
        const i = p as IdentityPayload;
        return (
          <View>
            <CopyValueRow label="TITLE" value={i.name || detail!.title || ''} copyable={false} testID="view-title" />
            <CopyValueRow label="FULL NAME" value={i.full_name || ''} testID="view-full-name" />
            <CopyValueRow label="COMPANY" value={i.company || ''} />
            <CopyValueRow label="PHONE" value={i.phone || ''} testID="view-phone" />
            <CopyValueRow label="EMAIL" value={i.email || ''} testID="view-email" />
            <CopyValueRow
              label="REGION"
              value={[i.country, i.state, i.city, i.district].filter(Boolean).join(' ')}
              copyable={false}
            />
            <CopyValueRow label="ADDRESS" value={i.address_line || ''} />
            <CopyValueRow label="POSTAL CODE" value={i.postal_code || ''} />
            <CopyValueRow label="NOTES" value={i.notes || ''} copyable={false} />
          </View>
        );
      }
      case 'secure_note': {
        const n = p as SecureNotePayload;
        return (
          <View>
            <CopyValueRow label="TITLE" value={n.name || detail!.title || ''} copyable={false} testID="view-title" />
            <CopyValueRow label="BODY" value={n.body} copyable={false} testID="view-body" />
          </View>
        );
      }
      case 'secret': {
        const s = p as SecretPayload;
        return (
          <View>
            <CopyValueRow label="TITLE" value={s.name || detail!.title || ''} copyable={false} testID="view-title" />
            {s.entries.map((entry, index) =>
              revealRow(
                `KEY: ${entry.key}`,
                entry.value,
                `entry-${index}`,
                undefined,
                `view-entry-${index}`,
              ),
            )}
            <CopyValueRow label="NOTES" value={s.notes || ''} copyable={false} />
          </View>
        );
      }
    }
  };

  return (
    <View style={[styles.root, {paddingBottom: insets.bottom}]}>
      <NewsprintHeader
        title={title}
        kicker={
          isCreate
            ? 'PERSONAL VAULT / CREATE'
            : isShared
              ? `SHARED · READ ONLY · REV ${detail!.revision}`
              : `${typeLabel} / PERSONAL · REV ${detail!.revision}`
        }
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

          {isCreate ? (
            <View style={styles.typePicker}>
              {ITEM_TYPES.map(t => {
                const selected = itemType === t.value;
                return (
                  <Pressable
                    key={t.value}
                    accessibilityRole="button"
                    accessibilityState={{selected}}
                    onPress={() => switchType(t.value)}
                    style={[styles.typeChip, selected && styles.typeChipSelected]}
                    testID={`type-${t.value}`}>
                    <Text style={[styles.typeChipText, selected && styles.typeChipTextSelected]}>
                      {t.label}
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          ) : null}

          {isCreate || mode === 'edit' ? (
            renderEditFields()
          ) : (
            renderViewFields()
          )}

          {!isCreate && !isShared ? (
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

      {itemType === 'login' ? (
        <PasswordGeneratorSheet
          visible={generatorVisible}
          api={api}
          csrfToken={csrf}
          onClose={() => setGeneratorVisible(false)}
          onUse={value => {
            patch({password: value});
            setGeneratorVisible(false);
          }}
        />
      ) : null}

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
      {detail ? (
        <ConfirmDialog
          visible={deleteConfirmVisible}
          title={`将 ${detail.payload.name || detail.title || '该条目'} 移入回收站？`}
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
      ) : null}
    </View>
  );
}

/** Multiline/masked-capable edit field with a SHOW/HIDE toggle for secrets. */
function SecureEditField({
  label,
  value,
  onChangeText,
  error,
  secretKey,
  showSecrets,
  onToggle,
  multiline,
  keyboardType,
  accessibilityLabel,
  testID,
}: {
  label: string;
  value: string;
  onChangeText: (text: string) => void;
  error?: string | undefined;
  secretKey: string;
  showSecrets: Record<string, boolean>;
  onToggle: (key: string) => void;
  multiline?: boolean;
  keyboardType?: 'default' | 'number-pad' | 'phone-pad' | 'email-address';
  accessibilityLabel?: string;
  testID?: string;
}): React.JSX.Element {
  const visible = !!showSecrets[secretKey];
  return (
    <View>
      <NewsprintInput
        label={label}
        value={value}
        onChangeText={onChangeText}
        error={error}
        secure={!visible}
        multiline={multiline}
        keyboardType={keyboardType}
        autoCapitalize="none"
        autoCorrect={false}
        accessibilityLabel={accessibilityLabel}
        testID={testID}
      />
      <View style={styles.secureToggleRow}>
        <NewsprintButton
          label={visible ? 'HIDE' : 'SHOW'}
          variant="ghost"
          onPress={() => onToggle(secretKey)}
          accessibilityLabel={visible ? `隐藏${label}` : `显示${label}`}
          style={styles.smallButton}
        />
      </View>
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
  typePicker: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: spacing.xs,
    marginTop: spacing.sm,
    marginBottom: spacing.md,
  },
  typeChip: {
    borderWidth,
    borderColor: colors.foreground,
    borderRadius: 0,
    paddingHorizontal: spacing.sm,
    minHeight: minTouchTarget,
    justifyContent: 'center',
    backgroundColor: colors.background,
  },
  typeChipSelected: {
    backgroundColor: colors.foreground,
  },
  typeChipText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    letterSpacing: 1,
    color: colors.foreground,
  },
  typeChipTextSelected: {
    color: colors.background,
  },
  choiceRow: {
    marginBottom: spacing.md,
  },
  choiceLabel: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: 1,
    color: colors.foreground,
    marginBottom: spacing.xs,
  },
  choiceOptions: {
    flexDirection: 'row',
    gap: spacing.xs,
  },
  choiceChip: {
    borderWidth,
    borderColor: colors.foreground,
    borderRadius: 0,
    paddingHorizontal: spacing.sm,
    minHeight: minTouchTarget,
    justifyContent: 'center',
  },
  choiceChipSelected: {
    backgroundColor: colors.foreground,
  },
  choiceChipText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    letterSpacing: 1,
    color: colors.foreground,
  },
  choiceChipTextSelected: {
    color: colors.background,
  },
  fieldErrorText: {
    ...typeScale.ui,
    fontFamily: fonts.ui,
    color: colors.accent,
    marginTop: spacing.xs,
  },
  sectionLabel: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: 1,
    color: colors.neutral600,
    marginTop: spacing.sm,
    marginBottom: spacing.xs,
  },
  entryBlock: {
    borderBottomWidth: borderWidth,
    borderBottomColor: colors.muted,
    marginBottom: spacing.sm,
  },
  entryHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  entryIndex: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
  },
  entryRemove: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    paddingHorizontal: spacing.xs,
  },
  entryRemoveText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    color: colors.accent,
    textDecorationLine: 'underline',
  },
  secureToggleRow: {
    alignItems: 'flex-end',
    marginTop: -spacing.xs,
    marginBottom: spacing.sm,
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
  splitRow: {
    flexDirection: 'row',
    gap: spacing.md,
  },
  splitField: {
    flex: 1,
  },
  smallButton: {
    minHeight: 44,
  },
  notesInput: {
    minHeight: 120,
  },
  bodyInput: {
    minHeight: 240,
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
