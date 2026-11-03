//go:build !darwin && !windows

package netproxy

// readPlatformProxy does not read the system proxy on this platform. Desktop
// environments normally pass their proxy to applications as environment
// variables, which are handled before this step.
func readPlatformProxy() systemProxy {
	return systemProxy{}
}
