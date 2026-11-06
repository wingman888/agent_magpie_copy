//go:build !darwin && !windows

package netproxy

// loadSystem has nothing beyond the environment to say on Linux and others.
func loadSystem() systemProxy { return systemProxy{} }
