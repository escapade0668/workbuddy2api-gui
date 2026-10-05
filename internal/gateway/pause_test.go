package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSetAccountPaused 走对路径、带鉴权；各类失败给出可读提示。
func TestSetAccountPaused(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		switch {
		case strings.Contains(r.URL.Path, "/missing/"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"account not found: missing"}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/accounts/"):
			w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r) // 旧网关：路由不存在
		}
	}))
	defer srv.Close()
	c := New(srv.URL, func() string { return "k" }, 0)
	ctx := context.Background()

	if err := c.SetAccountPaused(ctx, "u1", true); err != nil || gotPath != "/v1/accounts/u1/pause" || gotAuth != "Bearer k" {
		t.Fatalf("pause: err=%v path=%s auth=%s", err, gotPath, gotAuth)
	}
	if err := c.SetAccountPaused(ctx, "u1", false); err != nil || gotPath != "/v1/accounts/u1/resume" {
		t.Fatalf("resume: err=%v path=%s", err, gotPath)
	}
	if err := c.SetAccountPaused(ctx, "missing", true); err == nil || !strings.Contains(err.Error(), "没有该账号") {
		t.Errorf("未知账号应提示不在池中: %v", err)
	}

	old := New(srv.URL+"/legacy", func() string { return "" }, 0)
	if err := old.SetAccountPaused(ctx, "u1", true); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Errorf("旧网关应提示升级: %v", err)
	}
}
