// Package gwproc 把 workbuddy2api 网关作为面板的子进程托管：随面板启动、崩溃自动拉起、
// 面板退出时优雅停止。
//
// 为什么需要：Docker 之外的部署（典型是 Windows 双击运行）没有 `docker restart` 可用，
// 而网关只在启动时扫描 auths/ 与读取 config.json —— 面板添加账号/改配置后必须能重启它。
// 托管模式下面板本身就是网关的「进程管理器」，重启语义与 docker restart 一致。
package gwproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// stopGrace 优雅停止的等待上限：网关收到中断后会落盘 state.json / metrics 再退出，
// 超时则强杀（宁可丢最近几秒状态，也不能让重启卡死）。
const stopGrace = 8 * time.Second

// 崩溃重启退避：首次 2s，之后翻倍，封顶 60s；存活超过 stableAfter 视为恢复正常，退避归零。
const (
	backoffMin  = 2 * time.Second
	backoffMax  = 60 * time.Second
	stableAfter = 30 * time.Second
)

// Info 网关进程状态快照。
type Info struct {
	Exec      string    `json:"exec"`
	Running   bool      `json:"running"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Restarts  int       `json:"restarts"`            // 崩溃后被自动拉起的次数
	LastExit  string    `json:"last_exit,omitempty"` // 上一次退出原因
}

// Manager 托管单个网关进程。零值不可用，用 New 创建。
type Manager struct {
	exe  string
	args []string
	dir  string
	out  io.Writer

	mu        sync.Mutex
	cmd       *exec.Cmd
	done      chan struct{} // 当前进程退出时关闭
	startedAt time.Time
	restarts  int
	lastExit  string
	stopped   bool // 主动停止（Stop/Restart 期间）：退出不触发自动拉起
	closed    bool // 面板退出：永不再拉起
	backoff   time.Duration
}

// New 创建管理器。exe 为网关可执行文件路径，configFile 为网关 config.json；
// 网关的工作目录设为 exe 所在目录，使其配置里的相对路径（./auths、./data）落在网关旁边。
func New(exe, configFile string, out io.Writer) (*Manager, error) {
	absExe, err := filepath.Abs(exe)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(absExe); err != nil {
		return nil, fmt.Errorf("网关可执行文件 %s 不可用: %w", absExe, err)
	} else if st.IsDir() {
		return nil, fmt.Errorf("网关可执行文件 %s 是目录", absExe)
	}
	var args []string
	if configFile != "" {
		absCfg, err := filepath.Abs(configFile)
		if err != nil {
			return nil, err
		}
		args = []string{"-config", absCfg}
	}
	if out == nil {
		out = os.Stderr
	}
	return &Manager{exe: absExe, args: args, dir: filepath.Dir(absExe), out: out}, nil
}

// Start 启动网关（已在运行则无操作）。
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = false
	return m.startLocked()
}

func (m *Manager) startLocked() error {
	if m.closed {
		return errors.New("面板正在退出，不再启动网关")
	}
	if m.cmd != nil {
		return nil
	}
	cmd := exec.Command(m.exe, m.args...)
	cmd.Dir = m.dir
	w := &prefixWriter{w: m.out, prefix: "[网关] "}
	cmd.Stdout = w
	cmd.Stderr = w
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		m.lastExit = "启动失败: " + err.Error()
		return fmt.Errorf("启动网关失败: %w", err)
	}
	done := make(chan struct{})
	m.cmd, m.done, m.startedAt = cmd, done, time.Now()
	log.Printf("网关进程已启动（pid %d）：%s", cmd.Process.Pid, m.exe)
	go m.wait(cmd, done)
	return nil
}

// wait 等待进程退出；非主动停止的退出按退避策略自动拉起。
func (m *Manager) wait(cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	close(done)
	m.cmd, m.done = nil, nil
	if err != nil {
		m.lastExit = err.Error()
	} else {
		m.lastExit = "正常退出"
	}
	if m.stopped || m.closed {
		return
	}
	// 意外退出：存活够久说明不是启动即崩，退避归零。
	if time.Since(m.startedAt) > stableAfter {
		m.backoff = 0
	}
	if m.backoff == 0 {
		m.backoff = backoffMin
	} else if m.backoff *= 2; m.backoff > backoffMax {
		m.backoff = backoffMax
	}
	delay := m.backoff
	log.Printf("网关进程意外退出（%s），%s 后自动重启", m.lastExit, delay)
	time.AfterFunc(delay, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.stopped || m.closed || m.cmd != nil {
			return
		}
		m.restarts++
		if err := m.startLocked(); err != nil {
			log.Printf("自动重启网关失败: %v", err)
		}
	})
}

// Stop 停止网关并等待其退出；之后不会被自动拉起，直到再次 Start/Restart。
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	cmd, done := m.cmd, m.done
	m.mu.Unlock()
	return stopProcess(ctx, cmd, done)
}

// Close 面板退出时调用：停止网关且永不再拉起。
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	cmd, done := m.cmd, m.done
	m.mu.Unlock()
	return stopProcess(ctx, cmd, done)
}

// Restart 停止（若在运行）再启动，用于加载新账号/新配置。
func (m *Manager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = false
	m.backoff = 0
	return m.startLocked()
}

// Info 返回当前状态快照。
func (m *Manager) Info() Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	info := Info{Exec: m.exe, Restarts: m.restarts, LastExit: m.lastExit}
	if m.cmd != nil {
		info.Running = true
		info.PID = m.cmd.Process.Pid
		info.StartedAt = m.startedAt
	}
	return info
}

// stopProcess 先发中断请求优雅退出，超时或 ctx 取消则强杀。
func stopProcess(ctx context.Context, cmd *exec.Cmd, done chan struct{}) error {
	if cmd == nil {
		return nil
	}
	if err := interrupt(cmd); err != nil {
		_ = cmd.Process.Kill()
	}
	timer := time.NewTimer(stopGrace)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	log.Printf("网关未在 %s 内退出，强制结束（pid %d）", stopGrace, cmd.Process.Pid)
	_ = cmd.Process.Kill()
	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("无法结束网关进程（pid %d）", cmd.Process.Pid)
	}
}

// prefixWriter 给子进程输出逐行加前缀，与面板日志混排时可区分来源。
type prefixWriter struct {
	mu     sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		_, _ = p.w.Write(append([]byte(p.prefix), p.buf[:i+1]...))
		p.buf = p.buf[i+1:]
	}
	// 防御：子进程输出超长无换行内容时直接透传，避免无限增长。
	if len(p.buf) > 64<<10 {
		_, _ = p.w.Write(p.buf)
		p.buf = p.buf[:0]
	}
	return len(b), nil
}
