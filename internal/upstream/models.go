package upstream

import (
	"encoding/json"
	"fmt"

	"workbuddy2api-gui/internal/authstore"
)

// CatalogModel 上游模型目录条目（/v3/config 的 data.models）。
type CatalogModel struct {
	ID                string
	Name              string
	Description       string // descriptionZh，缺省回落 descriptionEn
	Credits           string // 积分倍率原文，如 "x0.79" / "x0.34 credits"；缺席或空串 = 未标倍率
	Vendor            string
	Tags              []string
	IsDefault         bool
	OnlyReasoning     bool
	SupportsImages    bool
	SupportsReasoning bool
	SupportsToolCall  bool
	MaxAllowedSize    int64
	ContextLength     int64 // = maxInputTokens
	MaxOutput         int64 // = maxOutputTokens
	ReasoningEffort   string
	ReasoningSummary  string
}

// RealmConfig 一个域的 /v3/config 解析结果：模型目录 + 促销。
//
// 两者同源但互不拖累：目录解析失败只记 ModelsErr，促销照常返回。
type RealmConfig struct {
	Models     []CatalogModel
	ModelsErr  string
	Promotions []ModelPromotion
}

// Config 拉取账号**所在域**的模型目录与促销（一次请求）。
//
// 为什么模型目录也从这里取、而不是网关用的 /console/enterprises/personal/models：
// 2026-10-05 实测，后者在国际版（workbuddy.ai）恒返回 500 HTML（无论 token 与请求头），
// 网关失败后静默回落内置静态表，所以从没报过错；/v3/config 两个域都正常，且带倍率。
//
// 域归属由账号决定：旧版网关 /v1/models 只用随机一个账号取目录、返回裸 id，
// 双域并存时无法区分（模型页曾因此把国际版模型显示到国内版栏、另一个域整栏丢失）。
//
// 两次请求、两种身份（2026-10-05 实测同一账号两种 UA 拿到的配置不同）：
//   - 促销用桌面端身份：CLI 身份拿到的配置里没有 modelPromotions；
//   - 模型目录用网关同款 CLI 身份：桌面端列表里有网关调不通的模型（如 gpt-6-luna →
//     "service info not found"），CLI 列表才与网关实际可路由的一致。
func (c *Client) Config(a *authstore.Account) (*RealmConfig, error) {
	data, err := c.fetchV3Config(a, false)
	if err != nil {
		return nil, err
	}
	promos, err := parsePromotions(data)
	if err != nil {
		return nil, err
	}
	cfg := &RealmConfig{Promotions: promos}
	cliData, err := c.fetchV3Config(a, true)
	if err != nil {
		cfg.ModelsErr = err.Error()
		return cfg, nil
	}
	if models, err := parseConfigModels(cliData); err != nil {
		cfg.ModelsErr = err.Error()
	} else {
		cfg.Models = models
	}
	return cfg, nil
}

// parseConfigModels 解析 data.models，只保留 agents 里 cli 的模型列表（按其顺序）。
//
// data.models 是全集：实测国内版 54 个，其中 37 个是 codewise-completions、
// hunyuan-image-alpha 之类的补全/图片/内部模型，cli 列表里只有 17 个可对话调用。
func parseConfigModels(data json.RawMessage) ([]CatalogModel, error) {
	type agent struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	}
	var resp struct {
		Models []struct {
			ID                string   `json:"id"`
			Name              string   `json:"name"`
			DescriptionZh     string   `json:"descriptionZh"`
			DescriptionEn     string   `json:"descriptionEn"`
			Credits           string   `json:"credits"`
			Vendor            string   `json:"vendor"`
			Tags              []string `json:"tags"`
			IsDefault         bool     `json:"isDefault"`
			OnlyReasoning     bool     `json:"onlyReasoning"`
			SupportsImages    bool     `json:"supportsImages"`
			SupportsReasoning bool     `json:"supportsReasoning"`
			SupportsToolCall  bool     `json:"supportsToolCall"`
			MaxAllowedSize    int64    `json:"maxAllowedSize"`
			MaxInputTokens    int64    `json:"maxInputTokens"`
			MaxOutputTokens   int64    `json:"maxOutputTokens"`
			Disabled          bool     `json:"disabled"`
			Reasoning         struct {
				Effort        string `json:"effort"`
				DefaultEffort string `json:"defaultEffort"`
				Summary       string `json:"summary"`
			} `json:"reasoning"`
		} `json:"models"`
		Agents []agent `json:"agents"`
		// 未登录时上游给的是 data.agent.agents 形态，一并兼容。
		Agent struct {
			Agents []agent `json:"agents"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("模型目录解析失败: %w", err)
	}
	var cliIDs []string
	for _, ag := range append(resp.Agents, resp.Agent.Agents...) {
		if ag.Name == "cli" {
			cliIDs = ag.Models
			break
		}
	}
	if len(cliIDs) == 0 {
		return nil, fmt.Errorf("上游配置里没有 cli 模型列表（token 可能已失效）")
	}
	byID := make(map[string]int, len(resp.Models))
	for i, m := range resp.Models {
		byID[m.ID] = i
	}
	out := make([]CatalogModel, 0, len(cliIDs))
	for _, id := range cliIDs {
		i, ok := byID[id]
		if !ok || resp.Models[i].Disabled {
			continue
		}
		m := resp.Models[i]
		desc := m.DescriptionZh
		if desc == "" {
			desc = m.DescriptionEn
		}
		effort := m.Reasoning.DefaultEffort
		if effort == "" {
			effort = m.Reasoning.Effort
		}
		out = append(out, CatalogModel{
			ID: m.ID, Name: m.Name, Description: desc, Credits: m.Credits, Vendor: m.Vendor, Tags: m.Tags,
			IsDefault: m.IsDefault, OnlyReasoning: m.OnlyReasoning, SupportsImages: m.SupportsImages,
			SupportsReasoning: m.SupportsReasoning, SupportsToolCall: m.SupportsToolCall,
			MaxAllowedSize: m.MaxAllowedSize, ContextLength: m.MaxInputTokens, MaxOutput: m.MaxOutputTokens,
			ReasoningEffort: effort, ReasoningSummary: m.Reasoning.Summary,
		})
	}
	return out, nil
}
