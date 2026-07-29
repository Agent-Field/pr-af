//go:build windows

package main

import "os"

// Windows has no syscall.Flock. The mock CLI's state files are only test
// instrumentation, so retain its documented best-effort behavior there.
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) {}
