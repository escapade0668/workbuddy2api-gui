package api

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"workbuddy2api-gui/internal/gateway"
	"workbuddy2api-gui/internal/upstream"
)

// fakeRealms 模拟凭证目录里各域的账号与上游目录。
type fakeRealms struct {
	accounts map[string]bool
	catalogs map[string][]upstream.CatalogModel
	errs     map[string]string
	calls    []string
}

func (f *fakeRealms) HasAccountInRealm(realm string) bool { return f.accounts[realm] }

func (f *fakeRealms) ModelsForRealm(realm string) ([]upstream.CatalogModel, string) {
	f.calls = append(f.calls, realm)
	return f.catalogs[realm], f.errs[realm]
}

func cat(ids ...string) []upstream.CatalogModel {
	out := make([]upstream.CatalogModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, upstream.CatalogModel{ID: id, Name: strings.ToUpper(id)})
	}
	return out
}

func gw(ids ...string) []gateway.Model {
	out := make([]gateway.Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, gateway.Model{ID: id, Object: "model"})
	}
	return out
}

// byRealm 把结果整理成 realm → 排好序的 id 列表，便于断言。
func byRealm(list []realmModel) map[string]string {
	tmp := map[string][]string{}
	for _, m := range list {
		tmp[m.Realm] = append(tmp[m.Realm], m.ID)
	}
	out := map[string]string{}
	for k, v := range tmp {
		sort.Strings(v)
		out[k] = strings.Join(v, ",")
	}
	return out
}

// 只有国际版账号：网关返回不带前缀的国际版目录 —— 以前会被归到国内版。
func TestAssembleModelsGlobalOnly(t *testing.T) {
	src := &fakeRealms{
		accounts: map[string]bool{"global": true},
		catalogs: map[string][]upstream.CatalogModel{"global": cat("hy4", "glm-5.2")},
	}
	got, errs, err := assembleModels(gw("hy4", "glm-5.2"), nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if r := byRealm(got); r["global"] != "glm-5.2,hy4" || r["cn"] != "" {
		t.Fatalf("国际版模型应归国际版: %v", r)
	}
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
}

// 双域账号：网关只随机返回一个域，另一个域以前整栏丢失。
func TestAssembleModelsBothRealms(t *testing.T) {
	src := &fakeRealms{
		accounts: map[string]bool{"global": true, "cn": true},
		catalogs: map[string][]upstream.CatalogModel{
			"global": cat("hy4", "glm-5.2"),
			"cn":     cat("glm-5.2", "deepseek-v4-pro"),
		},
	}
	// 网关这次恰好挑中了国内版账号。
	got, _, err := assembleModels(gw("glm-5.2", "deepseek-v4-pro"), nil, src)
	if err != nil {
		t.Fatal(err)
	}
	r := byRealm(got)
	if r["global"] != "glm-5.2,hy4" || r["cn"] != "deepseek-v4-pro,glm-5.2" {
		t.Fatalf("两个域都应完整: %v", r)
	}
}

// 倍率、描述、能力等以上游目录为准，原样带到每个域的条目上。
func TestAssembleModelsCarriesCatalogMetadata(t *testing.T) {
	src := &fakeRealms{
		accounts: map[string]bool{"global": true, "cn": true},
		catalogs: map[string][]upstream.CatalogModel{
			"global": {{ID: "glm-5.2", Name: "GLM-5.2", Credits: "x0.79", Description: "1M 上下文", ContextLength: 1000000, MaxOutput: 48000, SupportsToolCall: true, ReasoningEffort: "high"}},
			"cn":     {{ID: "glm-5.2", Name: "GLM-5.2", Credits: "x0.5"}},
		},
	}
	got, _, _ := assembleModels(gw("glm-5.2"), nil, src)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	for _, m := range got {
		switch m.Realm {
		case "global":
			if m.Credits != "x0.79" || m.Description != "1M 上下文" || m.ContextLength != 1000000 || m.MaxOutput != 48000 ||
				!m.SupportsToolCall || m.ReasoningEffort != "high" || m.Name != "GLM-5.2" {
				t.Errorf("国际版条目字段不对: %+v", m)
			}
		case "cn":
			if m.Credits != "x0.5" {
				t.Errorf("同名模型两个域的倍率不能串: %+v", m)
			}
		}
	}
}

// 网关已按域下发前缀（global:/cn:）时沿用网关结果，不额外打上游。
func TestAssembleModelsRealmAwareGateway(t *testing.T) {
	src := &fakeRealms{accounts: map[string]bool{"global": true, "cn": true}}
	got, _, err := assembleModels(gw("global:hy4", "glm-5.2", "cn:kimi-k3"), nil, src)
	if err != nil {
		t.Fatal(err)
	}
	r := byRealm(got)
	if r["global"] != "global:hy4" || r["cn"] != "cn:kimi-k3,glm-5.2" {
		t.Fatalf("got %v", r)
	}
	if len(src.calls) != 0 {
		t.Fatalf("网关已分域时不应直连上游: %v", src.calls)
	}
}

// 直连失败时：单域账号可安全回落到网关目录；错误照样上报。
func TestAssembleModelsFallbackToGatewayWhenSingleRealmFetchFails(t *testing.T) {
	src := &fakeRealms{
		accounts: map[string]bool{"global": true},
		errs:     map[string]string{"global": "上游超时"},
	}
	got, errs, err := assembleModels(gw("hy4"), nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if r := byRealm(got); r["global"] != "hy4" {
		t.Fatalf("应回落网关目录并归国际版: %v", r)
	}
	if errs["global"] == "" {
		t.Fatal("直连失败应在 model_errors 里说明")
	}
}

// 面板读不到任何账号（本地面板 + 远端网关）时保持旧行为，不丢数据。
func TestAssembleModelsNoLocalAccountsKeepsGatewayList(t *testing.T) {
	src := &fakeRealms{}
	got, _, err := assembleModels(gw("glm-5.2"), nil, src)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

// 网关不可达但直连能取到时，模型页照样能打开；两边都没有才报错。
func TestAssembleModelsGatewayDown(t *testing.T) {
	src := &fakeRealms{
		accounts: map[string]bool{"cn": true},
		catalogs: map[string][]upstream.CatalogModel{"cn": cat("glm-5.2")},
	}
	got, _, err := assembleModels(nil, errors.New("网关不可达"), src)
	if err != nil || byRealm(got)["cn"] != "glm-5.2" {
		t.Fatalf("got %+v err %v", got, err)
	}
	if _, _, err := assembleModels(nil, errors.New("网关不可达"), &fakeRealms{}); err == nil {
		t.Fatal("两边都拿不到时应返回网关错误")
	}
}

// 新网关（条目带 realms / credits_by_realm）：以网关为准逐域展开，不再直连上游取目录 ——
// 面板显示的就是网关实际能路由的模型（面板自己取的桌面端列表里有网关调不通的模型）。
func TestAssembleModelsRealmTaggedGateway(t *testing.T) {
	src := &fakeRealms{accounts: map[string]bool{"global": true, "cn": true}}
	g := []gateway.Model{
		{ID: "glm-5.2", Realms: []string{"cn", "global"}, CreditsByRealm: map[string]string{"cn": "x0.79 credits", "global": "x0.79"}, ContextLength: 1000000},
		{ID: "hy4-preview", Realms: []string{"global"}, Credits: "x0.29"},
		{ID: "hy3-x", Realms: []string{"cn"}, Credits: "x0.05 credits"},
	}
	got, errs, err := assembleModels(g, nil, src)
	if err != nil || len(errs) != 0 {
		t.Fatalf("err=%v errs=%v", err, errs)
	}
	if r := byRealm(got); r["global"] != "glm-5.2,hy4-preview" || r["cn"] != "glm-5.2,hy3-x" {
		t.Fatalf("got %v", r)
	}
	for _, m := range got {
		if m.ID == "glm-5.2" {
			want := map[string]string{"cn": "x0.79 credits", "global": "x0.79"}[m.Realm]
			if m.Credits != want || m.ContextLength != 1000000 {
				t.Errorf("%s/%s credits=%q ctx=%d", m.Realm, m.ID, m.Credits, m.ContextLength)
			}
		}
		if len(m.Realms) != 0 || len(m.CreditsByRealm) != 0 {
			t.Errorf("展开后的单域条目不应再带多域字段: %+v", m)
		}
	}
	if len(src.calls) != 0 {
		t.Fatalf("网关已给出分域目录时不应再直连上游: %v", src.calls)
	}
}
