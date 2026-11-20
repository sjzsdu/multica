---
name: gh-account-routing
description: 在执行任何 gh 命令前，根据当前仓库的 remote origin URL 自动选择并切换到正确的 GitHub 账号。remote 包含 github_sjzsdu 时切到 sjzsdu，其余情况切回默认账号。当 Agent 需要在多账号环境下使用 gh 操作仓库、PR、Issue 或 API 时使用。
---

# gh-account-routing

## 目的

`gh` 的活跃账号是全局状态，不会根据仓库自动变化。在多账号机器上，Agent 如果直接用当前活跃账号操作，可能在错误的身份下创建 PR、push 或调用 API[reference:3]。本 Skill 要求 Agent 在**每一次** `gh` 调用前，显式地把账号路由到与当前 remote 匹配的那个。

## 核心规则

| 条件 | 目标账号 |
|---|---|
| `git remote get-url origin` 的结果包含字符串 `github_sjzsdu` | `sjzsdu` |
| 其余所有情况（包括不在 git 仓库中、没有 origin） | 你的**默认账号**（下文用 `DEFAULT_ACCOUNT` 表示，Agent 需向用户确认一次并记住） |

## 强制执行流程

Agent 在执行任何 `gh` 命令（`gh auth status` 等只读诊断命令除外）之前，**必须**按顺序完成以下三步。

### 第一步：读取 remote URL 和当前活跃账号

```bash
REMOTE_URL=$(git remote get-url origin 2>/dev/null)
ACTIVE_ACCOUNT=$(gh auth status --active --json hosts \
  -q '.hosts["github.com"][0].login' 2>/dev/null)
