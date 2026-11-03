//go:build !darwin && !windows

package netproxy

// readSystemProxy does not read the system proxy on other platforms. Desktop
// environments pass their proxy to applications as environment variables.
func readSystemProxy() systemSettings {
	return systemSettings{}
}
