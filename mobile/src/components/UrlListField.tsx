import React from 'react';
import {Pressable, StyleSheet, Text, View} from 'react-native';
import {NewsprintInput} from './NewsprintInput';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {LIMITS} from '../vault/payloadMerge';
import {minTouchTarget, spacing} from '../theme/spacing';

interface UrlListFieldProps {
  urls: string[];
  onChange: (urls: string[]) => void;
  disabled?: boolean;
  error?: string | null;
}

/**
 * Ordered, editable URL list for the entry editor. Each line is its own
 * input; lines can be added and removed (order preserved, max 16); blank
 * lines simply never become array elements on save.
 */
export function UrlListField({
  urls,
  onChange,
  disabled = false,
  error,
}: UrlListFieldProps): React.JSX.Element {
  const update = (index: number, value: string) => {
    const next = urls.slice();
    next[index] = value;
    onChange(next);
  };

  const remove = (index: number) => {
    const next = urls.slice();
    next.splice(index, 1);
    onChange(next.length === 0 ? [''] : next);
  };

  const add = () => {
    if (urls.length >= LIMITS.urlCount) {
      return;
    }
    onChange([...urls, '']);
  };

  return (
    <View style={styles.container}>
      <View style={styles.labelRow}>
        <Text style={styles.label}>URLS</Text>
        <Text style={styles.counter}>
          {urls.filter(u => u.trim().length > 0).length}/{LIMITS.urlCount}
        </Text>
      </View>
      {urls.map((url, index) => (
        <View key={index} style={styles.row}>
          <View style={styles.inputWrap}>
            <NewsprintInput
              label={`URL ${index + 1}`}
              value={url}
              editable={!disabled}
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="url"
              onChangeText={value => update(index, value)}
              placeholder="https://example.com"
              accessibilityLabel={`URL 第 ${index + 1} 行`}
            />
          </View>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={`移除 URL 第 ${index + 1} 行`}
            onPress={() => remove(index)}
            disabled={disabled}
            hitSlop={8}
            style={styles.remove}>
            <Text style={styles.removeText}>REMOVE</Text>
          </Pressable>
        </View>
      ))}
      {error ? (
        <Text accessibilityLiveRegion="polite" style={styles.error}>
          {error}
        </Text>
      ) : null}
      <Pressable
        accessibilityRole="button"
        accessibilityLabel="添加 URL 行"
        onPress={add}
        disabled={disabled || urls.length >= LIMITS.urlCount}
        hitSlop={8}
        style={styles.add}>
        <Text style={[styles.addText, urls.length >= LIMITS.urlCount && styles.disabledText]}>
          + ADD URL
        </Text>
      </Pressable>
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    marginBottom: spacing.md,
  },
  labelRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: spacing.xs,
  },
  label: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  counter: {
    ...typeScale.meta,
    fontFamily: fonts.mono,
    color: colors.neutral600,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'flex-end',
  },
  inputWrap: {
    flex: 1,
  },
  remove: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    marginLeft: spacing.sm,
    paddingHorizontal: spacing.xs,
  },
  removeText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.accent,
  },
  error: {
    ...typeScale.ui,
    fontFamily: fonts.ui,
    color: colors.accent,
    marginTop: spacing.xs,
  },
  add: {
    minHeight: minTouchTarget,
    justifyContent: 'center',
    alignSelf: 'flex-start',
    paddingHorizontal: spacing.xs,
  },
  addText: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
  },
  disabledText: {
    color: colors.neutral400,
  },
});
