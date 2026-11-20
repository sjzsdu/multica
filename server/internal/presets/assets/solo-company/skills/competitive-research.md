---
name: competitive-research
description: "Use when analyzing competitors, mapping product landscapes, identifying product opportunities, scanning market trends, or building structured PRDs. Triggers: 市场研究, 品类分析, 竞品分析, 产品机会分析, 市场扫描, market research, competitor analysis, product opportunity."
---

# 竞品分析与产品机会发现

## 核心方法论

### 信号 > 声称

**强信号**（trust behavior over statement）：
- 用户反复出现的痛苦 workaround
- 高优先级问题的出现频率
- 用户主动推荐给有相同痛点的人
- 付费意愿 / 愿意 pilot / 愿意 pre-order / 愿意 switch

**弱信号**（不要过度相信）：
- "great idea"
- generic survey positivity
- likes、stars、"很有前景"类评论

### 后 AI 时代竞品分析框架

1. **入口层**：竞品的主入口是聊天框、表单还是 Agent 面板？
2. **Agent 层**：他们用了什么工具/API/工作流编排？
3. **数据层**：状态存在哪？能不能 resume？
4. **人机协作层**：审批点在哪？人类带宽分配得怎么样？
5. **生态层**：有没有开放 API / 插件市场 / Agent SDK？

### 差异化定位模板

列 3-5 个关键维度，把自己和 Top 3 竞品放进去对比，找差异化象限。输出一个矩阵。

### 9 大分析模块（每个按"信号 → 洞察 → 机会 → 行动"四层）

1. **市场趋势** — 增长/衰退/新兴，驱动力是什么
2. **用户洞察** — 谁在用？什么场景？未被满足的需求
3. **需求/痛点/行为** — 按强信号筛选
4. **竞争格局** — direct vs indirect，矩阵对比
5. **产品机会** — 空白象限、差异化点
6. **品牌洞察** — 用户心智、命名、定位
7. **营销策略** — 渠道、定价、GTM
8. **技术形态** — 竞品是 Agent-native 还是壳套 AI？
9. **交付形态** — SaaS / API / Agent SDK / 自部署

### 输出要求

- 每个结论必须标注证据来源
- 信号→洞察→机会→行动，每层可追溯
- 发现冲突时必须披露，不要掩盖
- 最后给一个优先级排序的行动清单（top 3）

### PRD 结构建议

1. 一句话目标（outcome）
2. 目标用户画像 + 核心场景
3. Agent-native 还是人类 UI 为主？决策理由
4. 关键功能（MVP / Phase 2 / 后续）
5. 风险与假设
6. 成功指标（signal metrics，不要 vanity metrics）
