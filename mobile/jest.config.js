module.exports = {
  preset: '@react-native/jest-preset',
  // 默认 5s 在 CI 的并行 worker 里偏紧，React Native 渲染型用例容易误报超时。
  testTimeout: 30000,
};
