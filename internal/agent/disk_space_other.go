//go:build !darwin && !linux

package agent

// Unknown free space skips the check rather than blocking runs.
func availableDiskBytes(string) (uint64, bool) { return 0, false }
