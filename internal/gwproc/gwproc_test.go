package gwproc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess 不是真正的测试：被 Manager 当作「网关」拉起的子进程入口。
// GWPROC_HELPER=serve 时打印 ready 并等待中断信号后正常退出；=crash 时立即以 1 退出。
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("GWPROC_HELPER") {
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fmt.Println("ready")
		<-ctx.Done()
		fmt.Println("graceful-exit")
		os.Exit(0)
	case "crash":
		fmt.Println("boom")
		os.Exit(1)
	}
}

func helperManager(t *testing.T, mode string) (*Manager, *syncBuffer) {
	t.Helper()
	t.Setenv("GWPROC_HELPER", mode)
	out := &syncBuffer{}
	m, err := New(os.Args[0], "", out)
	if err != nil {
		t.Fatal(err)
	}
	m.args = []string{"-test.run=^TestHelperProcess$"}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m, out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("超时：%s", what)
}

func TestStartRestartStop(t *testing.T) {
	m, out := helperManager(t, "serve")
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "子进程输出 ready", func() bool { return strings.Contains(out.String(), "[网关] ready") })
	first := m.Info()
	if !first.Running || first.PID == 0 {
		t.Fatalf("启动后应在运行: %+v", first)
	}

	if err := m.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := m.Info()
	if !second.Running || second.PID == first.PID {
		t.Fatalf("重启后应是新进程: before=%d after=%+v", first.PID, second)
	}
	if !strings.Contains(out.String(), "[网关] graceful-exit") {
		t.Fatalf("重启应优雅停止旧进程，输出:\n%s", out.String())
	}

	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Info().Running {
		t.Fatal("Stop 后不应在运行")
	}
	// 主动停止后不应被自动拉起。
	time.Sleep(backoffMin + 500*time.Millisecond)
	if info := m.Info(); info.Running || info.Restarts != 0 {
		t.Fatalf("主动停止后不应自动拉起: %+v", info)
	}
}

func TestCrashAutoRestart(t *testing.T) {
	m, _ := helperManager(t, "crash")
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "崩溃后被自动拉起", func() bool { return m.Info().Restarts >= 1 })
	if info := m.Info(); !strings.Contains(info.LastExit, "exit status 1") {
		t.Fatalf("应记录退出原因: %+v", info)
	}
}

func TestCloseIsFinal(t *testing.T) {
	m, _ := helperManager(t, "serve")
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err == nil {
		t.Fatal("Close 之后 Start 应失败")
	}
}

func TestNewRejectsMissingExe(t *testing.T) {
	if _, err := New("./definitely-not-here.exe", "", nil); err == nil {
		t.Fatal("可执行文件不存在时应报错")
	}
}

func TestPrefixWriterSplitsLines(t *testing.T) {
	var buf bytes.Buffer
	w := &prefixWriter{w: &buf, prefix: "> "}
	_, _ = w.Write([]byte("a\nb"))
	_, _ = w.Write([]byte("c\n"))
	if got := buf.String(); got != "> a\n> bc\n" {
		t.Fatalf("got %q", got)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
