//go:build !windows

package cmd

// runAsService is Windows-only (service_windows.go); elsewhere GoOmni runs
// under the init system or container runtime as before.
func runAsService() (bool, error) { return false, nil }
