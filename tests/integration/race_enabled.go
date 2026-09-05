//go:build race

package integration

// raceDetector reports that the test binary runs under the race detector;
// performance baselines skip in that mode (race overhead distorts timings).
const raceDetector = true
