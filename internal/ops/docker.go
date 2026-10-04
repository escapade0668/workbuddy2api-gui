// docker.go 网关进程控制：查询网关状态、重启网关以加载新账号/新配置。
//
// 为什么需要重启：workbuddy2api 只在启动时扫描 auths/ 目录（SyncToDir）并读取
// config.json；新增账号或改配置后必须重启进程才会生效。GUI 复刻 login.sh 的行为，
// 免去用户登录服务器敲命令。两种方式：
//   - 托管模式（配置了 gateway_exec）：网关是面板的子进程，直接重启该进程；
//   - Docker 模式：`docker restart <docker_container>`。
package ops

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"workbuddy2api-gui/internal/gwproc"
)

// 网关控制方式（ContainerInfo.Mode）。
const (
	ModeDocker  = "docker"
	ModeProcess = "process"
)

// ContainerInfo 网关容器（或托管进程）状态快照。
type ContainerInfo struct {
	Mode      string    `json:"mode"` // docker | process
	Name      string    `json:"name"` // 容器名；托管模式下为网关可执行文件路径
	PID       int       `json:"pid,omitempty"`
	Available bool      `json:"available"` // docker 命令是否可用
	Exists    bool      `json:"exists"`
	Running   bool      `json:"running"`
	Status    string    `json:"status"`
	Health    string    `json:"health"`
	Image     string    `json:"image"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	Disabled  bool      `json:"disabled"` // 未配置容器名
}

// DockerAvailable 报告 docker CLI 是否可用（GUI 可能跑在无 docker 的环境里，
// 或容器内未挂载 docker.sock —— 此时重启能力自动降级，不影响其他功能）。
func (s *Service) DockerAvailable() bool {
	if s.cfg.DockerContainer == "" {
		return false
	}
	return exec.Command("docker", "version", "--format", "{{.Server.Version}}").Run() == nil
}

// SetGatewayProcess 启用托管模式：重启/状态查询改走该进程管理器。
func (s *Service) SetGatewayProcess(m *gwproc.Manager) { s.proc = m }

// GatewayRestartable 报告当前是否具备重启网关的手段（不含权限检查）。
func (s *Service) GatewayRestartable() bool {
	return s.proc != nil || s.cfg.DockerContainer != ""
}

// ContainerStatus 查询网关容器（或托管进程）状态。
func (s *Service) ContainerStatus(ctx context.Context) *ContainerInfo {
	if s.proc != nil {
		return processStatus(s.proc.Info())
	}
	info := &ContainerInfo{Mode: ModeDocker, Name: s.cfg.DockerContainer}
	if s.cfg.DockerContainer == "" {
		info.Disabled = true
		info.Error = "未配置容器名（docker_container），重启能力已关闭"
		return info
	}
	if _, err := exec.LookPath("docker"); err != nil {
		info.Error = "环境中找不到 docker 命令，重启能力不可用"
		return info
	}
	info.Available = true

	// 用 docker inspect 一次拿到状态/健康/镜像/启动时间。
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format",
		"{{.State.Running}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}-{{end}}|{{.Config.Image}}|{{.State.StartedAt}}",
		s.cfg.DockerContainer).Output()
	if err != nil {
		info.Error = "容器不存在或查询失败：" + firstLine(err.Error())
		return info
	}
	info.Exists = true
	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) >= 5 {
		info.Running = parts[0] == "true"
		info.Status = parts[1]
		info.Health = parts[2]
		info.Image = parts[3]
		if t, perr := time.Parse(time.RFC3339Nano, parts[4]); perr == nil {
			info.StartedAt = t
		}
	}
	return info
}

// processStatus 把托管进程状态映射到与容器相同的结构，前端无需两套渲染。
func processStatus(p gwproc.Info) *ContainerInfo {
	info := &ContainerInfo{
		Mode:      ModeProcess,
		Name:      p.Exec,
		Available: true,
		Exists:    true,
		Running:   p.Running,
		PID:       p.PID,
		Health:    "-",
		StartedAt: p.StartedAt,
	}
	if p.Running {
		info.Status = "running"
		if p.Restarts > 0 {
			info.Health = fmt.Sprintf("已自动拉起 %d 次（上次退出：%s）", p.Restarts, p.LastExit)
		}
	} else {
		info.Status = "exited"
		info.Error = p.LastExit
	}
	return info
}

// RestartGateway 重启网关（高危操作）：托管模式重启子进程，否则 docker restart。
func (s *Service) RestartGateway(ctx context.Context) (string, error) {
	if err := s.ensureDangerous(); err != nil {
		return "", err
	}
	if s.proc != nil {
		if err := s.proc.Restart(ctx); err != nil {
			return "", fmt.Errorf("重启网关进程失败: %w", err)
		}
		// 等新进程开始监听再返回：调用方（如登录流程）随后会提示「新账号已加载」。
		pid := s.proc.Info().PID
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			if _, err := s.gw.Health(ctx); err == nil {
				return fmt.Sprintf("网关进程已重启（pid %d）", pid), nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return "", fmt.Errorf("网关进程已重启（pid %d），但 15 秒内未就绪，请查看面板窗口里的 [网关] 日志", pid)
	}
	if s.cfg.DockerContainer == "" {
		return "", fmt.Errorf("未配置容器名（docker_container），无法重启")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("环境中找不到 docker 命令，无法重启")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "restart", s.cfg.DockerContainer).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", fmt.Errorf("重启容器失败: %s", firstLine(text))
	}
	return fmt.Sprintf("容器 %s 已重启", s.cfg.DockerContainer), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
