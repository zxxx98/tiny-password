import React from 'react';
import {StyleSheet, Text, TextInput, View, type TextInputProps} from 'react-native';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {borderHeavy, spacing} from '../theme/spacing';

interface NewsprintInputProps extends TextInputProps {
  label: string;
  error?: string | null;
  mono?: boolean;
  secure?: boolean;
}

/**
 * Newsprint text field: transparent background, 2dp black bottom border,
 * light grey fill while focused, uppercase Inter label above, monospace
 * value text. Error text sits under the field in Editorial Red.
 */
export function NewsprintInput({
  label,
  error,
  mono = true,
  secure = false,
  style,
  ...rest
}: NewsprintInputProps): React.JSX.Element {
  return (
    <View style={[styles.container, style]}>
      <Text style={styles.label}>{label}</Text>
      <View style={[styles.fieldWrap, error ? styles.fieldError : null]}>
        <TextInput
          {...rest}
          style={[styles.input, mono ? styles.mono : styles.plain]}
          secureTextEntry={secure}
          placeholderTextColor={colors.neutral400}
          underlineColorAndroid="transparent"
        />
      </View>
      {error ? (
        <Text accessibilityLiveRegion="polite" style={styles.error}>
          {error}
        </Text>
      ) : null}
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    marginBottom: spacing.md,
  },
  label: {
    ...typeScale.label,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.label,
    color: colors.foreground,
    marginBottom: spacing.xs,
  },
  fieldWrap: {
    borderBottomWidth: borderHeavy,
    borderBottomColor: colors.foreground,
    backgroundColor: 'transparent',
    minHeight: 44,
    justifyContent: 'center',
  },
  fieldError: {
    borderBottomColor: colors.accent,
  },
  input: {
    minHeight: 44,
    paddingHorizontal: 4,
    paddingVertical: 10,
    fontSize: 15,
    color: colors.foreground,
    borderRadius: 0,
  },
  mono: {
    fontFamily: fonts.mono,
  },
  plain: {
    fontFamily: fonts.body,
  },
  error: {
    ...typeScale.ui,
    fontFamily: fonts.ui,
    color: colors.accent,
    marginTop: spacing.xs,
  },
});
