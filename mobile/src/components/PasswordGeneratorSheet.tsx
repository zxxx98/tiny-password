import React from 'react';
import {Dimensions, Modal, Pressable, StyleSheet, Text, View} from 'react-native';
import {PasswordGeneratorPanel} from './PasswordGeneratorPanel';
import {colors} from '../theme/colors';
import {fonts, letterSpacing, typeScale} from '../theme/typography';
import {borderHeavy, pageMargin, spacing} from '../theme/spacing';
import type {TinyPasswordApi} from '../api/client';

interface PasswordGeneratorSheetProps {
  visible: boolean;
  api: TinyPasswordApi;
  csrfToken: string | null;
  onClose: () => void;
  /** Receives the current generated value; must be in 'ready' state to fire. */
  onUse: (value: string) => void;
}

/**
 * Bottom-sheet wrapper around the shared generator panel, used by the entry
 * editor. The panel mounts only while the sheet is open so each opening
 * starts a fresh generation.
 */
export function PasswordGeneratorSheet({
  visible,
  api,
  csrfToken,
  onClose,
  onUse,
}: PasswordGeneratorSheetProps): React.JSX.Element {
  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose} statusBarTranslucent>
      <Pressable style={styles.backdrop} accessibilityLabel="关闭密码生成器" onPress={onClose} />
      <View accessibilityViewIsModal style={styles.sheet}>
        <View style={styles.sheetRule} />
        <Text style={styles.title}>PASSWORD GENERATOR</Text>
        <View style={styles.rule} />
        {visible ? (
          <PasswordGeneratorPanel api={api} csrfToken={csrfToken} showUse onUse={onUse} />
        ) : null}
      </View>
    </Modal>
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
});
