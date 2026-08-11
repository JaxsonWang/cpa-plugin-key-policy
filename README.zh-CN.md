# cpa-key-policy（中文说明）

面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 WebSocket 兼容下游 API Key 策略插件。

插件签发 `cpa_…` key，在不覆盖 CPA 模型解析的前提下执行精确模型白名单、RPM、每日/每周预算以及 token/按次计费。

| | |
|---|---|
| **仓库** | [JaxsonWang/cpa-plugin-key-policy](https://github.com/JaxsonWang/cpa-plugin-key-policy) |
| **协议** | MIT |
| **English** | [README.md](./README.md) |

## v0.5.0 行为

核心不变量：

> 插件不再路由或改写模型。客户端必须发送 CPA 能原生解析的真实模型名；插件只判断该精确模型名是否允许。

这样 CPA 可以继续使用原生 Responses WebSocket。插件只注册：

- 前端鉴权；
- 请求拦截；
- 用量统计；
- 管理 API 与内嵌 UI。

不再注册 `model.route`、scheduler 或响应拦截器。

### 保留

- `cpa_…` key 鉴权；
- 精确且大小写不敏感的模型白名单；
- 单 key RPM；
- 每日与滚动每周美元上限；
- token 单价与固定按次计费；
- 单 key/单模型用量及金额统计；
- key 创建、编辑、轮换、撤销、RPM 重置、全局每日/每周用量重置与用量 UI；
- `GET /v1/models` 的二值 `allow_models_endpoint` 权限。

### 删除

- 别名及模型名改写；
- 指定 provider；
- 多目标 priority/round-robin；
- 凭证 group/tier 调度；
- 自定义凭证分类；
- 插件侧 catalog 聚合。

模型选择器中的 provider 只用于展示 CPA 发现结果，不写入 key policy，也不参与运行时路由。

## WebSocket 修复原理

旧版 `model.route` 返回已处理的模型覆盖后，CPA 会关闭该请求的原生 WebSocket passthrough。带 `previous_response_id` / `response.append` 的增量帧可能要求 HTTP replay，日志出现：

```text
1012 upstream requires HTTP replay
```

v0.5.0 让 CPA 原生解析真实模型：

1. HTTP WebSocket Upgrade 阶段只确认 `cpa_…` key 存在且启用；Upgrade 没有模型执行，因此不消耗 RPM。
2. 每个 WebSocket 执行帧进入 `request.intercept_before` 后，使用原始 Authorization header 检查真实模型、RPM 与预算。
3. 普通 HTTP 请求继续在前端鉴权阶段完整校验，request interceptor 不重复计数。
4. 未知 key 或 CPA 原生 key 交给其他 CPA 鉴权 provider，不受本插件干扰。

## 配置

CLIProxyAPI 插件配置：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-policy:
      enabled: true
      priority: 10
      state_file: "cpa-key-policy-state.json"
```

CPA 插件商店源：

```text
https://raw.githubusercontent.com/JaxsonWang/cpa-plugin-key-policy/main/registry.json
```

将该 URL 添加到 `plugins.store-sources`，然后选择 `JaxsonWang` 来源的
`cpa-key-policy`。如果已经安装官方源中的 `origin652` 版本，需要先卸载该插件
条目再切换商店来源。卸载前先记录
`plugins.configs.cpa-key-policy.state_file`，因为卸载会清除插件配置，但不会删除
独立的 state file。安装 `JaxsonWang` 版本后，先恢复相同的 `state_file` 路径，
再打开 Key Policy 页面核对原有 key 与用量数据。

规范的 key policy：

```yaml
enabled: true
state_file: ./cpa-key-policy-state.json

keys:
  - id: team-a
    name: Team A
    enabled: true
    key_hash: "sha256:REPLACE_WITH_SHA256_HEX"
    key_preview: "cpa_..."
    rpm: 60
    daily_limit_usd: 10
    weekly_limit_usd: 50
    allow_models_endpoint: false
    models:
      - model: gpt-5.4
        billing_mode: tokens
        input_price_per_million: 2
        output_price_per_million: 8
        cache_read_price_per_million: 0.2
```

优先使用网页或管理 API 生成 key；明文只返回一次。插件 key 不要再加入 CPA 原生 `api-keys`，否则会形成另一条原生鉴权路径。

已有 state file 时，其中的 keys 与 usage 是运行时数据源。种子格式见 [`config.example.yaml`](./config.example.yaml)。

## 从 v0.4.x 升级

v0.5.0 state 版本为 `2`，加载旧配置/状态时自动迁移：

- 旧直连规则的 `target_model` 迁移为 `{model: target_model}`；
- 旧全局别名的每个 target 迁移成一个真实模型；
- 多 target 拆成多个允许的真实模型；
- 同名真实模型去重；
- 删除 alias/provider/group 路由字段；
- 保留定价；
- 旧 `usage.by_alias` 作为历史 residual 保留，因为旧别名无法可靠归属到某一个 target model。

升级后：

- 客户端必须发送真实 CPA 模型名，旧别名请求名不再生效；
- 如需回滚，首次使用 v0.5.0 前先备份 state file；
- 新建 Codex session 或重启客户端，旧 session 可能已经缓存 SSE fallback。

## 管理网页

插件加载后访问：

```text
http://HOST:PORT/v0/resource/plugins/cpa-key-policy/index.html
```

使用 CPA management secret 登录。网页保留 key 管理、真实模型选择、定价、预算/RPM 与按模型用量；管理密钥只保存在内存，不写 `localStorage`。

开发模式：

```bash
cd web
npm ci
VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev
```

## 管理 API

`/v0/management/plugins/cpa-key-policy` 下只保留：

- `GET/POST/PATCH/DELETE /keys`；
- `POST /keys/rotate`；
- `POST /keys/reset-rpm`；
- `POST /keys/reset-usage`（清空所有 key 的每日与每周用量）；
- `GET /keys/usage`；
- `GET /status`。

创建 key，`plain_key` 只返回一次：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/keys" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "team-a",
    "name": "Team A",
    "rpm": 60,
    "daily_limit_usd": 10,
    "weekly_limit_usd": 50,
    "models": [
      {
        "model": "gpt-5.4",
        "billing_mode": "tokens",
        "input_price_per_million": 2,
        "output_price_per_million": 8,
        "cache_read_price_per_million": 0.2
      }
    ]
  }'
```

## 构建与测试

```bash
go test ./...

gofmt -w internal/plugin internal/policy
go vet ./...
go test -race ./internal/policy ./internal/plugin

cd web
npm ci
npm test
npm run typecheck
npm run build
```

构建内嵌网页和 Linux 共享库：

```bash
make web-build
make build-linux-amd64
# 或：make build-linux
```

## WebSocket 验收

1. 安装新插件并重启 CPA。
2. 使用 `Authorization: Bearer cpa_…` 和真实模型名（如 `gpt-5.4`）创建新的 Codex session。
3. 至少连续完成两个 turn，覆盖 `previous_response_id` / `response.append`。
4. 确认 RPM/usage 按执行帧计数，而不是按 Upgrade 计数。
5. 确认新 session 的 CPA 日志不再出现 `1012 upstream requires HTTP replay`。
