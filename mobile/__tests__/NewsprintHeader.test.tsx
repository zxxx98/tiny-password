import React from 'react';
import {StyleSheet, View} from 'react-native';
import renderer, {act, type ReactTestRenderer} from 'react-test-renderer';
import {SafeAreaProvider} from 'react-native-safe-area-context';
import {NewsprintHeader} from '../src/components/NewsprintHeader';

test('places the header after the status-bar inset plus the standard top spacing', () => {
  let tree!: ReactTestRenderer;
  act(() => {
    tree = createTree(
      <SafeAreaProvider
        initialMetrics={{
          frame: {x: 0, y: 0, width: 360, height: 800},
          insets: {top: 28, right: 0, bottom: 16, left: 0},
        }}>
        <NewsprintHeader
          title="ENTRY"
          backLabel="Back"
          onBack={() => undefined}
          testID="header"
        />
      </SafeAreaProvider>,
    );
  });

  const header = tree.root.findAllByType(View).find(node => node.props.testID === 'header');
  expect(header).toBeDefined();
  expect(StyleSheet.flatten(header?.props.style).paddingTop).toBe(44);
});

const renderedTrees: ReactTestRenderer[] = [];
function createTree(element: React.ReactElement): ReactTestRenderer {
  const tree = renderer.create(element);
  renderedTrees.push(tree);
  return tree;
}
afterEach(async () => {
  act(() => renderedTrees.splice(0).forEach(tree => tree.unmount()));
});
