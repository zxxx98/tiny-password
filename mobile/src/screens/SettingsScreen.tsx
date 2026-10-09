import React from 'react';
import {Modal, ScrollView, StyleSheet, Text, View} from 'react-native';
import {useSafeAreaInsets} from 'react-native-safe-area-context';
import {NewsprintHeader} from '../components/NewsprintHeader';
import {CheckUpdateButton} from '../components/CheckUpdateButton';
import {useInteraction} from '../privacy/interaction';
import {colors} from '../theme/colors';
import {fonts, typeScale} from '../theme/typography';
import {pageMargin, spacing} from '../theme/spacing';

/** A full-screen settings page keeps the underlying form/list state intact. */
export function SettingsScreen({onClose}: {onClose: () => void}): React.JSX.Element {
  const {hidden, onActivity} = useInteraction();
  const insets = useSafeAreaInsets();
  return (
    <Modal visible={!hidden} animationType="slide" onRequestClose={onClose}>
      <View style={[styles.root, {paddingBottom: insets.bottom}]} onTouchStart={onActivity}>
        <NewsprintHeader title="SETTINGS" kicker="tiny-password" backLabel="返回" onBack={onClose} />
        <ScrollView contentContainerStyle={styles.content}>
          <Text style={styles.title}>应用更新</Text>
          <Text style={styles.description}>查看当前版本，手动检查是否有新版本。</Text>
          <CheckUpdateButton />
        </ScrollView>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  root: {flex: 1, backgroundColor: colors.background},
  content: {padding: pageMargin, paddingBottom: spacing.xl},
  title: {...typeScale.h3, fontFamily: fonts.display, color: colors.foreground},
  description: {...typeScale.body, fontFamily: fonts.body, color: colors.neutral600, marginTop: spacing.sm, marginBottom: spacing.lg},
});
