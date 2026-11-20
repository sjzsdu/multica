---
name: pragmatic-implementation
description: "Use when implementing features, writing code, choosing implementation patterns, or deciding when to refactor vs ship. Triggers: 实现, 编码, implementation, 重构, 性能优化, 数据模型设计."
---

# 务实的实现指南

## 核心原则

**先跑通，再完善。不要为"未来可能需要"设计，为"现在明确需要"设计。**

## 编码检查清单

### 1. 可读性 > 聪明
- 命名说清楚意图，不要用缩写除非是行业标准（ID、API、URL）
- 函数单一职责，一个函数只做一件事
- 注释解释"为什么"，不解释"做什么"（代码已经在做什么）

### 2. 错误处理
- 不要 silent fail：catch 后 log 了吗？log 级别对吗？
- 不要吞错误：`catch (e) {}` 是 bug，不是 feature
- 错误信息要让调用方能恢复，不是只能看

### 3. 数据模型
- 列有定义 DEFAULT 时，INSERT 必须显式用 COALESCE 兜底（sqlc 会透传 NULL 覆盖默认值）
- 不建外键约束，在应用代码里校验和清理
- 软删除（revoked_at IS NULL）查询必须加条件

### 4. API 边界
- JSON 必须经过 zod schema + parseWithFallback，不要 `as T` 直接转
- 给可选字段加默认值，给未知枚举加 fallback
- Preserve response compatibility：已安装的客户端可能跟新后端

### 5. 并发
- 不要假设 token 永远有效：每次用之前校验
- 不要假设同一 task 只有一个 agent 实例：代次绑定（task_id + dispatched_at）
- 关键锁要短：不要在持锁期间做外部调用

## 什么时候重构 vs 什么时候直接加

**直接加** 当：
- 功能简单，复杂度可控
- 没有重复实现第二次之前

**重构** 当：
- 同样的逻辑要第三次复制粘贴
- 新功能强制触及 3+ 已有模块
- 性能瓶颈已通过 profiling 确认，不是猜测

## 提交检查清单

- [ ] 代码干净可编译（typecheck + lint + test 通过）
- [ ] 没有硬编码的 URL / token / secret
- [ ] 没有 TODO / FIXME / HACK 留在生产代码（除非有 issue 跟踪）
- [ ] 新增测试覆盖了 happy path + 至少 1 条失败路径
- [ ] 数据库 migration 有 UP + DOWN（如果有的话）
- [ ] 并发索引在 cleanups 里注册了（如果有的话）
