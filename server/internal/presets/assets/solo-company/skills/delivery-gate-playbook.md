---
name: delivery-gate-playbook
description: 交付推进 SOP：巡检 in_review / in_progress 的 PR 门禁，送包公复核，按判定返工或合并；对 todo / backlog 做就绪分诊，能开工就派 agent，不能开工就标 blocked。
---

# 交付推进 Playbook

## 目标

把 issue 推进到 `done`，并让排队中的 issue 真正进入执行。你只做编排：发现 PR、送审、按判定分流、合并、改状态、做就绪分诊。你不写代码，不替包公做质量判断，不替产品做需求设计。

## 低成本巡检协议（第一步）

每轮开始先只做五个哨兵查询，每个最多 1 条、只取 id：

```bash
multica issue list --status in_review --limit 1 --output json --fields id
multica issue list --status in_progress --limit 1 --output json --fields id
multica issue list --status todo --limit 1 --output json --fields id
multica issue list --status backlog --limit 1 --output json --fields id
multica issue list --metadata delivery_state=fixing --limit 1 --output json --fields id
```

全部为 0 → **立即结束本轮**，只回一行「无目标 issue」，不再执行任何其他命令、不输出统计表。

有目标时才继续，并且遵守：

- 只用 `--fields` 取需要的字段；不要把完整 JSON 拉进上下文。
- 禁止无过滤的 `multica issue list`；禁止统计 done/backlog/cancelled 数量。
- 每个类别单轮最多处理 2 个 issue，其余留到下一轮。
- 评论只取最后 5 条，并用 `jq` 截断到 400 字；不要打印原始 JSON。
- `multica issue get` 只在需要描述/验收标准时调用；list 里已有的字段不要重复取。
- 已分诊且 `issue.updated_at` 没变的 todo/backlog issue 直接跳过，不重复读描述。
- 无动作时最终输出不超过一行；不要写巡检报告或表格。

## 防重复与防循环（最高优先级）

同一个 issue 的同一个动作只做一次。每次动作前先读 metadata 并检查：

1. `delivery_state=escalated` → 不再评论、不再派工、不再改状态。
   - 只有满足任一条件才恢复：PR head sha 变化、出现新的包公判定、成员在 `delivery_escalated_at` 之后发表评论。
   - 恢复时先删除 `delivery_state`，再按新情况重新判断。
2. `delivery_next_action_at` 未到 → 静默跳过，不评论。
3. 同一动作重复：`delivery_last_action` 与本次动作相同，且 sha、判定、状态都没变化 → 跳过。
4. 同一 issue 任意 30 分钟内最多发 1 条自动化评论；只有「处理新判定」可以突破这个间隔。
5. 任何动作后写 `delivery_last_action_at`、`delivery_last_action`；需要冷却的动作同时写 `delivery_next_action_at`。
6. 没有新证据（新 sha、新包公评论、人工评论、CI 结果变化）时，本 tick 只读不写。
7. 升级是一次性的：立即写 `delivery_state=escalated`、`delivery_escalated_at`，之后靠恢复条件决定是否重新纳入。
8. 同一 PR 同一 head sha 只送审一次；多个 issue 共享 PR 时，等判定/合并后统一对账，不重复送审。

## 每次运行的固定流程

1. 执行五个哨兵查询；全部为空直接结束。
2. 按顺序处理非空类别，每个类别最多 2 条：
   - `in_review`：交付门禁（送审、按判定返工/合并/完成）。
   - `in_progress`：有 PR 的按最终门禁处理；PR 已合并的直接对账标完成；无 PR 的按停滞规则处理。
   - `todo`：就绪分诊，能开工就启动，否则 blocked。
   - `backlog`：就绪分诊，能开工就派 agent 并启动，否则 blocked。
   - `delivery_state=fixing`：兜底检查停滞和 sha 变化。
3. 每个 issue 只推进到「派发下一步」为止，不阻塞等待。
4. 处理顺序固定，先门禁、后分诊；本 tick 预算不够时留给下一轮。
5. 同一 issue 一个 tick 只处理一次；已在 `in_review` 或 `in_progress` 分支处理过的，不再进 `fixing` 兜底。

## metadata 状态机（键名固定）

| key | 取值 | 含义 |
|---|---|---|
| `delivery_state` | `reviewing` / `fixing` / `awaiting_pr` / `escalated` / `merged` | 当前阶段 |
| `delivery_sha` | git sha | 已送审的 PR head sha |
| `delivery_pr` | PR URL | 当前门禁的 PR |
| `delivery_requested_at` | RFC3339 | 送审时间 |
| `delivery_request_comment` | comment id | 送审评论 ID，用于定位之后的包公判定 |
| `delivery_rounds` | 整数 | 已打回次数（FAIL 或 CI 红都算），达到 3 升级 |
| `delivery_verdict` | `PASS` / `FAIL` | 最近一次包公判定 |
| `delivery_start_attempts` | 整数 | 实际启动次数，达到 2 次仍回到 `todo`/`backlog` 则升级 |
| `delivery_triage_at` | RFC3339 | 分诊完成时间；`>= issue.updated_at` 表示分诊后未变化 |
| `delivery_triage_verdict` | `ready` / `ready_pending` / `blocked` / `ready_manual` | 分诊结论 |
| `delivery_last_action` | 字符串 | 最近一次自动化动作名 |
| `delivery_last_action_at` | RFC3339 | 最近一次动作时间，用于 30 分钟评论间隔 |
| `delivery_next_action_at` | RFC3339 | 冷却结束时间，未到则静默 |
| `delivery_escalated_at` | RFC3339 | 升级时间，用于判断人工是否已介入 |

写入示例：

```bash
multica issue metadata set <id> --key delivery_state --value reviewing --type string
multica issue metadata set <id> --key delivery_rounds --value 1 --type number
```

## 发现 PR

按顺序尝试，拿到第一个可信结果即可：

1. `multica issue pull-requests <id> --output json`，取 `pull_requests` 数组。
2. 从评论中提取 PR URL：

```bash
multica issue comment list <id> --output json \
  | jq -r '.[].content' \
  | grep -oE 'https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[0-9]+' | tail -1
```

3. 按项目资源仓库搜索：

```bash
multica project resource list <project-id> --output json
gh pr list --repo <owner/repo> --state all --search "<identifier>" \
  --json number,url,state,headRefOid,isDraft,title
```

只采纳标题、分支名或正文明确包含该 issue 编号的 PR。多个 issue 指向同一 PR 时，按同一 PR 去重。

## PR 门禁数据

```bash
gh pr view <pr-url> --json number,url,state,isDraft,mergeable,mergeStateStatus,headRefName,headRefOid,baseRefName,statusCheckRollup
```

判定：

- `MERGED`：所有关联 PR 都已合并 → `multica issue status <id> done --no-start`，`delivery_state=merged`，结束。
- `CLOSED` 且未合并：不合并、不标 done；按「打回」处理并说明交付被放弃。
- `isDraft=true`：请负责人改为 ready for review，不要送审。
- `OPEN`：继续。

CI 绿色必须同时满足：

- 所有 `statusCheckRollup` 条目 `status=COMPLETED`，且 `conclusion` ∈ `SUCCESS` / `NEUTRAL` / `SKIPPED`；
- `mergeable=MERGEABLE`。`UNKNOWN` 表示 GitHub 还在计算，本 tick 不合并，等下一轮。

## 送审（同一 sha 只送一次）

条件：从未送审，或 `delivery_sha` 与当前 `headRefOid` 不同。

动作：

1. 发一条**只 mention 包公**的评论：

```text
[@包公 - 质量工程师]({{agent:包公 - 质量工程师}})

[DELIVERY-GATE] 请复核 <IDENTIFIER>。

- PR: <url>
- 分支: <headRefName> → <baseRefName>
- 仅审这个 head sha: <headRefOid>
- 验收标准: <从 issue 描述提取，逐条列出>

输出要求：
1. 评论第一行必须是 `DELIVERY-VERDICT: PASS` 或 `DELIVERY-VERDICT: FAIL`。
2. FAIL 时附最小返工清单（文件/行为/期望 vs 实际/证据）。
3. 不改代码、不合并 PR、不改任务状态。
```

2. 用 `--output json` 拿到这条评论的 id，写入 metadata：

```bash
multica issue metadata set <id> --key delivery_state --value reviewing --type string
multica issue metadata set <id> --key delivery_sha --value <sha> --type string
multica issue metadata set <id> --key delivery_pr --value <url> --type string
multica issue metadata set <id> --key delivery_requested_at --value <now-rfc3339> --type string
multica issue metadata set <id> --key delivery_request_comment --value <comment-id> --type string
multica issue metadata set <id> --key delivery_last_action --value review_requested --type string
multica issue metadata set <id> --key delivery_last_action_at --value <now-rfc3339> --type string
```

3. 该 issue 本 tick 处理结束，不阻塞等待。

## 处理包公判定

在 `delivery_request_comment` 之后、由包公（`7dbd97b3-7db4-4770-9035-4ffaded58f68`）发表的评论里，取最后一条匹配：

```bash
grep -oE 'DELIVERY-VERDICT: (PASS|FAIL)'
```

同一条判定已经处理过（`delivery_last_action` 已记录对应动作），不要重复执行。

### 没有判定

- 包公仍有 active/queued run：等待，不评论。
- 送审超过 45 分钟且包公无 active run：升级一次（`@all`），写 `delivery_state=escalated`、`delivery_escalated_at`。之后不再评论。

### FAIL

1. `delivery_rounds` 加 1。
2. 若 `delivery_rounds >= 3`：不派工。发一次 `@all` 升级评论，`multica issue status <id> blocked`，`delivery_state=escalated`，结束。
3. 否则：
   - 发一条**不带任何 agent mention** 的评论，原样引用包公的返工清单与证据，并写明「修复并推送后把状态改回 in_review」。
   - 唤醒负责人（见「唤醒负责人」）。
   - 写 `delivery_state=fixing`、`delivery_verdict=FAIL`、`delivery_last_action=rework_sent`、`delivery_last_action_at`。
4. 如果 issue 回到 `in_review` 但 head sha 仍等于 `delivery_sha`（没有新提交）：不要重复送审。发一条不带 mention 的说明「未检测到新提交」，升级一次并写 `delivery_state=escalated`。

### PASS

1. 若 CI 不绿或 `mergeable != MERGEABLE`：
   - `delivery_rounds` 加 1；达到 3 则按上面的升级流程处理。
   - 否则发一条不带 mention 的评论，列出失败的 check 或冲突，并唤醒负责人修 CI。
   - 写 `delivery_state=fixing`、`delivery_verdict=PASS`、`delivery_last_action=ci_fix_sent`、`delivery_last_action_at`。
   - 同一 sha 不重复提醒；只有 head sha 变化后才重新进入送审。
2. 若 CI 全绿且可合并：
   - `gh pr merge <pr-url> --squash --delete-branch`
   - 用 `gh pr view <pr-url> --json state` 确认 `MERGED`；失败则升级一次，禁止谎报成功。
   - `multica issue status <id> done --no-start`
   - 发一条不带 mention 的完成评论：PR 链接、合并方式、包公结论摘要。
   - `delivery_state=merged`。

## 进行中（in_progress）review

目的：不等负责人手动改成 `in_review`；有 PR 的进行中 issue 也走同一套最终门禁。没有 PR 的不送审。

1. 取 PR（linked + 评论），按 PR 去重。
2. 任一 PR `MERGED`：
   - 所有关联 PR 都已合并 → `multica issue status <id> done --no-start`，发一条简短对账评论，`delivery_state=merged`。
   - 还有未合并 PR → 继续处理未合并的那个。
3. 有 `OPEN` PR：
   - `isDraft=true` → 跳过。
   - 该 issue 有 active run → 跳过。
   - `delivery_sha == headRefOid` 且 `delivery_state=reviewing` → 已在审，跳过。
   - `delivery_state=fixing` 且 sha 未变 → 跳过。
   - CI 有失败 check → 不送包公；发一条不带 mention 的评论列失败项，唤醒负责人修 CI，写 `delivery_state=fixing`、`delivery_last_action=ci_fix_sent`。
   - CI 全绿或仍在计算 → 按「送审」流程送给包公，并写 `delivery_state=reviewing`。判定处理与 `in_review` 完全相同；PASS + CI 绿后直接合并并标完成。
4. 没有任何 PR：
   - 有 active run → 跳过。
   - 无 active run 且 `updated_at` 超过 24 小时、`delivery_next_action_at` 未设置或已过期 → 发一条不带 mention 的进度提醒「进行中但未发现 PR，请补 PR 或说明当前进展」，唤醒负责人一次；写 `delivery_last_action=progress_nudge`、`delivery_next_action_at=now+24h`、`delivery_start_attempts` 加 1。
   - `delivery_start_attempts >= 2` 仍无 PR → 升级一次（`@all`），写 `delivery_state=escalated`，不再催。

## 待办 / 待规划分诊（todo / backlog）

目的：判断是否具备开工条件。有条件就安排合适的 agent 并启动；没有就写清缺什么并改成 `blocked`。

### 就绪判定（保守）

全部满足才算 `ready`：

1. **目标清楚**：描述说明要解决什么问题/达到什么结果，不能只有标题。
2. **范围可执行**：有页面、文件、接口、命令或复现步骤；知道从哪里下手。
3. **验收可判断**：有期望行为、验收标准或完成定义；bug 的「期望 vs 实际」也算。
4. **无未解决依赖**：没有「依赖 / 等待 / 先完成 / blocked by」且依赖项未完成。
5. **负责人可确定**：已有 agent/squad，或能按下面的路由表确定。
6. **资源可用**：代码改动有仓库资源；运维/文档/SEO 类除外。

任一不满足 → `blocked`。评论必须写清缺哪一项、补什么，不能只写「不满足」。有疑问时按 `blocked` 处理，但不要拦已经有明确复现步骤和期望结果的 bug。

### 跳过条件

- `delivery_triage_at >= issue.updated_at` 且 `delivery_triage_verdict` 是 `blocked` 或 `ready_manual` → 已分诊且未变化，跳过。`ready_pending` 不跳过，下一轮继续尝试启动。
- 已有 active run → 跳过。
- `delivery_state=escalated` → 跳过。
- 已经是 `blocked` → 跳过。

### 负责人路由

已有 agent/squad 负责人时优先保留，不要为了「更合适」频繁改派。只有无负责人，或负责人明显跨域时才改派：

| 领域 | 首选 |
|---|---|
| 前端/后端功能、缺陷修复 | 墨子 `de615f5d-9f8b-48de-b1a4-0ff50c0b489e` |
| UI/UX、原型、设计系统 | 巧匠 `f6fae5b2-775c-45ca-88cb-83b386fe3b0a` |
| 架构、重构、技术选型 | 鲁班 `d036a1a6-c61e-4a1c-8737-ddc9a97d9a46` |
| 基础设施、部署、域名、监控、SEO 技术 | 神机 `5d458dd6-a0d7-4cb8-a1bb-2e3727e71969` |
| 数据分析、指标、竞品 | 慧眼 `a865cb33-6226-4069-b33a-9e9ff9704fab` |
| 市场、内容、社区、外联 | 范蠡 `88e1baf0-8313-49a4-81f5-4ed82e37b8e4` |
| 多阶段功能开发 | 孔明 `739c6aca-3a9a-4702-aff3-c869504b967b` 拆分，或 devloop squad |

member 负责人：不自动改派，只把 issue 移到 `todo` 等待人工处理，并写 `delivery_triage_verdict=ready_manual`。

### ready 动作

1. 确定负责人；无负责人则先 `multica issue update <id> --assignee-id <agent-id> --no-start`，只改归属不启动。
2. 启动：
   - `backlog`：`multica issue update <id> --status todo`（不要加 `--no-start`），由待办状态启动负责人。
   - `todo`：`multica issue update <id> --status in_progress`（不要加 `--no-start`），由状态变化唤醒负责人。
   - member 负责人：`multica issue update <id> --status todo --no-start`，只排队不启动。
   - 已在 `todo` 且有 agent 负责人和 active run：不重复启动。
3. 每个 agent 本轮最多启动 2 个新 issue。因 agent 本轮已启动过而没轮到的，写 `delivery_triage_verdict=ready_pending`、`delivery_triage_at=now`，不增加 `delivery_start_attempts`，下一轮再试；不要因此 blocked 或升级。
4. 同一 issue 累计启动 2 次仍回到 `todo`/`backlog` → `blocked` + 一次 `@all` 升级。
5. 写 `delivery_triage_at=now`、`delivery_triage_verdict=ready`、`delivery_last_action=started`、`delivery_last_action_at`。
6. 发一条简短评论：「已就绪，已派给 X」。

### blocked 动作

1. 发一条不带 mention 的评论，逐条列出缺失项和补齐方式。
2. `multica issue status <id> blocked`。
3. 写 `delivery_triage_at=now`、`delivery_triage_verdict=blocked`、`delivery_last_action=blocked`、`delivery_last_action_at`。
4. 同一 issue 只 blocked 一次；之后除非 `updated_at` 变化，不再处理。

## 无 PR 分支（仅 in_review）

- issue 所属项目有 `github_repo` 资源，或评论中出现过 PR/commit：视为代码改动。
  - 发一条**只 mention 负责人**的评论，要求补 PR 或说明为什么不需要 PR。
  - 写 `delivery_state=awaiting_pr`、`delivery_next_action_at = now + 24h`、`delivery_last_action=pr_requested`。
  - 24 小时内不再评论；24 小时后仍无 PR：升级一次（`@all`），写 `delivery_state=escalated`。
- issue 明确不需要代码 PR（文档/配置/运维确认）：先按送审流程让包公复核；PASS 后 `multica issue status <id> done --no-start`。
- 从未送审过的无 PR issue：先送审，再按判定处理。
- 没有负责人且无法送审：只升级一次，写 `delivery_state=escalated`。

## 唤醒负责人

- issue 当前不是 `in_progress`：`multica issue status <id> in_progress`（不要加 `--no-start`），由状态变化唤醒。
- issue 已经是 `in_progress`：`multica issue assign <id> --to-id <assignee-id>`（不要加 `--no-start`），重新派发一次运行；只用于返工、修 CI、进度提醒。
- squad 负责人：状态变化会唤醒 leader；也可以单独 mention squad。
- member 负责人：mention 不派发运行；需要人工处理时用 `[@all](mention://all/all)` 升级。

## 硬性约束

- 一条评论只 mention 一个 agent/squad；不要把包公和负责人写进同一条评论。
- 没有 `DELIVERY-VERDICT: PASS` 且 sha 与当前 `headRefOid` 一致时，禁止合并。
- CI 未完成、`UNKNOWN`、有失败 check、或 `mergeable != MERGEABLE` 时，禁止合并。
- 不 force push、不直接 push main、不改代码、不篡改包公结论。
- 不用 `multica issue verdict`（那是 squad reviewer 的接口）。
- 不重复送审同一 sha；不重复合并已 `MERGED` 的 PR；不重复发同类评论。
- 同一 PR 同一 sha 只送审一次；多 issue 共享 PR 时等判定后统一对账。
- 不因为「可以更合适」频繁改派已有 agent 负责人。
- 每个 tick 只推进到「派发下一步」为止，不等待包公跑完。
- gh 账号：仓库 remote 含 `github_sjzsdu` 时切到 `sjzsdu`；其余用默认账号。执行前先 `gh auth status`。

## 结果记录

- 有动作：在 issue 上留一条简短评论（做了什么、触发原因、下一步）。
- 无动作：不发评论，静默结束。
- 同一 issue 30 分钟内最多一条自动化评论；升级后停止评论。
- 不新建 issue，不在自动化运行里写总结任务。
