import React, {useEffect, useRef, useState} from 'react';
import {Pressable, StyleSheet, Text, View} from 'react-native';
import Clipboard from '@react-native-clipboard/clipboard';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {minTouchTarget, spacing} from '../theme/spacing';

interface CopyValueRowProps {
  label: string;
  value: string | null | undefined;
  /** Optional extra action (e.g. password SHOW/HIDE). */
  onToggleVisibility?: () => void;
  visible?: boolean;
  masked?: boolean;
  /** Set false for read-only rows the design shows without a copy action. */
  copyable?: boolean;
  /** Fired after the value was written to the clipboard (audit hook). */
  onCopy?: () => void;
  testID?: string;
}

/**
 * View-mode value row: uppercase label, monospace value (masked for
 * secrets), COPY action with a transient COPIED state, optional SHOW/HIDE.
 * Empty optional values render an em dash and hide the copy button.
 */
export function CopyValueRow({
  label,
  value,
  onToggleVisibility,
  visible = false,
  masked = false,
  copyable = true,
  onCopy,
  testID,
}: CopyValueRowProps): React.JSX.Element {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (timer.current !== null) {
        clearTimeout(timer.current);
      }
    };
  }, []);

  const hasValue = typeof value === 'string' && value.length > 0;
  const shown = hasValue && (!masked || visible);
  const display = !hasValue ? '—' : masked && !visible ? maskValue() : value!;

  const copy = () => {
    if (!hasValue) {
      return;
    }
    Clipboard.setString(value!);
    setCopied(true);
    onCopy?.();
    if (timer.current !== null) {
      clearTimeout(timer.current);
    }
    timer.current = setTimeout(() => setCopied(false), 1500);
  };

  return (
    <View style={styles.container} testID={testID}>
      <Text style={styles.label}>{label}</Text>
      <View style={styles.row}>
        <Text selectable={shown} style={[styles.value, !hasValue && styles.empty]}>
          {display}
        </Text>
        {onToggleVisibility && hasValue ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={visible ? '隐藏密码' : '显示密码'}
            accessibilityState={{expanded: visible}}
            onPress={onToggleVisibility}
            hitSlop={8}
            style={styles.action}>
            <Text style={styles.actionText}>{visible ? 'HIDE' : 'SHOW'}</Text>
          </Pressable>
        ) : null}
        {hasValue && copyable ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={`复制${label}`}
            onPress={copy}
            hitSlop={8}
            style={styles.action}>
            <Text style={[styles.actionText, copied && styles.copiedText]}>
              {copied ? 'COPIED' : 'COPY'}
            </Text>
          </Pressable>
        ) : null}
      </View>
    </View>
  );
}

function maskValue(): string {
  // Fixed dot count: never leak the real value's length through the mask.
  return '•'.repeat(14);
}

const styles = StyleSheet.create({
  container: {
    borderBottomWidth: 1,
    borderBottomColor: colors.muted,
    paddingVertical: spacing.md,
  },
  label: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.neutral600,
    marginBottom: spacing.xs,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    minHeight: minTouchTarget,
  },
  value: {
    flex: 1,
    ...typeScale.mono,
    fontFamily: fonts.mono,
    color: colors.foreground,
  },
  empty: {
    color: colors.neutral400,
  },
  action: {
    minHeight: minTouchTarget,
    minWidth: minTouchTarget,
    justifyContent: 'center',
    alignItems: 'flex-end',
    paddingHorizontal: spacing.xs,
    marginLeft: spacing.xs,
  },
  actionText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
    textDecorationLine: 'underline',
  },
  copiedText: {
    color: colors.accent,
  },
});
