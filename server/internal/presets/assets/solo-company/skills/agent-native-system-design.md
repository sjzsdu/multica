---
name: agent-native-system-design
description: "Use when designing systems that AI agents will call, resume, or collaborate with — agent tool contracts, checkpoint/resume design, human-agent bandwidth allocation, agent observability, multi-agent orchestration. Triggers: agent 架构, 工具契约, checkpoint, 多 agent, agent resume, LangGraph, agent 可观测性."
---

# Agent-Native 系统设计

当系统的主要调用者是 AI Agent 而非人类 UI 时，架构设计要回答的问题完全不同。这个 skill 把原则落到可执行的设计决策。

## 一、Agent 工具契约设计

**目标：让 Agent 一次调用就能对，错了能自己恢复。**

### 参数语义

| 问题 | 设计决策 |
|---|---|
| 参数含义能猜到吗 | 参数名必须是业务语言（`workspace_slug` 而非 `ws_id_or_slug`） |
| 传错了知道吗 | 参数验证错误要返回"哪个字段、期望什么、实际收到什么" |
| 必填/可选清晰吗 | schema 里显式标 required，可选参数给默认值 |
| 范围/格式明确吗 | 枚举值要列出全部合法值；ID 要说明是 UUID 还是 slug |

### 错误码设计（关键）

**Agent 能根据错误码决定下一步动作，这是和人类 UI 最大的区别。**

| 错误类别 | HTTP | Agent 应该做的 |
|---|---|---|
| 参数错 | 400 | 修正参数重试，**不要原样重试** |
| 凭证失效 | 401 + `invalid_token` | 停止，上报（无法自救） |
| 凭证被吊销 | 401 + `task_token_revoked` | 停止，上报（任务已被取消） |
| 权限不足 | 403 | 停止，上报 |
| 资源不存在 | 404 | 检查 ID 是否正确，不要盲目重试 |
| 冲突 | 409 | 读取最新状态后重新决策 |
| 限流 | 429 + `Retry-After` | 按提示退避重试 |
| 服务端错 | 5xx | 指数退避重试，最多 3 次 |

**铁律**：**同一个错误码对应同一个恢复动作**。如果 401 有时候要重试有时候要上报，Agent 就无法决策。

### 超时与重试

- 每个工具调用必须有明确超时（默认 30s，长任务显式声明）
- 重试必须**幂等**：用 idempotency key 或操作天然幂等
- 写操作重试前要确认"上次到底成没成"（先查询再决定）

### 返回结构

```json
{
  "ok": true,
  "data": { ... },
  "next_actions": ["agent 可以接着做什么的提示"]
}
```

错误时：
```json
{
  "ok": false,
  "error": {
    "code": "task_token_revoked",
    "message": "人类可读",
    "recovery": "stop_and_report",
    "retryable": false
  }
}
```

**`recovery` 字段是给 Agent 看的行动指令**，比 message 更重要。

## 二、状态机与检查点（Checkpoint/Resume）

**目标：Agent A 挂了，Agent B 能接着跑，不重复、不丢失。**

### 状态存哪里

| 状态类型 | 存储位置 | 理由 |
|---|---|---|
| 业务实体状态 | 数据库（权威） | 多 Agent 共享、可审计 |
| 任务执行进度 | 数据库 checkpoint 表 | 可 resume |
| Agent 中间推理 | 内存 + 关键节点落盘 | 量大、生命周期短 |
| 会话上下文 | 文件 / KV | 便于恢复 |

### Checkpoint 粒度

**太粗**：Agent 挂了要重做一大段。
**太细**：写 checkpoint 本身成为瓶颈。

**经验法则**：每个"不可逆操作"前后各存一个 checkpoint。
- 发 API 请求前
- 写数据库前
- 调外部付费服务前
- 产生副作用前

### Resume 协议

```
1. Agent B 启动 → 读最新 checkpoint
2. 校验 checkpoint 完整性（task_id + dispatched_at + 版本号）
3. 从 checkpoint 的 next_action 继续
4. 如果 checkpoint 指向的操作已执行过（幂等键冲突），跳过
```

### 什么时候用 LangGraph / 工作流引擎

| 场景 | 方案 |
|---|---|
| 简单线性流程 | 自己写状态表就够 |
| 有分支/循环/人工审批 | LangGraph / 工作流引擎 |
| 多 Agent 协作 | 显式状态机 + 消息传递 |
| 长任务（小时级） | 必须 checkpoint，且要能断点续跑 |

**不要为用而用**：90% 的场景一张 `task_checkpoints` 表 + 状态字段就够。

## 三、人机协作带宽分配

**目标：人类的注意力是稀缺资源，只在高价值决策上出现。**

### 必须人批（Human-in-the-loop）

- 不可逆操作（删数据、发公告、转账）
- 花钱的操作（调付费 API、升级订阅）
- 对外影响（发邮件、发推、改公开页面）
- 权限变更
- 超出预设边界的操作

### 可以让 Agent 自主

- 读取、分析、整理
- 可逆的草稿（写 PRD、写代码但不合并）
- 有明确回滚方案的操作
- 在预设预算/权限内的操作

### 审批点设计反模式

| 反模式 | 问题 |
|---|---|
| 每个小步骤都要批 | 人变成瓶颈，失去 Agent 价值 |
| 只在最后批 | 前面走错方向浪费大量 token |
| 不给上下文就让人批 | 人看不懂在批什么 |
| 审批项堆一堆 | 人会全点通过，等于没批 |

**正确做法**：在"方向性决策"和"不可逆操作"两处设审批点，每次审批给 3 行以内的上下文 + 明确选项。

## 四、Agent 可观测性

**目标：Agent 卡住了、做错了、花多了，能立刻定位。**

### 必记录的字段

每次 Agent 运行：

```
run_id, agent_id, task_id, workspace_id,
started_at, ended_at, status,
model, input_tokens, output_tokens, cost_usd,
tool_calls: [{tool, args_hash, latency_ms, ok, error_code}],
checkpoint_ids: [...]
```

### 关键指标

- **任务完成率**（不是"跑完了"，是"产出可用"）
- **P95 任务时长**
- **token 成本 / 任务**
- **工具调用失败率**（按 error_code 分桶）
- **checkpoint 恢复成功率**
- **人工介入率**（越低越好，但不为 0）

### Tracing

每个 task 一个 trace_id，贯穿：
- API 调用
- 工具调用
- 数据库写入
- Agent 间交接

**没有 trace_id，多 Agent 系统出问题根本查不动。**

## 五、多 Agent 编排

### 什么时候用什么

| 场景 | 方案 |
|---|---|
| 单 Agent 顺序执行 | 直接函数调用 |
| 固定流水线（设计→实现→复核） | 显式状态机 + 队列 |
| 动态路由（根据内容派给不同专家） | Leader 派发模式 |
| 并行 + 汇总 | fan-out/fan-in + 队列 |
| 需要人工审批的中断点 | 状态机 + 审批表 |

### 交接契约

Agent A → Agent B 必须传递：

```
1. 完成了什么（结论）
2. 遇到了什么决策（含备选）
3. 遗留什么待办
4. 怎么 resume（checkpoint_id / 状态位置）
5. 证据（日志、文件、commit hash）
```

**缺任何一项，下游 Agent 就要重新摸索，浪费 token。**

### 避免的架构

- **共享黑板**（多 Agent 读写同一份可变状态）→ 竞态、难调试
- **隐式依赖**（A 假设 B 会做某事）→ 断裂
- **循环调用无上限** → token 爆炸

## 六、Multica 项目特化参考

在 Multica 仓库做 Agent 相关架构时：

- task token 按 `task_id + dispatched_at` 代次绑定，吊销要按代次精确
- daemon 启动 agent 前必须调 `/task-token/whoami` 校验归属
- 凭证兜底用 workdir 下的临时凭证文件（jcode session 复用会冻结环境变量）
- 所有 token 操作记 `task_id, agent_id, workspace_id, dispatched_at, token_id, token_fingerprint`
- 并发索引迁移必须在 `migrate/main.go` 的 `concurrentIndexCleanups` 注册

## 输出要求

设计 Agent 相关架构时，必须显式回答：

1. Agent 调错参数/工具失败时，错误码够不够它自己恢复？
2. Agent 中途挂了，下一个 Agent 从哪里接着跑？checkpoint 在哪？
3. 哪些决策必须人批？审批点在哪？
4. 出问题时，用什么字段定位到具体那一次运行？
5. 这个设计会不会让 token 成本失控？
