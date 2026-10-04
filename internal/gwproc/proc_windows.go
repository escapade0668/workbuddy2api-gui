//go:build windows

package gwproc

import (
	"os/exec"
	"syscall"
)

// Windows 没有 SIGTERM，os.Process.Signal(os.Interrupt) 也不受支持。网关用
// signal.NotifyContext(os.Interrupt) 等待退出信号，而 Go 运行时会把控制台的
// CTRL_BREAK_EVENT 转成 os.Interrupt —— 所以把网关放进独立进程组，停止时向该组
// 发 CTRL_BREAK，网关即可走正常的落盘 + 优雅停机流程。
//
// 独立进程组还有个副作用正合需要：在面板控制台按 Ctrl+C 不会直接打到网关，
// 由面板统一按顺序停止；关闭控制台窗口时两者都会收到 CTRL_CLOSE，各自落盘退出。

const ctrlBreakEvent = 1

var procGenerateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func interrupt(cmd *exec.Cmd) error {
	r, _, err := procGenerateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(cmd.Process.Pid))
	if r == 0 {
		return err
	}
	return nil
}
