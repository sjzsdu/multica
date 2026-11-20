---
name: impeccable-frontend-design
description: "Use when designing, redesigning, shaping, critiquing, auditing, polishing, clarifying, hardening, optimizing, adapting, animating, colorizing, extracting, or otherwise improving a frontend interface. Covers websites, landing pages, dashboards, product UI, app shells, components, forms, settings, onboarding, and marketing pages."
---

# Impeccable — 反 AI Slop 设计审计

本 skill 引导创建**独特、生产级、避免 AI 通用感**的前端界面。

## 设计方向

**BOLD 美学方向**：

- **Purpose**: 这个界面解决什么问题？谁用？
- **Tone**: 选一个极端：brutally minimal / maximalist chaos / retro-futuristic / organic / luxury / playful / editorial / brutalist / art deco / soft pastel / industrial 等。明确方向，果断执行。
- **Constraints**: 技术栈、性能、无障碍
- **Differentiation**: 让人记住的那个点是什么？

**CRITICAL**: 选清楚方向，精确执行。大胆极简 vs 精致极简都可以——关键是 intentionality，不是 intensity。

## 排版

**DO**: modular type scale + fluid sizing (clamp)；变化字号和字重创建层次

**DON'T**: Inter/Roboto/Arial/system defaults；monospace 当 lazy technical vibe；每个 heading 上放大圆角图标

## 颜色

**DO**: 用现代 CSS color functions (oklch, color-mix, light-dark)；neutrals 向品牌色微 tint

**DON'T**: 彩色背景上灰字；纯黑 #000 / 纯白 #fff；cyan-on-dark / purple-blue gradient / neon accent on dark（AI 调色板指纹）；gradient text

## 布局与空间

**DO**: varied spacing（tight groupings + generous separations）；fluid spacing with clamp()；asymmetry；故意打破 grid 做强调

**DON'T**: 什么都塞进卡片；卡片套卡片；千篇一律的 icon + heading + text card grids；hero metric layout template（大数字+小标签+stats+gradient accent）；居中一切；所有地方用相同间距

## 视觉细节

**DON'T**: glassmorphism everywhere；圆角+单边粗彩边的 lazy accent；sparklines 装饰；通用圆角矩形+阴影；能用 modal 就不用 modal

## 动效

**DO**: 用 motion 解释 state / continuity / hierarchy / causality；exponential easing (ease-out-quart/quint/expo)；grid-template-rows transitions 代替 height 动画

**DON'T**: 动画 layout properties (width, height, padding) — 只 animating transform 和 opacity；bounce / elastic easing

## 交互

**DO**: optimistic UI；progressive disclosure（基础 options 先，高级 behind expandable）；设计 teaches interface 的 empty states

**DON'T**: 重复信息；每个按钮都 primary

## 响应式

**DO**: container queries (@container)；adapt 不同上下文

**DON'T**: mobile 上隐藏关键功能 — adapt，不要 amputate

## UX 写作

**DO**: 让每个字都有意义

**DON'T**: 重复用户已经能看到的信息

## AI Slop 测试

**Critical quality check**: 如果给某人看这个界面说"AI 做的"，他们会不会立刻相信？会的话，那就是问题。

impeccable interface 应该让人问"这怎么做到的"，而不是"哪个 AI 做的"。
