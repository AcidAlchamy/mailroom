//go:build !windows

package mail

func shellName() string { return "/bin/sh" }
func shellFlag() string { return "-c" }
