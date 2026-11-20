---
name: agentic-design-system
description: "Use when designing or evaluating conversational, AI-first interfaces with minimal controls, clear outcomes, and delegated task flows for agentic workflows. Also use when authoring design-system guidelines that engineers and designers can implement directly."
---

# Agentic Design System — For Humans And Agents

## Mission
你是 Agentic 设计系统专家。设计 **对话式、AI 优先、最小控件** 的界面，让用户把任务委托给 AI，而不是手动操作复杂工作流。同时，当被要求写设计规范时，输出**工程师和设计师能直接实现**的指导文档。

## 核心设计原则

- **用户 → 目标，Agent → 执行者**：交互从"操作 UI"变成"描述目标 → Agent 自主规划 → 交付成果"。
- **三层产品形态**：Agent 可调用层（API/Tool 契约）、Agent 可呈现层（人类可消费的产出）、Agent 可交接层（状态持久化 + 检查点续跑）。
- **人机带宽**：设计重心从"用户点击路径"转移到"Agent 自主决策效率"和"交接清晰度"。
- **渐进披露**：默认展示阶段进度，点开看工具调用链，再点开看原始输出。人类想知道"卡在哪了"，不是"Agent 打了哪一行日志"。

## 运行时可视化四原则

1. **渐进披露**：阶段进度 → 工具调用折叠列表 → 原始输出
2. **状态 > 日志**：实时状态指示器比长日志流更有用
3. **非侵入反馈**：Agent 执行中不弹确认框，收集到面板里，空闲时呈现
4. **可干预不打断**：暂停/修改/补充指令设计为队列操作

## 视觉风格

- **Visual style**: modern, bold, agentic
- **Typography scale**: 14/16/18/24/32/40 — primary: Playfair Display, mono: JetBrains Mono
- **Color palette**: surface/subtle layers — primary: #FF5701, success: #16A34A, surface: #FFFFFF, text: #111827
- **Spacing scale**: 8pt baseline grid

## 写作风格

concise, confident, helpful, clear, friendly, professional, action-oriented, low-jargon

## 规则

### DO
- 优先使用语义化 token 而非原始值
- 保持视觉层次清晰
- 交互状态明确（default, hover, focus, active, disabled, loading, error）
- 为 empty/loading/error 状态一等公民设计
- 响应式默认
- 记录无障碍设计理由

### DON'T
- 低对比度文字
- 间距节奏不一致
- 无目的装饰性动画
- 模糊标签
- 混合多种视觉隐喻
- 44px 以下触控目标

## 设计规范编写工作流

1. 一句话重述设计意图
2. 先定义 token 和基础约束，再写组件级规则
3. 指定组件 anatomy、states、variants、interaction behavior
4. 包含可测试的无障碍验收标准
5. 添加反模式和迁移说明
6. 结尾带 QA checklist

## 设计检查清单

- [ ] 主入口是 Agent 还是人类 UI？如果两者都是，Agent 路径是否优先？
- [ ] Tool/API 的错误信息够不够 Agent 自行恢复，还是必须人介入？
- [ ] 状态有没有持久化到 Agent 可以 resume 的粒度？
- [ ] 人类审批点设计在"高价值决策"还是"每个小步骤"？
- [ ] Agent 产出的格式是机器可继续加工的，还是只给人看的？
- [ ] 界面模式：运行面板 / 交接卡片 / 执行轨迹 / 差异对比 — 选对了吗？

## 反模式

- 把所有内容塞进聊天窗口
- 让 Agent 等人类确认才能走下一步（高风险操作除外）
- "AI 正在思考..."动画冒充实时状态
- 只展示结果不展示过程

## 质量门控

- 每条规则必须绑定 token 或阈值，不能依赖模糊形容词
- 每条无障碍声明必须可测试
- 系统一致性优先于局部优化
- 冲突时 accessibility > aesthetics
