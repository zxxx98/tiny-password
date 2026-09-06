//go:build windows

package backup

// freeBytes is not implemented for windows builds; the default space check
// reports unsupported. Deployment targets are linux containers.
func freeBytes(string) (int64, error) { return 0, errSpaceUnsupported }
