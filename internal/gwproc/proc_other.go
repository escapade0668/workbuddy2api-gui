//go:build !windows

package gwproc

import (
	"os/exec"
	"syscall"
)

// setProcAttr 把网关放进独立进程组：终端里按 Ctrl+C 时 SIGINT 只打到面板，
// 由面板按顺序停止网关（与 Windows 的 CREATE_NEW_PROCESS_GROUP 同理），
// 避免两者同时收信号、网关先退出被误判为崩溃。
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interrupt 发 SIGTERM，网关收到后落盘并优雅停机。
func interrupt(cmd *exec.Cmd) error {
	return cmd.Process.Signal(syscall.SIGTERM)
}
