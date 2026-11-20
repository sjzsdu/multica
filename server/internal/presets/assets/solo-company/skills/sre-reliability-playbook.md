---
name: sre-reliability-playbook
description: "Use when designing deployment strategies, setting up monitoring/alerting, managing CI/CD, planning incident response, or making infrastructure decisions. Triggers: 部署, 监控, 告警, CI/CD, 灾备, incident, oncall, 可观测性, SRE."
---

# SRE 可靠性与运维

## SLO 先于指标

**不要"监控机器指标"**，要"监控对用户重要的东西"：

| 用户视角 | 指标 | SLO 示例 |
|---|---|---|
| 任务能跑起来吗 | 任务启动成功率 | 99.5% |
| 任务能跑完吗 | 任务完成率（非 token 失效） | 98% |
| 任务跑多久 | P95 任务时长 | < 30 min |
| API 响应 | P95 / P99 | P99 < 2s |

## 部署策略

- **零停机**：滚动部署 + health check；新旧版本兼容的 API schema
- **回滚优先**：任何有风险的变更，提前准备回滚脚本，不是"等出问题再想"
- **灰度**：新功能先开 flag，逐步放量；不要一次性全量
- **并发索引**：必须用 CONCURRENTLY 单独迁移文件，且在 migrate cleanups 里注册

## 告警设计

### 好告警：
- 有明确的 oncall 责任人
- 有 runbook（"收到此告警，第一步做什么"）
- 有升级路径（15 min 没人接 → 升级到谁）
- 只告警需要人介入的事

### 坏告警：
- "CPU 90%"（除非 CPU 90% 真导致 SLO 破坏）
- 重复告警风暴（同一根因触发 5 条告警）
- 没人管告警（等于没告警）

## 可观测性三件套

- **Logs**：结构化（JSON）、带 trace_id、分级；生产环境不要打 debug
- **Metrics**：Gauge / Counter / Histogram；业务指标（任务成功率）> 机器指标（CPU）
- **Traces**：关键路径贯穿 API → DB → external call；超时和错误有 span status

## 常见坑

- **token 失效竞态**：task token 按代次吊销，不要按 task 全量吊销；软删除（revoked_at 标记）
- **jcode session 复用**：workdir 临时凭证文件兜底，不要依赖环境变量
- **daemon 退出**：检查点续跑粒度够不够？Agent B 能不能从 Agent A 的断点继续？
- **DB migration**：每次 migration 都要有 DOWN；并发索引要在 cleanups 注册；INSERT 显式 COALESCE
