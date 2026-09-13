const { getDefaultConfig, mergeConfig } = require('@react-native/metro-config');

/**
 * Metro configuration
 * https://reactnative.dev/docs/metro
 *
 * @type {import('@react-native/metro-config').MetroConfig}
 */
const config = {
  resolver: {
    // Android/Gradle native-build scratch dirs churn while Gradle runs and
    // crash Metro's watcher on Windows; exclude them from scan and watch.
    blockList: [
      /android\/app\/\.cxx[\\/]?.*/,
      /android\/app\/build[\\/]?.*/,
      /android\/build[\\/]?.*/,
    ],
  },
};

module.exports = mergeConfig(getDefaultConfig(__dirname), config);
