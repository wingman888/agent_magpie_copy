//go:build !darwin && !windows

package netproxy

// readSystem: on other systems the desktop proxy usually reaches apps as
// environment variables, so there is no system proxy to read.
func readSystem() sysProxy {
	return sysProxy{}
}
