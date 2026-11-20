---
name: tech-stack-decision-matrix
description: "Use when choosing between specific technologies — databases, frameworks, hosting, auth, payments, queues, monitoring — for a solo-founder product. Triggers: 技术选型, 选哪个, vs, 对比, 数据库选型, 部署方案, tech stack, choose between."
---

# 技术栈决策矩阵（一人公司场景）

## 使用方式

被问"选 A 还是 B"时，**先在下面对应类别里找到对比表**，按"推荐场景 / 不推荐场景 / 月成本 / 退出难度"四条给结论。不要泛泛而谈"看情况"。

> 价格为量级参考，会变动。给具体数字时标注"以官方最新定价为准"。

## 一、数据库

| 方案 | 推荐场景 | 不推荐 | 月成本量级 | 退出难度 |
|---|---|---|---|---|
| **SQLite** | MVP、单文件、嵌入式、读多写少、<10万行 | 多实例并发写、需要用户权限隔离 | $0 | 极易（一个文件） |
| **PostgreSQL**（自托管） | 关系型为主、需要事务、JSONB、复杂查询、长期项目 | 完全没运维能力 | $5-20（VPS） | 中（pg_dump 可导出） |
| **Supabase** | 要 PG + Auth + Storage + Realtime 一把梭 | 已有 Auth 方案、怕锁定 | $0 起步 / Pro $25 | 中（PG 可导出，Auth 难迁） |
| **Neon** | Serverless PG、按需扩缩、多分支 | 持续高并发（计费不划算） | $0 起步 | 中 |
| **MongoDB** | 文档模型天然契合、schema 频繁变 | 关系密集、需要多表事务 | Atlas $0 起步 | 中 |
| **Turso** | 边缘部署 SQLite、全球读副本 | 写密集、强一致 | $0 起步 | 中 |

**一人公司默认建议**：能 SQLite 就 SQLite，要 PG 优先 Supabase/Neon（免运维），慎选自托管 PG。

## 二、后端框架

| 方案 | 推荐场景 | 不推荐 | 学习曲线 | 一人适配度 |
|---|---|---|---|---|
| **Hono**（TS） | 边缘/Cloudflare Workers、轻量 API | 重业务逻辑、复杂中间件生态 | 低 | 高 |
| **Fastify**（TS） | Node API、要插件生态 | — | 低 | 高 |
| **NestJS**（TS） | 大型模块化、企业结构 | 小项目（过重） | 中高 | 低 |
| **Go + Chi** | 高并发、CLI 工具、单二进制部署 | 团队不熟 Go | 中 | 中 |
| **FastAPI**（Py） | AI/ML 集成、数据密集 | 高并发 IO 密集型 | 低 | 高 |
| **Next.js Route Handlers** | 前后端一体、轻量后端 | 复杂后端逻辑 | 低 | 高 |

**原则**：前后端同语言优先（TS 一把梭），别为了"性能"上 Go/Rust 除非有明确瓶颈。

## 三、前端框架

| 方案 | 推荐场景 | 不推荐 | 部署成本 | Agent 生成代码质量 |
|---|---|---|---|---|
| **Next.js** | SSR/SSG 都要、SEO 重要、生态丰富 | 简单静态站（过重） | Vercel $0 起 | 高（训练数据多） |
| **SvelteKit** | 极简、编译时优化 | 需要大型组件生态 | 低 | 中 |
| **Remix / React Router** | 嵌套路由、表单密集 | — | 低 | 高 |
| **Astro** | 内容站、营销站 | 重交互应用 | $0 | 中高 |
| **Vite + React SPA** | 纯前端应用、有独立后端 | SEO 敏感 | $0 | 高 |

## 四、部署

| 方案 | 推荐场景 | 不推荐 | 月成本量级 | 运维负担 |
|---|---|---|---|---|
| **Vercel** | Next.js 项目、零运维 | 长连接、WebSocket、后台任务 | $0 / Pro $20 | 极低 |
| **Cloudflare** | 边缘计算、静态站、Workers | 需要传统服务器 | $0 / Workers $5 | 极低 |
| **Fly.io** | 容器化、要长连接/后台任务、多区域 | 完全不懂 Docker | $0-10 | 低 |
| **Railway** | 全栈一键部署、内置 PG | 大规模 | $5-20 | 低 |
| **Hetzner/Contabo VPS** | 长期跑、成本敏感、有运维能力 | 不想碰服务器 | €4-10 | 中 |
| **AWS/GCP** | 企业级、合规要求 | 一人公司（复杂度高） | 不定 | 高 |

**默认建议**：前端 Vercel/Cloudflare，后端+DB 优先 Supabase/Neon 免运维，避免一上来就 AWS。

## 五、认证

| 方案 | 推荐场景 | 不推荐 | 月成本 | 锁定风险 |
|---|---|---|---|---|
| **Supabase Auth** | 已用 Supabase | 独立后端 | 含在 Supabase | 高 |
| **Auth.js** | Next.js、自托管、要 OAuth | 非 JS 栈 | $0 | 低 |
| **Clerk** | 快速上线、用户管理 UI 现成 | 用户量大（按 MAU 计费） | $0 起 / $25+ | 中 |
| **Casdoor** | 开源自托管、要 LDAP/SAML | 简单场景（过重） | 自托管 | 低 |
| **Lucia + 自写** | 极简、完全掌控 | 不想写认证代码 | $0 | 无 |

## 六、支付

| 方案 | 推荐场景 | 不推荐 | 费率 | 税务处理 |
|---|---|---|---|---|
| **LemonSqueezy** | 数字产品、SaaS、个人开发者、要处理全球 VAT | 实物、中国大陆主体 | ~5%+50¢ | ✅ 全包（MoR） |
| **Polar** | 同上、开源友好 | — | ~4%+40¢ | ✅ MoR |
| **Stripe** | 有公司主体、要完整支付能力 | 个人开发者没主体 | ~2.9%+30¢ | ❌ 自己处理 |
| **Paddle** | B2B SaaS、要 MoR | — | ~5%+50¢ | ✅ MoR |

**个人开发者默认 LemonSqueezy 或 Polar**，省掉全球 VAT/GST 申报。

## 七、队列与后台任务

| 方案 | 推荐场景 | 不推荐 | 月成本 | 运维 |
|---|---|---|---|---|
| **pg-boss** | 已用 PG、轻量队列 | 高吞吐 | $0 | 零 |
| **Trigger.dev** | 长任务、要 UI、Serverless | 自托管需求 | $0 起 | 零（SaaS） |
| **Inngest** | 事件驱动、Serverless | — | $0 起 | 零 |
| **BullMQ + Redis** | Node、要精细控制 | 没 Redis | Redis 成本 | 中 |
| **简单 cron + DB 表** | 任务少、可延迟 | 实时性要求高 | $0 | 零 |

**默认**：先用 cron + DB，不够再上 pg-boss，慎上 Redis。

## 八、监控与分析

| 方案 | 用途 | 月成本 | 备注 |
|---|---|---|---|
| **Uptime Kuma**（自托管） | 可用性监控 | $0 | 一个 Docker |
| **Better Stack** | 日志+监控+告警 | $0 起 | SaaS |
| **Plausible** | 隐私友好网站分析 | $9 | 可自托管 |
| **PostHog** | 产品分析+Feature Flag | $0 起（免费额度大） | 可自托管 |
| **Sentry** | 错误追踪 | $0 起 | — |

## 决策输出格式

被问选型时输出：

```
推荐：<方案A>
理由：
  1. <针对当前场景的核心理由>
  2. <成本/运维/退出中的一项关键优势>
对比：<方案B> 在 <什么条件变化时> 更合适
成本：<月成本量级>（以官方最新定价为准）
退出：<退出难度一句话>
风险：<最大的一个风险>
```

**禁止**：罗列 5 个选项让用户自己选、只说"看情况"、不给退出难度评估。
