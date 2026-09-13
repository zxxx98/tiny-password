import React from 'react';
import {Modal, StyleSheet, Text, View} from 'react-native';
import {NewsprintButton} from './NewsprintButton';
import {colors} from '../theme/colors';
import {fonts, typeScale} from '../theme/typography';
import {borderWidth, spacing} from '../theme/spacing';

interface ConfirmDialogProps {
  visible: boolean;
  title: string;
  message: string;
  confirmLabel: string;
  cancelLabel: string;
  destructive?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  testID?: string;
}

/**
 * Newsprint modal confirmation: sharp black-bordered box on a dimmed paper
 * backdrop. Used for trash confirmation, draft reload confirmation and
 * unsaved-changes discard prompts.
 */
export function ConfirmDialog({
  visible,
  title,
  message,
  confirmLabel,
  cancelLabel,
  destructive = false,
  onConfirm,
  onCancel,
  testID,
}: ConfirmDialogProps): React.JSX.Element {
  return (
    <Modal
      visible={visible}
      transparent
      animationType="fade"
      onRequestClose={onCancel}
      statusBarTranslucent>
      <View style={styles.backdrop}>
        <View accessibilityViewIsModal style={styles.box} testID={testID}>
          <Text style={styles.title}>{title}</Text>
          <Text style={styles.message}>{message}</Text>
          <View style={styles.actions}>
            <View style={styles.button}>
              <NewsprintButton label={cancelLabel} variant="secondary" onPress={onCancel} />
            </View>
            <View style={styles.button}>
              <NewsprintButton
                label={confirmLabel}
                variant={destructive ? 'danger' : 'primary'}
                onPress={onConfirm}
              />
            </View>
          </View>
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  backdrop: {
    flex: 1,
    backgroundColor: 'rgba(17, 17, 17, 0.5)',
    justifyContent: 'center',
    paddingHorizontal: spacing.lg,
  },
  box: {
    backgroundColor: colors.background,
    borderWidth: borderWidth,
    borderColor: colors.foreground,
    padding: spacing.lg,
  },
  title: {
    ...typeScale.h3,
    fontFamily: fonts.display,
    color: colors.foreground,
    marginBottom: spacing.sm,
  },
  message: {
    ...typeScale.body,
    fontFamily: fonts.body,
    color: colors.foreground,
    marginBottom: spacing.lg,
  },
  actions: {
    flexDirection: 'row',
    justifyContent: 'flex-end',
    gap: spacing.sm,
  },
  button: {
    flex: 1,
    maxWidth: 200,
  },
});
