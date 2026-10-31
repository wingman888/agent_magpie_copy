//go:build !darwin && !windows

package netproxy

// Linux has no system proxy beyond the environment.
func readSystem() *SystemConfig { return nil }
