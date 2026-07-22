//go:build windows

package mail

func shellName() string { return "cmd" }
func shellFlag() string { return "/c" }
