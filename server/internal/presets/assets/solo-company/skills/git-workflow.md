---
name: git-workflow
description: "Use when working with git — branching strategy, commit conventions, rebase vs merge, PR hygiene. Triggers: git, branch, commit, rebase, merge, PR, pull request, 分支, 提交."
---

# Git 工作流

## 分支策略

### 主分支
- `main` — 始终可部署，保护分支，不直接 push
- 所有改动走 feature 分支 + PR

### 分支命名

```
<type>/<short-description>

type:
  feat/     新功能
  fix/      bug 修复
  refactor/ 重构（不改行为）
  chore/    构建/工具/依赖
  docs/     文档
```

示例：`feat/task-token-revocation`、`fix/daemon-jcode-session`

### 生命周期
1. 从 `main` 切出 feature 分支
2. 开发 + 提交
3. 推到远端
4. 开 PR，CI 通过后合并
5. 删除 feature 分支（本地 + 远端）

## Commit 规范

### Conventional Commits

```
<type>(<scope>): <subject>

<body>  # 可选，说明为什么

<footer>  # 可选，breaking change 或 issue 引用
```

**type**：`feat` / `fix` / `refactor` / `chore` / `docs` / `test` / `perf` / `ci`

**subject**：祈使句、小写开头、不加句号、不超过 72 字符

### 示例

```
feat(auth): add task token revocation by dispatch generation

Tokens are now revoked per task_id + dispatched_at instead of bulk
per task, preventing cross-generation 401s.

Fixes #1234
```

### 好 commit vs 坏 commit

| 好 | 坏 |
|---|---|
| 一个 commit 一个逻辑单元 | 一个 commit 改 5 个不相关的东西 |
| commit message 说"为什么" | commit message 说"改了 xxx"（git diff 能看到） |
| 可独立编译通过 | WIP 提交推到远端 |
| 粒度适中（几十到几百行） | 一个 commit 改 2000 行 |

## Rebase vs Merge

### 什么时候用 rebase
- 把 feature 分支变基到最新 main 上，保持线性历史
- 整理本地未 push 的 commit（squash、reword）

### 什么时候用 merge
- 合并 PR 到 main（保留合并记录，便于回滚）
- 合并别人的分支

### 铁律
- **不要 rebase 已经 push 到远端且别人在用的分支**（会改写历史，别人崩溃）
- rebase 后 push 需要 `--force-with-lease`（不是 `--force`）
- 合并到 main 用 merge 或 squash merge，不用 rebase merge

## PR 规范

### PR 描述必含

```
## 改了什么
（一句话 + 关键改动列表）

## 为什么
（解决什么问题，关联 issue）

## 怎么验证
（测试命令、手动验证步骤）

## 风险
（有没有 breaking change、需要数据迁移、影响范围）
```

### PR 大小
- 理想：< 400 行
- 可接受：< 800 行
- 超过 1000 行：拆成多个 PR

### Review 关注点
- 设计原则（SOLID、DRY、解耦）
- 边界条件和错误处理
- 测试覆盖（happy + 至少一条失败路径）
- 有没有硬编码的 secret/token
- 有没有遗留 TODO/FIXME

## 冲突处理

1. `git pull --rebase` 而不是 `git pull`（避免无意义的 merge commit）
2. 冲突时一个文件一个文件解
3. 解完 `git add` + `git rebase --continue`
4. 不确定时 `git rebase --abort` 重来，不要硬来

## 常用命令速查

```bash
git checkout -b feat/xxx          # 建新分支
git add -A && git commit -m "..."  # 提交
git pull --rebase origin main      # 同步 main
git rebase -i HEAD~5               # 整理最近 5 个 commit
git push --force-with-lease        # rebase 后推送
git log --oneline -20              # 看历史
git diff main...HEAD               # 看当前分支改了什么
```
