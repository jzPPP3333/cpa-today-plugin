package app

import (
	"strconv"
	"strings"
)

// Config 是插件配置（来自 config.yaml 的 plugins.configs.today）。
type Config struct {
	// TimeoutSeconds 是等待上游回复的总时长。
	TimeoutSeconds int
	// PollIntervalMs 是轮询间隔起点。
	PollIntervalMs int
	// DefaultModel 是请求未指定模型时使用的模型 ID。
	DefaultModel string
	// ReasoningHint 非空时追加到提示词末尾（上游不返回 reasoning）。
	ReasoningHint string
	// ContextTurns 是回传给上游的历史轮数（0 表示只发当前一轮）。
	ContextTurns int
	// AppBase / APIBase 覆盖上游基址（指向自建 mock 或中转时使用）。
	AppBase string
	APIBase string
	// ProxyURL 是插件出站代理。实测 todayai.cn 直连 11s、走代理 1s，
	// 不设代理会让每次对话额外多等十几秒。
	ProxyURL string
	// Raw 保存未识别的原始键。
	Raw map[string]string
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		TimeoutSeconds: 240,
		PollIntervalMs: 700,
		DefaultModel:   "deepseek-v4.1-flash",
		ContextTurns:   0,
		Raw:            map[string]string{},
	}
}

// DecodeConfig 解析宿主下发的 config_yaml。
//
// 宿主传的是该插件块下的键值（plugins.configs.today），形态不固定：
// 可能是嵌套 map，也可能是标量。只认本插件关心的键，其余原样留存。
func DecodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(raw) == 0 {
		return cfg, nil
	}
	flat, errFlat := flattenYAML(raw)
	if errFlat != nil {
		return cfg, errFlat
	}
	for key, value := range flat {
		cfg.Raw[key] = value
		switch strings.ToLower(key) {
		case "timeout_seconds", "timeout":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				cfg.TimeoutSeconds = n
			}
		case "poll_interval_ms":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				cfg.PollIntervalMs = n
			}
		case "default_model":
			if v := strings.TrimSpace(value); v != "" {
				cfg.DefaultModel = v
			}
		case "reasoning_hint":
			cfg.ReasoningHint = value
		case "context_turns":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				cfg.ContextTurns = n
			}
		case "app_base":
			cfg.AppBase = strings.TrimRight(strings.TrimSpace(value), "/")
		case "api_base":
			cfg.APIBase = strings.TrimRight(strings.TrimSpace(value), "/")
		case "proxy_url":
			cfg.ProxyURL = strings.TrimSpace(value)
		}
	}
	return cfg, nil
}

// flattenYAML 做最小化的 YAML 解析：只处理 `key: value` 与两空格缩进的嵌套，
// 够读插件配置，且不引入第三方依赖（插件以 c-shared 构建，依赖越少越好）。
func flattenYAML(raw []byte) (map[string]string, error) {
	out := map[string]string{}
	var prefixes []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmedRight := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmedRight) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(trimmedRight), "#") {
			continue
		}
		indent := len(trimmedRight) - len(strings.TrimLeft(trimmedRight, " "))
		level := indent / 2
		content := strings.TrimSpace(trimmedRight)
		idx := strings.Index(content, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(content[:idx])
		value := strings.TrimSpace(content[idx+1:])
		key = strings.Trim(key, `"'`)
		for len(prefixes) > level {
			prefixes = prefixes[:len(prefixes)-1]
		}
		if value == "" {
			for len(prefixes) < level {
				prefixes = append(prefixes, "")
			}
			if len(prefixes) == level {
				prefixes = append(prefixes, key)
			} else {
				prefixes[level] = key
			}
			continue
		}
		full := key
		if len(prefixes) > 0 && level > 0 {
			parts := make([]string, 0, level+1)
			for i := 0; i < level && i < len(prefixes); i++ {
				if prefixes[i] != "" {
					parts = append(parts, prefixes[i])
				}
			}
			parts = append(parts, key)
			full = strings.Join(parts, ".")
		}
		out[full] = strings.Trim(value, `"'`)
	}
	// 空配置（全注释 / 只有空白）是合法状态：返回空表，让调用方用默认值。
	// 早先这里报错会让插件带着一份只含注释的配置启动失败。
	return out, nil
}
