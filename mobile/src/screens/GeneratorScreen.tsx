import React, {useEffect} from 'react';
import {BackHandler, ScrollView, StyleSheet, View} from 'react-native';
import {useSafeAreaInsets} from 'react-native-safe-area-context';
import {NewsprintHeader} from '../components/NewsprintHeader';
import {PasswordGeneratorPanel} from '../components/PasswordGeneratorPanel';
import {colors} from '../theme/colors';
import {pageMargin, spacing} from '../theme/spacing';
import type {TinyPasswordApi} from '../api/client';

interface GeneratorScreenProps {
  api: TinyPasswordApi;
  csrfToken: string | null;
  onClose: () => void;
  onActivity: () => void;
}

/**
 * Standalone password generator. Same shared panel as the entry editor's
 * bottom sheet, but copy-only: generated passwords never touch storage here.
 */
export function GeneratorScreen({
  api,
  csrfToken,
  onClose,
  onActivity,
}: GeneratorScreenProps): React.JSX.Element {
  const insets = useSafeAreaInsets();

  useEffect(() => {
    onActivity();
    const handler = () => {
      onClose();
      return true;
    };
    const subscription = BackHandler.addEventListener('hardwareBackPress', handler);
    return () => subscription.remove();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <View style={[styles.root, {paddingTop: insets.top, paddingBottom: insets.bottom}]}>
      <NewsprintHeader
        title="PASSWORD GENERATOR"
        kicker="PERSONAL VAULT / TOOL"
        backLabel="Back"
        onBack={onClose}
        testID="generator-header"
      />
      <ScrollView contentContainerStyle={styles.content} keyboardShouldPersistTaps="handled">
        <PasswordGeneratorPanel api={api} csrfToken={csrfToken} />
      </ScrollView>
    </View>
  );
}

const styles = StyleSheet.create({
  root: {
    flex: 1,
    backgroundColor: colors.background,
  },
  content: {
    paddingHorizontal: pageMargin,
    paddingBottom: spacing.xl,
  },
});
