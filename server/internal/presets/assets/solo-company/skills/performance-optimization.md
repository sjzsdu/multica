---
name: performance-optimization
description: "Use when optimizing performance — profiling, identifying bottlenecks, database queries, frontend rendering, caching. Triggers: 性能优化, 优化, profiling, 慢, 瓶颈, 缓存, 性能, 响应慢."
---

# 性能优化手册

## 核心原则

**先测后改。** 没证实是瓶颈就优化，是浪费时间且可能引入 bug。

**Pareto 法则**：80% 的时间花在 20% 的代码上。找到那 20%。

## 优化流程

1. **量化**：定义指标（响应时间、吞吐量、P95/P99）
2. **测量**：用 profiler 找到瓶颈，不靠猜
3. **假设**：提出根因假设
4. **验证**：改之前测一次，改之后测一次，对比
5. **确认**：没有引入回归，指标确实改善

## 后端优化

### 数据库（最常见的瓶颈）

| 问题 | 解法 |
|---|---|
| 缺索引 | EXPLAIN ANALYZE 看执行计划，加索引 |
| N+1 查询 | 用 JOIN 或批量 IN 查询 |
| SELECT * | 只查需要的列 |
| 深分页 | 用 keyset pagination（`WHERE id > last_id`） |
| 大 OFFSET | 同上 |
| 慢 COUNT(*) | 用估计值或缓存 |
| 锁等待 | 缩短事务、优化索引减少锁范围 |
| 连接池耗尽 | 调大池大小或排查泄漏 |

### 应用层

| 问题 | 解法 |
|---|---|
| 同步串行调用 | 改并行（Promise.all / goroutine） |
| 重复计算 | 缓存结果 |
| 大对象序列化 | 用更高效的格式（protobuf） |
| 内存泄漏 | heap profile，找未释放的引用 |
| GC 频繁 | 减少对象分配、对象池 |

### 缓存

- 缓存什么：读多写少、计算昂贵、允许短暂过期的数据
- 缓存策略：Cache-Aside（最常用）、Read-Through、Write-Through
- 过期策略：TTL + 主动失效
- 缓存击穿：热点 key 用 singleflight / 互斥锁
- 缓存雪崩：TTL 加随机抖动
- 不要缓存：强一致要求的数据、写多读少的数据

## 前端优化

### 渲染性能

| 问题 | 解法 |
|---|---|
| 不必要的 re-render | React.memo、useMemo、useCallback、状态下放 |
| 大列表 | 虚拟滚动（react-virtual / react-window） |
| 重排重绘 | 避免 layout thrashing，用 transform 代替 top/left |
| 长任务 | 拆分到多个帧（requestIdleCallback、Suspense） |

### 加载性能

| 问题 | 解法 |
|---|---|
| bundle 太大 | 代码分割、tree shaking、移除无用依赖 |
| 首屏慢 | SSR/SSG、关键 CSS 内联、预加载 |
| 图片大 | 用 WebP/AVIF、懒加载、响应式图片 |
| 第三方脚本 | 异步加载、延迟加载非关键脚本 |

### 网络

| 问题 | 解法 |
|---|---|
| 请求太多 | 合并请求、GraphQL、批量 API |
| 串行请求 | 并行（Promise.all） |
| payload 大 | 压缩、只返回需要的字段 |
| 重复请求 | 缓存（TanStack Query 自动处理） |

## 测量工具

| 场景 | 工具 |
|---|---|
| Go CPU/内存 | `pprof` |
| Node.js | `clinic.js`、`0x`、Chrome DevTools |
| React 渲染 | React Profiler |
| 前端整体 | Chrome DevTools Performance / Lighthouse |
| 数据库 | `EXPLAIN ANALYZE`、慢查询日志 |
| 网络 | Chrome DevTools Network、后端 access log |

## 不要做的事

- **不要过早优化**：写清晰的代码，证实慢了再优化
- **不要优化错误的东西**：P99 10s 但 QPS 很低，可能不值得优化
- **不要为优化牺牲可维护性**：除非性能是硬需求
- **不要只看平均值**：看 P95/P99，平均值掩盖长尾
- **不要缓存一切**：缓存带来一致性问题，能不加就不加

## 输出要求

做性能优化时，必须说明：

1. 当前指标是什么（量化，不要说"有点慢"）
2. 瓶颈在哪里（profiler 证据）
3. 优化方案和预期收益
4. 优化后指标是多少（实测对比）
