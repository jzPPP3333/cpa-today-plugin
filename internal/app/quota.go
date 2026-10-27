package app

// quota provider：从 /v1/pricing/status 读出套餐与用量。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"cpa-today-plugin/cpasdk/pluginapi"
	"cpa-today-plugin/internal/core"
	"cpa-today-plugin/internal/hostbridge"
)

type quotaFetchRPC struct {
	pluginapi.QuotaFetchRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func (a *App) handleQuotaDescribe() ([]byte, error) {
	return core.OKEnvelope(pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{ProviderKey},
		DisplayName:        DisplayName,
		SupportsReset:      false,
	})
}

func (a *App) handleQuotaFetch(request []byte) ([]byte, error) {
	var rpc quotaFetchRPC
	if errDecode := decodeRequest(request, &rpc); errDecode != nil {
		return nil, core.NewPluginError("invalid_request", errDecode.Error(), http.StatusBadRequest)
	}
	cred, ok := parseCredential(rpc.StorageJSON, rpc.AuthID+".json")
	if !ok {
		return nil, core.NewPluginError("invalid_credential", "credential is not a today credential", http.StatusBadRequest)
	}
	ctx, cancel := context.WithTimeout(hostbridge.WithCallbackID(context.Background(), rpc.HostCallbackID), hostbridge.DefaultTimeout)
	defer cancel()

	cfg := a.Config()
	client := clientFor(cred, cfg)
	status, errStatus := client.PricingStatus(ctx)
	if errStatus != nil {
		return nil, core.ToPluginError(errStatus)
	}

	resp := pluginapi.QuotaFetchResponse{}
	plan, _ := status["plan"].(string)
	if plan == "" {
		if planBlock, okPlan := status["plan"].(map[string]any); okPlan {
			plan, _ = planBlock["id"].(string)
		}
	}
	if plan != "" {
		resp.Subscription = &pluginapi.QuotaSubscription{Plan: plan, TierName: plan}
	}

	// 上游实际结构：usage.progress = 已用百分比整数，
	// usage.resetAt = 重置时间，usage.breakdown.items[] = 各类别明细。
	if usageBlock, okUsage := status["usage"].(map[string]any); okUsage {
		usedPercent, _ := number(usageBlock["progress"])
		remaining := 1 - usedPercent/100
		if remaining < 0 {
			remaining = 0
		}
		bucket := pluginapi.QuotaBucket{
			Window:            strOr(usageBlock["period"], "monthly"),
			RemainingFraction: remaining,
			ResetTime:         strOr(usageBlock["resetAt"], ""),
			Description:       strOr(usageBlock["progressText"], ""),
		}
		resp.Groups = append(resp.Groups, pluginapi.QuotaGroup{
			DisplayName: "Today 套餐",
			Buckets:     []pluginapi.QuotaBucket{bucket},
		})
		resp.Summary = append(resp.Summary,
			pluginapi.QuotaMetric{Key: "used_percent", Label: "已用", Value: usedPercent, Unit: "%", Format: "number"},
			pluginapi.QuotaMetric{Key: "remaining_percent", Label: "剩余", Value: 100 - usedPercent, Unit: "%", Format: "number"})

		if breakdown, okBreakdown := usageBlock["breakdown"].(map[string]any); okBreakdown {
			if items, okItems := breakdown["items"].([]any); okItems {
				group := pluginapi.QuotaGroup{DisplayName: "用量明细"}
				for _, raw := range items {
					item, okItem := raw.(map[string]any)
					if !okItem {
						continue
					}
					used, _ := number(item["usedPercent"])
					label := strOr(item["key"], "")
					if display, okDisplay := item["display"].(map[string]any); okDisplay {
						if t := strOr(display["title"], ""); t != "" {
							label = t
						}
					}
					group.Buckets = append(group.Buckets, pluginapi.QuotaBucket{
						Description:       label,
						RemainingFraction: (100 - used) / 100,
						Window:            "monthly",
					})
				}
				if len(group.Buckets) > 0 {
					resp.Groups = append(resp.Groups, group)
				}
			}
		}
	}
	return core.OKEnvelope(resp)
}

func strOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

var _ = fmt.Sprintf
