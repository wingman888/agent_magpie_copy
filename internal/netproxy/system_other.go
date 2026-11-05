//go:build !darwin && !windows

package netproxy

// systemConfig has no system proxy source off macOS and Windows: Linux
// relies on the environment alone.
var systemConfig = func() (*sysConfig, error) { return nil, nil }
