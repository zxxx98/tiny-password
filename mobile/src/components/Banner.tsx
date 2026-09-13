import React from 'react';
import {Pressable, StyleSheet, Text, View} from 'react-native';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {borderWidth, minTouchTarget, spacing} from '../theme/spacing';

interface BannerProps {
  kind: 'error' | 'info' | 'warning';
  text: string;
  actionLabel?: string;
  onAction?: () => void;
  testID?: string;
}

/**
 * Inline status banner. Errors use Editorial Red text (never color alone:
 * the text itself carries the meaning) and an optional retry action.
 */
export function Banner({kind, text, actionLabel, onAction, testID}: BannerProps): React.JSX.Element | null {
  if (!text) {
    return null;
  }
  return (
    <View
      accessibilityLiveRegion="polite"
      style={[styles.box, kind === 'error' ? styles.error : styles.info]}
      testID={testID}>
      <Text style={[styles.text, kind === 'error' ? styles.errorText : null]}>{text}</Text>
      {actionLabel && onAction ? (
        <Pressable
          accessibilityRole="button"
          accessibilityLabel={actionLabel}
          onPress={onAction}
          hitSlop={8}
          style={styles.action}>
          <Text style={styles.actionText}>{actionLabel.toUpperCase()}</Text>
        </Pressable>
      ) : null}
    </View>
  );
}

const styles = StyleSheet.create({
  box: {
    borderWidth: borderWidth,
    borderColor: colors.foreground,
    backgroundColor: colors.background,
    paddingVertical: spacing.sm,
    paddingHorizontal: spacing.md,
    flexDirection: 'row',
    alignItems: 'center',
    minHeight: minTouchTarget,
    marginVertical: spacing.sm,
  },
  error: {
    borderColor: colors.accent,
  },
  info: {
    borderColor: colors.foreground,
  },
  text: {
    ...typeScale.ui,
    fontFamily: fonts.ui,
    color: colors.foreground,
    flex: 1,
  },
  errorText: {
    color: colors.accent,
    fontFamily: fonts.uiSemi,
  },
  action: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    paddingHorizontal: spacing.sm,
    marginLeft: spacing.sm,
  },
  actionText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
    textDecorationLine: 'underline',
  },
});
