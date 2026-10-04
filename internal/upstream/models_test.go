package upstream

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"workbuddy2api-gui/internal/authstore"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubClient 返回固定响应体的客户端，并按顺序记录全部请求。
func stubClient(t *testing.T, body string) (*Client, *[]*http.Request) {
	t.Helper()
	var got []*http.Request
	c := New(0)
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	return c, &got
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ids(models []CatalogModel) string {
	var out []string
	for _, m := range models {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

// testdata 取自真实账号的 /v3/config 响应（裁剪，结构原样）。
//
// 关键事实（2026-10-05 实测）：
//   - /console/enterprises/personal/models 在国际版恒返回 500 HTML，国际版目录只能从 /v3/config 取；
//   - data.models 是全集（国内版 54 个，含 codewise-completions 等内部模型），
//     data.agents 里 cli 的 models 才是可调用列表（国内版 17 个）——必须按它过滤。
func TestConfigGlobal(t *testing.T) {
	c, got := stubClient(t, fixture(t, "v3config-global.json"))
	cfg, err := c.Config(&authstore.Account{Domain: "www.workbuddy.ai", AccessToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	// 两次请求：促销用桌面端身份（CLI 身份取不到 modelPromotions），模型目录用网关同款
	// CLI 身份（桌面端列表里有网关调不通的模型，如 gpt-6-luna）。两边口径因此一致。
	reqs := *got
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	var sawDesktop, sawCLI bool
	for _, req := range reqs {
		if req.URL.Host != "www.workbuddy.ai" || req.URL.Path != EndpointV3Config || req.Header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("应带 token 请求国际版 /v3/config，实际 %s %v", req.URL, req.Header)
		}
		ua := req.Header.Get("User-Agent")
		sawDesktop = sawDesktop || strings.Contains(ua, "WorkBuddy AI")
		sawCLI = sawCLI || ua == clientUA
	}
	if !sawDesktop || !sawCLI {
		t.Fatalf("应分别用桌面端（促销）与 CLI（目录）身份各取一次: %v / %v", reqs[0].Header.Get("User-Agent"), reqs[1].Header.Get("User-Agent"))
	}
	if cfg.ModelsErr != "" {
		t.Fatalf("ModelsErr = %s", cfg.ModelsErr)
	}
	// 只保留 cli 列表里的模型，按 cli 顺序；deep-model / 图片模型等不在 cli 列表的剔除。
	if got := ids(cfg.Models); got != "default-model,hy4-preview-f,gpt-5.6-sol,glm-5.2" {
		t.Fatalf("models = %s", got)
	}
	glm := cfg.Models[3]
	if glm.Name != "GLM-5.2" || glm.Credits != "x0.79" || glm.ContextLength != 1000000 || glm.MaxOutput != 48000 {
		t.Errorf("字段映射不对: %+v", glm)
	}
	if glm.Description != "1M 上下文，擅长长程任务" || glm.Vendor != "e" || !glm.SupportsToolCall || !glm.SupportsReasoning {
		t.Errorf("描述/厂商/能力不对: %+v", glm)
	}
	if glm.ReasoningEffort != "high" { // 新形态：reasoning.defaultEffort
		t.Errorf("ReasoningEffort = %q", glm.ReasoningEffort)
	}
	// default-model 的 credits 是空串：保持为空（页面显示「—」），不能当免费。
	if def := cfg.Models[0]; def.Credits != "" || !def.IsDefault {
		t.Errorf("default-model = %+v", def)
	}
	// 同一份响应里的促销照常解析，不因加了模型解析而丢失。
	if len(cfg.Promotions) != 1 {
		t.Errorf("promotions = %d", len(cfg.Promotions))
	}
}

func TestConfigCN(t *testing.T) {
	c, got := stubClient(t, fixture(t, "v3config-cn.json"))
	cfg, err := c.Config(&authstore.Account{Domain: "www.codebuddy.cn", AccessToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if (*got)[0].URL.Host != "copilot.tencent.com" {
		t.Fatalf("应请求国内版，实际 %s", (*got)[0].URL)
	}
	if got := ids(cfg.Models); got != "auto,glm-5.2,deepseek-v4.1-flash,kimi-k3-1" {
		t.Fatalf("应剔除 codewise-completions 等内部模型: %s", got)
	}
	auto := cfg.Models[0]
	if auto.Credits != "" || auto.ReasoningEffort != "high" { // 旧形态：reasoning.effort；credits 字段缺席
		t.Errorf("auto = %+v", auto)
	}
	if cfg.Models[3].Credits != "x1.62" {
		t.Errorf("kimi-k3-1 = %+v", cfg.Models[3])
	}
}

// 没有 cli 列表时：模型目录报错，但促销照常返回（促销与目录同源，互不拖累）。
func TestConfigNoCLIAgent(t *testing.T) {
	body := `{"code":0,"data":{"models":[{"id":"a"}],"agents":[],"modelPromotions":[{"id":"p","enabled":true,"modelIds":["a"]}]}}`
	c, _ := stubClient(t, body)
	cfg, err := c.Config(&authstore.Account{Domain: "www.workbuddy.ai"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelsErr == "" || len(cfg.Models) != 0 {
		t.Fatalf("没有 cli 列表应报错而不是返回空目录冒充「该域没有模型」: %+v", cfg)
	}
	if len(cfg.Promotions) != 1 {
		t.Fatalf("促销不应受影响: %+v", cfg.Promotions)
	}
}
