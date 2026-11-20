---
name: data-driven-decision-framework
description: "Use when defining metrics, designing experiments, analyzing user behavior, building dashboards, or making data-backed product decisions. Triggers: 指标体系, 数据分析, A/B 测试, 用户行为, 增长分析, data-driven, 指标定义."
---

# 数据驱动决策框架

## 指标设计

### 好指标 vs 坏指标

| 好指标（signal metrics） | 坏指标（vanity metrics） |
|---|---|
| 任务完成率（end-to-end） | 活跃用户数（不做事的不算） |
| P95 任务时长 | 总对话条数 |
| 用户重复使用同功能 | 总注册数 |
| Agent 产出采纳率（人类用了多少） | Agent 总调用次数 |
| 有付费意愿的用户比例 | 页面浏览量 |

### 指标金字塔

```
L1: 北极星指标（一个）
    ↓
L2: 驱动指标（3-5 个，驱动北极星）
    ↓
L3: 拆解指标（每个驱动指标下 2-3 个）
    ↓
L4: 诊断指标（出问题时定位根因）
```

**示例**：
- L1: 任务完成率（用户给目标 → Agent 交付可使用成果的比例）
- L2: Agent 启动成功率 / Agent 运行时长 P95 / Agent 产出采纳率
- L3: 启动成功率 → token 校验通过率 + runtime 连接成功率
- L4: 细粒度错误码分布（invalid_token vs task_token_revoked vs timeout）

## A/B 测试设计

### 什么时候用 A/B 测试

- 有明确的假设（"把入口从聊天框改成面板，任务完成率会上升 5%"）
- 有足够的样本量（每个 variant 至少 100 个有效用户）
- 有 primary metric 和 guardrail metric（不要全看 primary）

### 不要做 A/B 测试

- 样本量太小（5 个用户分 2 组 = 噪声）
- 没有明确假设（"试试哪个好看" = 凭感觉就行）
- 同一个用户多次切换 variant（混淆）

### 输出格式

```
## 实验: [一句话]
### 假设
### Primary Metric: [哪个指标提升]
### Guardrail Metrics: [哪个指标不能掉]
### 样本量估算
### 分流方案
### 预期时长
### 决策标准：如果 primary 提升 X% 且 guardrail 不低于 Y%，选 A
```

## Agent-Native 数据视角

### Agent 特有指标

- **任务完成率**：用户给目标 → Agent 交付可使用成果的比例（不是"跑完了就算"）
- **Agent 产出采纳率**：人类实际使用 Agent 产出的比例（读了 =/= 用了）
- **Agent 自主决策率**：Agent 不需要人类审批直接做完的步骤比例
- **交接开销**：Agent A → Agent B 的续跑成功率和耗时
- **Token 生命周期**：token 平均存活时长、吊销原因分布

### 避免

- 把"Agent 跑了多久"当成价值（AI slop 也是产出）
- 把"总调用次数"当成增长（用户被迫重试也会增加）
- 忽略人类最终有没有用 Agent 产出
