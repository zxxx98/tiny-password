import React, {useState} from 'react';
import {
  ActivityIndicator,
  Pressable,
  StyleSheet,
  Text,
  View,
  type ViewStyle,
} from 'react-native';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {borderWidth, buttonHeight, minTouchTarget} from '../theme/spacing';

interface ButtonProps {
  label: string;
  onPress: () => void;
  disabled?: boolean;
  loading?: boolean;
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost';
  style?: ViewStyle;
  accessibilityLabel?: string;
  testID?: string;
}

/**
 * Newsprint button: sharp corners, uppercase Inter label with wide tracking,
 * solid black primary / outlined secondary / red-accented danger.
 * Height 52dp, min touch target 48dp.
 */
export function NewsprintButton({
  label,
  onPress,
  disabled = false,
  loading = false,
  variant = 'primary',
  style,
  accessibilityLabel,
  testID,
}: ButtonProps): React.JSX.Element {
  const [pressed, setPressed] = useState(false);
  const inactive = disabled || loading;

  let container: ViewStyle;
  let textColor: string;
  switch (variant) {
    case 'secondary':
      container = {
        backgroundColor: pressed ? colors.foreground : 'transparent',
        borderWidth,
        borderColor: colors.foreground,
      };
      textColor = pressed ? colors.paper : colors.foreground;
      break;
    case 'danger':
      container = {
        backgroundColor: pressed ? colors.accent : 'transparent',
        borderWidth,
        borderColor: colors.accent,
      };
      textColor = pressed ? colors.paper : colors.accent;
      break;
    case 'ghost':
      container = {
        backgroundColor: pressed ? colors.muted : 'transparent',
        borderWidth: 0,
      };
      textColor = colors.foreground;
      break;
    default:
      container = {
        backgroundColor: inactive ? colors.neutral400 : pressed ? colors.paper : colors.foreground,
        borderWidth,
        borderColor: inactive ? colors.neutral400 : colors.foreground,
      };
      textColor = inactive ? colors.paper : pressed ? colors.foreground : colors.paper;
  }

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel ?? label}
      accessibilityState={{disabled: inactive, busy: loading}}
      disabled={inactive}
      testID={testID}
      onPress={onPress}
      onPressIn={() => setPressed(true)}
      onPressOut={() => setPressed(false)}
      hitSlop={{top: 4, bottom: 4, left: 4, right: 4}}
      style={[styles.base, container, style]}>
      {loading ? (
        <View style={styles.row}>
          <ActivityIndicator size="small" color={textColor} />
          <Text style={[styles.label, {color: textColor, marginLeft: 8}]}>{label}</Text>
        </View>
      ) : (
        <Text style={[styles.label, {color: textColor}]}>{label}</Text>
      )}
    </Pressable>
  );
}

const styles = StyleSheet.create({
  base: {
    minHeight: buttonHeight,
    paddingHorizontal: 20,
    justifyContent: 'center',
    alignItems: 'center',
    borderRadius: 0,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  label: {
    ...typeScale.button,
    fontFamily: fonts.uiSemi,
    textTransform: 'uppercase',
    letterSpacing: letterSpacing.button,
  },
});

export const touchTarget = minTouchTarget;
