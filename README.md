# cpa-today-plugin

CLIProxyAPI 的 **Today**（today.ai / todayai.cn）供应商插件。把 Today App/Web 的私有协议翻译成标准 OpenAI / Anthropic 接口，由 CPA 统一对外提供。

不需要额外进程、不需要反代中间层——插件以 c-shared 动态库形式被 CPA 直接加载，和内置供应商一样参与路由、冷却、轮换与用量统计。

支持 Windows / Linux / macOS，x86_64 与 arm64。

## 能力

| 能力 | 说明 |
|---|---|
| `auth_provider` | 解析 `today-*.json` 凭证；粘贴 Cookie 登录、邮箱 OTP 登录；续期校验 |
| `model_provider` | 静态模型 + 按凭证实时探测 `/v1/pricing/status` |
| `executor` | 把 chat-completions 请求翻译成 Today 私有协议，轮询取回，输出 OpenAI / Anthropic 两种格式 |
| `quota_provider` | 套餐、已用量、剩余比例、分项明细 |
| `management_api` | `/v0/management/plugins/today/*` 状态、账号、额度、直连测试 |
| 内嵌控制台 | `/v0/resource/plugins/today/console` |

## 安装

### 从 Release 安装（推荐）

1. 打开 [Releases](https://github.com/jzPPP3333/cpa-today-plugin/releases)，按平台下载归档：

   | 平台 | 归档 |
   | --- | --- |
   | Linux x86_64 | `today_<版本>_linux_amd64.zip` |
   | Linux arm64 | `today_<版本>_linux_arm64.zip` |
   | macOS Apple Silicon | `today_<版本>_darwin_arm64.zip` |
   | macOS Intel | `today_<版本>_darwin_amd64.zip` |
   | Windows x64 | `today_<版本>_windows_amd64.zip` |

2. 解压，把里面的动态库放到 CPA 插件目录的对应位置：

   ```
   <CPA>/plugins/windows/amd64/today.dll
   <CPA>/plugins/linux/amd64/today.so
   <CPA>/plugins/linux/arm64/today.so
   <CPA>/plugins/darwin/arm64/today.dylib
   <CPA>/plugins/darwin/amd64/today.dylib
   ```

   > 目录必须是 `<plugins>/<goos>/<goarch>/`，CPA 按这个布局扫描。

3. 在 `config.yaml` 启用：

   ```yaml
   plugins:
     enabled: true
     # 建议写绝对路径：GUI 启动核心进程时工作目录可能与手动启动不同，
     # 相对路径会导致插件目录解析失败（插件显示已配置但不生效）。
     dir: "/absolute/path/to/cpa/plugins"
     configs:
       today:
         enabled: true
         timeout_seconds: 240
         poll_interval_ms: 700
         default_model: "deepseek-v4.1-flash"
         context_turns: 0
         # 国内访问 todayai.cn 直连约 11s，走代理约 1s。强烈建议配置。
         proxy_url: "http://127.0.0.1:10808"
   ```

4. 重启 CPA。日志应出现：

   ```
   pluginhost: plugin loaded plugin_id=today ...
   pluginhost: plugin registered plugin_id=today plugin_name=Today version=0.1.0
   Registered client plugin:today:today:executor from provider today with N models
   ```

5. `GET /v1/models` 里出现带 `today-` 前缀的模型。

### 从源码编译

需要 Go 1.22+ 与 C 编译器（`CGO_ENABLED=1`）：

```bash
# Linux
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o dist/today.so .

# macOS
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o dist/today.dylib .

# Windows（MinGW-w64）
CGO_ENABLED=1 go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o dist/today.dll .
```

Linux arm64 交叉编译需要 `gcc-aarch64-linux-gnu`：

```bash
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc \
  go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o dist/today-arm64.so .
```

## 登录账号

浏览器打开内嵌控制台：

```
http://<CPA地址>:8317/v0/resource/plugins/today/console
```

先在页面顶部「管理密钥」填入 CPA 的 management secret-key（只存在本机 localStorage），再登录账号。

**方式一 · 粘贴 Cookie（推荐）**

浏览器登录 https://today.ai 或 https://todayai.cn → F12 → Application → Cookies → 复制
`__Secure-better-auth.session_token` 的值（或整段 `Cookie:` 头）粘贴提交。

插件会立即用这份 Cookie 换 JWT 验证，成功后才写入凭证文件。

> 国内账号（`todayai.cn`）与国际账号（`today.ai`）的 Cookie 不通用，页面上的「区域」要选对。

**方式二 · 邮箱验证码**

填邮箱 → 发送验证码 → 填验证码提交。走 `auth.todayai.cn` / `auth.today.ai` 的 `email-otp` 接口。

## 调用

模型名带 `today-` 前缀，避免与其它上游同名模型冲突：

```bash
# OpenAI 协议
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer <CPA_API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"today-deepseek-v4.1-flash","messages":[{"role":"user","content":"你好"}]}'

# Anthropic 协议
curl http://127.0.0.1:8317/v1/messages \
  -H "x-api-key: <CPA_API_KEY>" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"today-glm-5.3","max_tokens":256,"messages":[{"role":"user","content":"你好"}]}'
```

流式：`"stream": true`。

## 配置项

| 键 | 默认 | 说明 |
|---|---|---|
| `timeout_seconds` | 240 | 等待上游回复的总时长 |
| `poll_interval_ms` | 700 | 轮询间隔起点（自适应退避到 1s） |
| `default_model` | `deepseek-v4.1-flash` | 请求未带 model 时使用 |
| `context_turns` | 0 | 回传历史轮数；0 = 只发当前一轮 |
| `reasoning_hint` | 空 | 追加到提示词末尾（上游不返回 reasoning，可在正文里逼出来） |
| `proxy_url` | 空 | 插件出站代理。国内直连约 11s，走代理约 1s |
| `app_base` | 空 | 覆盖 `https://today.ai`（自建 mock 或中转用） |
| `api_base` | 空 | 覆盖 `https://api.today.ai` |

## 管理 API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/v0/management/plugins/today/status` | 插件状态、模型清单 |
| GET | `/v0/management/plugins/today/accounts` | 账号清单 |
| POST | `/v0/management/plugins/today/accounts/login` | `{cookie, realm}` 或 `{email}` / `{email, otp, realm}` |
| POST | `/v0/management/plugins/today/chat` | `{prompt, model}` 直连测试 |
| GET | `/v0/management/plugins/today/quota?auth_index=<idx>` | 套餐与用量 |

## 上游协议

```
浏览器 session cookie (__Secure-better-auth.session_token)
  └─ POST {app_base}/api/token {audience, expiresIn}
       → {token, expiresIn}                        API JWT (约 1h)
          └─ Authorization: Bearer <JWT> → {api_base}/v1/*
```

对话（**不是** OpenAI 协议，也没有 SSE）：

```
POST /v1/messages  {content, messageType:"text", clientMessageId, channel:"main"}
     → 202 {threadId, messageId}
GET  /v1/conversations/messages?isMainChat=true&limit=3
     → data.items[]  取 threadId 匹配、role=assistant、state=completed 的那条
       正文在 body.parts[] 里 messageType=text 的 content.text
```

`channel` 字段是必需的：缺了它消息不进主对话，后续轮询永远查不到这条 thread。

模型选项由 `PUT /v1/pricing/model-option {optionId}` 切换。

## 本地验证

仓库自带 mock 上游，用于验证成功路径而不消耗真实额度：

```bash
cd testtools
python -I mock_today.py 18999
```

然后在插件配置里把 `app_base` / `api_base` 都指向 `http://127.0.0.1:18999`。

## 已知限制

- **没有真流式**。上游是轮询模型，`stream:true` 只是把完整结果分片吐出，首字延迟高。
- **单轮文本**。`/v1/messages` 只接受一段文本，没有 system role；插件把历史压成 `[历史对话]` + `[当前消息]` 的纯文本。
- **工具调用不支持**。上游没有 function calling 概念。
- **上下文长度未知**。上游未公开，插件按 128k 保守声明。
- **模型清单以账号为准**。实际可用模型来自 `/v1/pricing/status`，随套餐变化；`internal/today/client.go` 的 `KnownModels` 只负责给常见模型配显示名，新增模型能自动调用。

## 开发

```bash
go vet ./...
go test ./... -count=1

# 打包插件商店归档（CI 用，也可本地验证）
go run ./scripts/packstore -lib dist/today.dll -id today -version 0.1.0 \
  -goos windows -goarch amd64 -out-dir dist
```

`cpasdk/` 是与 CPA 宿主 ABI 对齐的 vendored 副本，刻意不参与 gofmt。

## 许可证

MIT
