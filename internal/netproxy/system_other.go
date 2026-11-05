//go:build !darwin && !windows

package netproxy

// readSystem reports no system proxy on other systems. Desktop sessions there
// usually pass their proxy on as environment variables.
func readSystem() sysConf {
	return sysConf{}
}
