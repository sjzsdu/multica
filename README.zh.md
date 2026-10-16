<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.svg">
  <img alt="Multica" src="docs/assets/logo-light.svg" width="50">
</picture>

# Multica — v2.0 更新说明

**智能体，也在看板上。**

本页面记录 `d1df235d2` 提交引入的所有变更。原始项目文档已保留在 [README.original.zh.md](README.original.zh.md)。

[![CI](https://github.com/multica-ai/multica/actions/workflows/ci.yml/badge.svg)](https://github.com/multica-ai/multica/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/multica-ai/multica?style=flat)](https://github.com/multica-ai/multica/releases)

**[English](README.md) | [简体中文](README.zh.md)**

</div>

---

## 概览

本次更新（`d1df235d2`）包含 **198 个文件变更**，涵盖以下核心内容：

| 模块 | 变更概要 |
| --- | --- |
| [OpenClaude 提供方](#openclaude-提供方) | 新增第 27 个 AI 编码智能体运行时 |
| [本地一体化 Web 模式](#本地一体化-web-模式) | 一键启动全栈本地开发环境 |
| [Squad 工作流策略](#squad-工作流策略) | 小队级自动化协调与审批流程 |
| [架构图文档](#架构图文档) | 新增 5 套可交互架构可视化 |
| [文档与本地化](#文档与本地化) | 多语言项目资源文档重写 |

---

## OpenClaude 提供方

本次更新将 [OpenClaude](https://github.com/openclaude/openclaude) 作为第 27 个受支持的 AI 编码智能体 CLI 集成到 Multica 中。

### 支持的功能

- **模型发现** — 通过 `openclaude models` 命令动态获取可用模型列表
- **MCP 配置** — 支持通过 `mcp_config` 字段配置 Model Context Protocol
- **运行时标识** — 在守护进程、前端 UI 和 CLI 中正确显示 OpenClaude 品牌名

### 相关变更

```
server/pkg/agent/openclaude_models.go          # 模型发现逻辑
server/pkg/agent/openclaude_models_test.go     # 模型发现测试
server/pkg/agent/openclaude_runtime_test.go    # 运行时集成测试
server/internal/daemon/agents_probe_openclaude_test.go  # 守护进程探测测试
packages/core/runtimes/display.ts              # 前端显示名称
packages/core/agents/mcp-support.ts            # MCP 配置支持
```

### 使用方式

安装 OpenClaude CLI 并完成认证后，Multica 会自动检测并将其列为可用运行时。在创建智能体时，从 Provider 下拉菜单中选择 **OpenClaude** 即可。

---

## 本地一体化 Web 模式

新增 `multica web` 命令，支持从代码仓库一键启动完整的本地开发环境，无需 Docker 或外部服务。

### 快速开始

```bash
make install        # 构建 Next.js 前端 + Go CLI
multica web         # 启动全栈服务
```

### 服务组成

`multica web` 启动以下服务：

| 服务 | 地址 | 说明 |
| --- | --- | --- |
| Web UI | `http://127.0.0.1:9393` | 嵌入式 Next.js 前端 |
| Go 后端 | 同上 | Chi 路由 + WebSocket |
| PostgreSQL | 本地 | 自动设置 + 迁移 |
| Agent 守护进程 | 本地 | 自动拉起并注册运行时 |

### 守护进程模式

```bash
multica web --daemon   # 后台启动所有服务
multica web status     # 查看运行状态
multica web stop       # 停止所有服务
multica web restart    # 用新编译的二进制重启
```

### 环境变量

```env
MULTICA_POSTGRES_MODE=local          # 使用本地 PostgreSQL
MULTICA_LOCAL_AUTH=true              # 启用本地登录
MULTICA_LOCAL_AUTH_EMAIL=local@multica.dev
```

### 前置条件

- Node.js（前端为 Next.js standalone server）
- Go + pnpm（从源码构建时）
- PostgreSQL + pgvector
- 至少一个已认证的智能体 CLI

---

## Squad 工作流策略

为 Squad（小队）引入全新的工作流策略系统，支持多阶段自动化交付。

### 核心概念

| 概念 | 说明 |
| --- | --- |
| `workflow.mode` | 工作流模式：`manual`（默认）、`staged`、`delivery` |
| `child_completion_signal` | 子任务完成信号：`terminal_only` 或 `review_ready` |
| `max_active_children` | 同时活跃的子任务上限 |
| `auto_promote_stage` | 自动提升下一阶段的积压子任务 |
| `auto_finalize_parent` | 最终阶段完成后自动将父任务移至审核中 |
| `require_tests` / `require-review` | 强制要求测试通过 / 人工审核 |

### 配置命令

```bash
multica squad update <squad-id> \
  --workflow-mode staged \
  --child-completion-signal review_ready \
  --max-active-children 2 \
  --auto-promote-stage \
  --auto-finalize-parent \
  --require-tests \
  --require-review
```

### 数据库迁移

新增 9 个迁移文件（`433` – `441`），创建以下表和索引：

| 表 | 用途 |
| --- | --- |
| `squad_workflow_policy` | 小队工作流策略配置 |
| `squad_workflow_run` | 工作流运行实例 |
| `squad_workflow_approval` | 审批记录 |

### API 端点

```
GET  /api/issues/<parent-id>/workflow-runs
POST /api/issues/<parent-id>/workflow-runs/<run-id>/approvals
```

---

## 架构图文档

新增 5 套可交互的架构可视化文档，位于 `docs/` 目录：

| 文件 | 内容 |
| --- | --- |
| `multica-architecture` | 整体系统架构 |
| `multica-agent-task.lifecycle` | 智能体任务生命周期 |
| `multica-agent-task.workflow` | 智能体任务工作流 |
| `multica-issue-agent.dataflow` | Issue ↔ 智能体数据流 |
| `multica-issue-update.sequence` | Issue 更新时序图 |

每套文档包含：
- 可交互的 HTML 页面（支持暗色/亮色主题）
- JSON 数据源文件
- 1440×900 和 2048×1320 两种分辨率的预览图（暗色/亮色各一张）

---

## 文档与本地化

### 项目资源文档重写

重写了多语言项目资源文档（`project-resources`），改进内容包括：

- 明确区分 `github_repo` 与 `local_directory` 的使用场景
- 新增本地目录执行模式说明（`in_place` vs `worktree`）
- 新增 SSH 身份配置说明
- 新增 GitHub CLI 账户配置
- 全面更新韩语、日语、中文翻译

### 提供方文档

在所有语言版本的提供方文档中添加了 OpenClaude 条目。

---

## 其他变更

### 后端

- `server/internal/handler/issue.go` — 新增 `ErrActiveChildrenLimitExceeded` 错误处理
- `server/pkg/taskfailure/failure_test.go` — 新增失败原因字符串测试
- `server/internal/service/builtin_skills_test.go` — 新增 autopilots、runtimes-and-repos、projects-and-resources 技能测试

### 前端

- `packages/views/squads/components/squad-detail-page.tsx` — 新增工作流设置面板
- `packages/views/issues/components/workflow-panel.tsx` — 新增工作流审批 UI
- `packages/core/issues/mutations.ts` — 新增 `useWorkflowApproval` mutation
- `packages/views/projects/components/github-ssh-alias.ts` — 新增 SSH 别名解析

### 基础设施

- `.env.example` — 新增 `MULTICA_LOCAL_AUTH` 相关变量
- `Makefile` — 新增 `install` 和 `web` 相关构建目标
- `scripts/ensure-postgres.sh` — 重写 PostgreSQL 设置脚本

---

## 文件统计

```
198 files changed, 78246 insertions(+), 296 deletions(-)
```

主要新增目录：
- `server/internal/squadworkflow/` — 工作流协调器
- `server/migrations/433–441/` — 数据库迁移
- `docs/*.html` — 架构可视化文档
- `server/pkg/agent/openclaude_*` — OpenClaude 集成

---

> 📖 完整项目文档请参阅 [README.original.zh.md](README.original.zh.md)
