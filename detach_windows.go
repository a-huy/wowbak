//go:build windows

package main

import "os/exec"

// detachProcess is a no-op on Windows: the launcher there is a separate
// windowsgui binary and does not need to re-spawn anything.
func detachProcess(cmd *exec.Cmd) {}
