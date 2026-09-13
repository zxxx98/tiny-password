import React, {useCallback, useEffect, useRef, useState} from 'react';
import {
  Dimensions,
  Modal,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import Slider from '@react-native-community/slider';
import Clipboard from '@react-native-clipboard/clipboard';
import {NewsprintButton} from './NewsprintButton';
import {Banner} from './Banner';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {borderHeavy, borderWidth, minTouchTarget, pageMargin, spacing} from '../theme/spacing';
import type {PasswordGeneratorOptions} from '../api/types';
import type {TinyPasswordApi} from '../api/client';
import {DebouncedRunner} from '../api/async';

export const GENERATOR_DEFAULTS: PasswordGeneratorOptions = {
  length: 20,
  uppercase: true,
  lowercase: true,
  digits: true,
  symbols: true,
  exclude_ambiguous: false,
};

const GENERATOR_RANGE = {min: 8, max: 128} as const;
const PARAM_DEBOUNCE_MS = 400;

type GeneratorStatus = 'generating' | 'stale' | 'ready' | 'error';

interface PasswordGeneratorSheetProps {
  visible: boolean;
  api: TinyPasswordApi;
  csrfToken: string | null;
  onClose: () => void;
  /** Receives the current generated value; must be in 'ready' state to fire. */
  onUse: (value: string) => void;
}

/**
 * Bottom-sheet password generator over POST /generators/password.
 *
 * Ordering guarantee: the moment any parameter changes (including during the
 * debounce wait) the previously generated value becomes stale — COPY and USE
 * PASSWORD are disabled, so a password can never be filled that does not
 * match the visible parameters. REGENERATE re-requests without changing
 * parameters; failures keep the editor's existing password untouched.
 */
export function PasswordGeneratorSheet({
  visible,
  api,
  csrfToken,
  onClose,
  onUse,
}: PasswordGeneratorSheetProps): React.JSX.Element {
  const [params, setParams] = useState<PasswordGeneratorOptions>(GENERATOR_DEFAULTS);
  const [status, setStatus] = useState<GeneratorStatus>('generating');
  const [value, setValue] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const runnerRef = useRef<DebouncedRunner | null>(null);
  if (runnerRef.current === null) {
    runnerRef.current = new DebouncedRunner(PARAM_DEBOUNCE_MS);
  }
  const runner = runnerRef.current;

  const generate = useCallback(
    (options: PasswordGeneratorOptions, signal: AbortSignal) => {
      const token = csrfToken;
      if (!token) {
        setStatus('error');
        setError('缺少会话上下文');
        return Promise.resolve();
      }
      setStatus('generating');
      setError(null);
      return api
        .generatePassword(options, token, signal)
        .then(result => {
          if (signal.aborted) {
            return; // stale response: a newer parameter set is active
          }
          if (result.kind === 'success') {
            setValue(result.data.value);
            setStatus('ready');
          } else if (result.kind === 'http-error') {
            setStatus('error');
            setError(
              result.error.code === 'RATE_LIMITED'
                ? '请求过于频繁，请稍后再试'
                : result.error.message || '生成失败',
            );
          } else if (result.kind === 'network-error') {
            setStatus('error');
            setError('网络异常，无法生成密码');
          } else {
            setStatus('error');
            setError('生成失败');
          }
        })
        .catch(() => {
          if (!signal.aborted) {
            setStatus('error');
            setError('生成失败');
          }
        });
    },
    [api, csrfToken],
  );

  // Generate immediately each time the sheet opens.
  useEffect(() => {
    if (visible) {
      setValue(null);
      runner.runNow(signal => generate(params, signal));
    }
    return () => {
      runner.invalidate();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible]);

  useEffect(() => () => runner.dispose(), [runner]);

  const updateParams = (patch: Partial<PasswordGeneratorOptions>) => {
    const next = {...params, ...patch};
    setParams(next);
    // Invalidate the old value the instant parameters change: during the
    // debounce wait the status is 'stale' and copy/use stay disabled.
    setStatus('stale');
    runner.schedule(signal => generate(next, signal));
  };

  const toggle = (key: 'uppercase' | 'lowercase' | 'digits' | 'symbols') => {
    const enabledCount =
      Number(params.uppercase) + Number(params.lowercase) + Number(params.digits) + Number(params.symbols);
    if (params[key] && enabledCount <= 1) {
      return; // at least one character class must stay on
    }
    const next = {...params, [key]: !params[key]};
    setParams(next);
    setStatus('stale');
    runner.schedule(signal => generate(next, signal));
  };

  const ready = status === 'ready' && value !== null;

  const copy = () => {
    if (!ready || !value) {
      return;
    }
    Clipboard.setString(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  const use = () => {
    if (!ready || !value) {
      return;
    }
    onUse(value);
  };

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose} statusBarTranslucent>
      <Pressable style={styles.backdrop} accessibilityLabel="关闭密码生成器" onPress={onClose} />
      <View accessibilityViewIsModal style={styles.sheet}>
        <View style={styles.sheetRule} />
        <Text style={styles.title}>PASSWORD GENERATOR</Text>
        <View style={styles.rule} />

        <View style={styles.valueBox} testID="generator-value">
          <Text style={styles.value} accessibilityLiveRegion="polite">
            {status === 'ready' && value ? value : status === 'stale' ? '· · ·' : ''}
          </Text>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="复制生成的密码"
            onPress={copy}
            disabled={!ready}
            hitSlop={8}
            style={styles.copyButton}>
            <Text style={[styles.copyText, !ready && styles.disabledText]}>
              {copied ? 'COPIED' : 'COPY'}
            </Text>
          </Pressable>
        </View>

        <Banner kind="error" text={status === 'error' ? error ?? '生成失败' : ''} />

        <View style={styles.lengthRow}>
          <Text style={styles.label}>LENGTH</Text>
          <Text style={styles.lengthValue}>{params.length}</Text>
        </View>
        <Slider
          accessibilityLabel="密码长度"
          minimumValue={GENERATOR_RANGE.min}
          maximumValue={GENERATOR_RANGE.max}
          step={1}
          value={params.length}
          minimumTrackTintColor={colors.foreground}
          maximumTrackTintColor={colors.muted}
          thumbTintColor={colors.foreground}
          style={styles.slider}
          onValueChange={v => updateParams({length: Math.round(v)})}
        />
        <View style={styles.rangeRow}>
          <Text style={styles.range}>{GENERATOR_RANGE.min}</Text>
          <Text style={styles.range}>{GENERATOR_RANGE.max}</Text>
        </View>

        <View style={styles.options}>
          <Toggle label="UPPERCASE" value={params.uppercase} onToggle={() => toggle('uppercase')} />
          <Toggle label="LOWERCASE" value={params.lowercase} onToggle={() => toggle('lowercase')} />
          <Toggle label="NUMBERS" value={params.digits} onToggle={() => toggle('digits')} />
          <Toggle label="SYMBOLS" value={params.symbols} onToggle={() => toggle('symbols')} />
          <Toggle
            label="EXCLUDE AMBIGUOUS"
            value={params.exclude_ambiguous}
            onToggle={() => updateParams({exclude_ambiguous: !params.exclude_ambiguous})}
          />
        </View>

        <View style={styles.actions}>
          <NewsprintButton
            label="REGENERATE"
            variant="secondary"
            onPress={() => runner.runNow(signal => generate(params, signal))}
            style={styles.actionButton}
          />
          <NewsprintButton
            label="USE PASSWORD"
            onPress={use}
            disabled={!ready}
            testID="generator-use"
            style={styles.actionButton}
          />
        </View>
        <Text style={styles.hint}>生成密码通过服务端安全随机源产生，不会被存储。</Text>
      </View>
    </Modal>
  );
}

interface ToggleProps {
  label: string;
  value: boolean;
  onToggle: () => void;
}

function Toggle({label, value, onToggle}: ToggleProps): React.JSX.Element {
  return (
    <Pressable
      accessibilityRole="checkbox"
      accessibilityState={{checked: value}}
      accessibilityLabel={label}
      onPress={onToggle}
      hitSlop={6}
      style={styles.toggle}>
      <View style={[styles.checkbox, value ? styles.checkboxOn : null]}>
        {value ? <Text style={styles.checkmark}>✕</Text> : null}
      </View>
      <Text style={styles.toggleLabel}>{label}</Text>
    </Pressable>
  );
}

const sheetWidth = Dimensions.get('window').width;

const styles = StyleSheet.create({
  backdrop: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(17, 17, 17, 0.5)',
  },
  sheet: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    width: sheetWidth,
    maxHeight: '90%',
    backgroundColor: colors.background,
    borderTopWidth: borderHeavy,
    borderTopColor: colors.foreground,
    paddingHorizontal: pageMargin,
    paddingTop: spacing.md,
    paddingBottom: spacing.xl,
  },
  sheetRule: {
    alignSelf: 'center',
    width: 44,
    height: 4,
    backgroundColor: colors.muted,
    marginBottom: spacing.md,
  },
  title: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  rule: {
    height: 1,
    backgroundColor: colors.foreground,
    marginTop: spacing.sm,
    marginBottom: spacing.md,
  },
  valueBox: {
    borderWidth: borderWidth,
    borderColor: colors.foreground,
    backgroundColor: colors.background,
    flexDirection: 'row',
    alignItems: 'center',
    minHeight: minTouchTarget + 4,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  value: {
    flex: 1,
    ...typeScale.mono,
    fontFamily: fonts.mono,
    color: colors.foreground,
  },
  copyButton: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    paddingHorizontal: spacing.sm,
  },
  copyText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
    textDecorationLine: 'underline',
  },
  disabledText: {
    color: colors.neutral400,
    textDecorationLine: 'none',
  },
  lengthRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginTop: spacing.md,
  },
  label: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  lengthValue: {
    ...typeScale.mono,
    fontFamily: fonts.monoMedium,
    color: colors.foreground,
  },
  slider: {
    width: '100%',
    height: 40,
  },
  rangeRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    marginBottom: spacing.sm,
  },
  range: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
  },
  options: {
    marginTop: spacing.md,
    gap: spacing.sm,
  },
  toggle: {
    flexDirection: 'row',
    alignItems: 'center',
    minHeight: minTouchTarget,
  },
  checkbox: {
    width: 22,
    height: 22,
    borderWidth: borderWidth,
    borderColor: colors.foreground,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: spacing.sm,
  },
  checkboxOn: {
    backgroundColor: colors.foreground,
  },
  checkmark: {
    color: colors.paper,
    fontSize: 13,
    lineHeight: 15,
    fontFamily: fonts.uiBold,
  },
  toggleLabel: {
    ...typeScale.uiSemi,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  actions: {
    flexDirection: 'row',
    gap: spacing.sm,
    marginTop: spacing.lg,
  },
  actionButton: {
    flex: 1,
  },
  hint: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
    marginTop: spacing.md,
    textAlign: 'center',
  },
});
