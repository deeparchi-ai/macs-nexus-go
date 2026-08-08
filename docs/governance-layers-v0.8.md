# Nexus v0.8: L0–L3 Governance Layer Standardization

## Motivation

v0.7 中 L0–L3 治理层的语义完全依赖 `cmd/nexusd/main.go` 注入的闭包（`layerOf`、`perm`），`pkg/feishu` 包内没有标准化的层定义。这导致：

- 层语义（L0=完全隔离、L1=白名单、L2=受限路由、L3=默认放行）分散在部署配置和闭包中，不可复用
- 权限矩阵没有默认 fail-closed 行为——完全依赖外部注入
- `Allowed()` 返回值 `(bool, string)` 缺少结构化审计信息（层、权限级别、mention 状态）
- 不同部署可能对同一层语义理解不一致

v0.8 将层语义提升为 `pkg/feishu` 内一等公民。

## Design

### 1. Layer 类型

```go
type Layer string

const (
    LayerL0 Layer = "L0"  // 完全隔离
    LayerL1 Layer = "L1"  // 白名单受限
    LayerL2 Layer = "L2"  // 受限路由
    LayerL3 Layer = "L3"  // 默认放行
)
```

### 2. LayerBehavior — 每层默认行为

| 层 | 语义 | 默认权限 | Mention 提升 | 审计 |
|---|------|---------|-------------|------|
| L0 | 完全隔离——仅记录不路由，bootstrap/supervisor only | Forbidden | ❌ | ✅ |
| L1 | 白名单受限——白名单 agent + 审计 | Forbidden | ✅ | ✅ |
| L2 | 受限路由——受限路由 + 审计 | Forbidden | ✅ | ✅ |
| L3 | 默认放行——公开渠道/对外群 | Allowed | ✅ | ❌ |

**Fail-Closed 矩阵**: 所有层默认拒绝，L3 是唯一默认放行的层。未注册的 LU、未匹配的层一律返回 `Forbidden`。

### 3. Decision — 结构化审计

```go
type Decision struct {
    Allowed    bool
    Reason     string
    Layer      Layer
    LU         string
    Permission Permission
    IsMention  bool
}
```

上游（nexusd）可通过 `Decision` 的完整字段做结构化日志/审计留存。

### 4. LayerPolicy — 标准化策略

`LayerPolicy` 封装层行为、LU 权限覆盖、fail-closed 默认值：

```go
type LayerPolicy struct {
    registry  *Registry
    layerOf   func(chatID string) Layer
    behaviors map[Layer]LayerBehavior       // default: DefaultLayerBehaviors()
    luPerms   map[string]map[Layer]Permission // optional per-LU overrides
    defaultLayer Layer                       // default: LayerL3
}

func NewLayerPolicy(registry *Registry, layerOf func(chatID string) Layer, opts ...LayerOption) *LayerPolicy
```

- `Check(lu, chatID string, isMention bool) Decision` — 主决策方法，返回完整审计
- `Allowed(lu, chatID string, isMention bool) (bool, string)` — 向后兼容

### 5. 决策流程

```
Check(lu, chatID, isMention):
1. layer = layerOf(chatID) 或 defaultLayer
2. behavior = behaviors[layer] 或 fail-closed 默认
3. 查找 LU 覆盖: luPerms[lu][layer]
4. 未覆盖 → behavior.DefaultPermission
5. Mention 提升检查: 若 behavior.AllowMentionOverride && isMention → 可升级
6. 返回 Decision{Allowed, Reason, Layer, LU, Permission, IsMention}
```

### 6. 向后兼容

- `NewPolicy(reg, layerOf, perm)` — 保留原签名，内部适配层类型
- `Policy.Allowed(lu, chatID, isMention) (bool, string)` — 签名不变
- 新增 `Policy.Check(lu, chatID, isMention) Decision` — 结构化决策
- `cmd/nexusd/main.go` 从闭包注入迁移到 `NewLayerPolicy` + `WithLUPermissions` 选项

## Files Changed

| File | Change |
|------|--------|
| `pkg/feishu/layer.go` | **新增** — Layer, LayerBehavior, Decision, LayerPolicy, 默认行为 |
| `pkg/feishu/layer_test.go` | **新增** — 表驱动矩阵测试 |
| `pkg/feishu/feishu.go` | **修改** — 增加 Check(), 适配 Layer 类型 |
| `cmd/nexusd/main.go` | **修改** — 使用 LayerPolicy 替代闭包 |
| `docs/governance-layers-v0.8.md` | **新增** — 本文档 |
| `README.md` | **修改** — v0.7 → v0.8, 测试数更新 |
