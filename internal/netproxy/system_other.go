//go:build !darwin && !windows

package netproxy

// readSystem: on Linux the environment is the only proxy source.
func readSystem() sysProxy { return sysProxy{} }
