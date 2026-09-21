//go:build !unix

package orch

import "os/exec"

func configureGitProcess(_ *exec.Cmd) {}
