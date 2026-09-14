import React from 'react';
import {Pressable, StyleSheet, Text, View} from 'react-native';
import {useSafeAreaInsets} from 'react-native-safe-area-context';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {borderSection, minTouchTarget, pageMargin, spacing} from '../theme/spacing';

interface NewsprintHeaderProps {
  title: string;
  kicker?: string;
  rightActionLabel?: string;
  onRightAction?: () => void;
  backLabel?: string;
  onBack?: () => void;
  testID?: string;
}

/**
 * Newsprint page header: serif masthead, uppercase kicker, 4dp bottom rule,
 * optional leading back action and trailing uppercase text action.
 * Respects the 16dp page margin and 48dp touch targets.
 */
export function NewsprintHeader({
  title,
  kicker,
  rightActionLabel,
  onRightAction,
  backLabel,
  onBack,
  testID,
}: NewsprintHeaderProps): React.JSX.Element {
  const insets = useSafeAreaInsets();

  return (
    <View style={[styles.container, {paddingTop: insets.top + spacing.md}]} testID={testID}>
      <View style={styles.row}>
        {backLabel && onBack ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={backLabel}
            onPress={onBack}
            hitSlop={8}
            style={styles.back}>
            <Text style={styles.backText}>‹ {backLabel.toUpperCase()}</Text>
          </Pressable>
        ) : (
          <View style={styles.flex} />
        )}
        {rightActionLabel && onRightAction ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={rightActionLabel}
            onPress={onRightAction}
            hitSlop={8}
            style={styles.action}>
            <Text style={styles.actionText}>{rightActionLabel.toUpperCase()}</Text>
          </Pressable>
        ) : null}
      </View>
      {kicker ? <Text style={styles.kicker}>{kicker.toUpperCase()}</Text> : null}
      <Text style={styles.title}>{title}</Text>
      <View style={styles.rule} />
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    paddingHorizontal: pageMargin,
    backgroundColor: colors.background,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    minHeight: minTouchTarget,
  },
  flex: {
    flex: 1,
  },
  back: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    paddingRight: spacing.md,
  },
  backText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  action: {
    flex: 1,
    minHeight: minTouchTarget,
    justifyContent: 'center',
    alignItems: 'flex-end',
  },
  actionText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
    textDecorationLine: 'underline',
  },
  kicker: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.wide,
    color: colors.neutral600,
    marginTop: spacing.xs,
  },
  title: {
    ...typeScale.h1,
    fontFamily: fonts.display,
    color: colors.foreground,
    marginTop: spacing.xs,
  },
  rule: {
    height: borderSection,
    backgroundColor: colors.foreground,
    marginTop: spacing.md,
  },
});
