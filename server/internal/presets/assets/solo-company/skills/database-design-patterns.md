---
name: database-design-patterns
description: "Use when designing database schemas, indexes, migrations, queries, or optimizing data access. Triggers: 数据库设计, schema, 索引, 迁移, migration, 查询优化, SQL, 数据模型."
---

# 数据库设计模式

## 设计原则

1. **先想查询，再想表结构**。表结构是为查询服务的，不是为了"规范化好看"。
2. **写一次，读多次**。为读优化，不为写优雅。
3. **能在应用层做的约束，不建外键**（Multica 规则：不加外键、不加级联）。

## 索引设计

### 什么时候加索引

- WHERE 条件里频繁出现的列
- JOIN 的连接列
- ORDER BY 的排序列
- 有唯一性要求的列（UNIQUE INDEX）

### 什么时候不加

- 低基数列（性别、状态只有几个值）——全表扫描更快
- 写远多于读的表——索引拖慢写入
- 小表（< 1 万行）——内存里全表扫描够快

### 复合索引顺序

**最左前缀原则**：把 WHERE 等值条件的列放前面，范围条件的列放后面。

```sql
-- 好：WHERE workspace_id = ? AND status = ? ORDER BY created_at DESC
CREATE INDEX idx_issues_ws_status_created ON issues(workspace_id, status, created_at DESC);

-- 坏：把范围列放前面，后面的列用不上
CREATE INDEX idx_bad ON issues(created_at, workspace_id, status);
```

### 并发索引（Multica 规则）

- 所有索引用 `CREATE [UNIQUE] INDEX CONCURRENTLY`
- 每个并发索引一个独立迁移文件
- 在 `migrate/main.go` 的 `concurrentIndexCleanups` 注册
- 不要在事务里建并发索引

## 查询优化

### 必做

- 用 `EXPLAIN ANALYZE` 看执行计划，不要靠猜
- 避免 `SELECT *`，只查需要的列
- 大结果集必须分页（LIMIT/OFFSET 或 keyset pagination）
- 深分页（OFFSET > 10000）用 keyset pagination（`WHERE id > last_id`）

### 常见慢查询原因

| 现象 | 原因 | 解法 |
|---|---|---|
| Seq Scan 大表 | 缺索引或索引没命中 | 加索引 / 改 WHERE |
| 函数包索引列 | `WHERE lower(email) = ?` 不走索引 | 建表达式索引或改查询 |
| N+1 查询 | 循环里查数据库 | 用 JOIN 或批量 IN 查询 |
| 排序慢 | ORDER BY 无索引 | 加覆盖索引 |
| COUNT(*) 慢 | 全表计数 | 用估计值或缓存 |

## 迁移设计

### 原则

- **每个迁移必须可回滚**（UP + DOWN，除非框架不支持）
- 破坏性变更分两步：先加（兼容），后删（确认无依赖）
- 数据迁移和 schema 迁移分开
- 大表加列用 `DEFAULT NULL` + 后台回填，不要 `DEFAULT 'x' NOT NULL`（锁表）

### Multica 特有

- 不加外键、不加级联
- 并发索引单独文件 + 注册 cleanup
- 列有 DEFAULT 时，INSERT 必须 `COALESCE` 兜底（sqlc 会透传 NULL）

## 软删除 vs 硬删除

| 场景 | 方案 |
|---|---|
| 需要恢复 / 审计 | 软删除（`revoked_at` / `deleted_at`） |
| 不需要、且量大 | 硬删除 |
| 关联数据 | 应用层清理，不用级联 |

**注意**：软删除后，所有查询必须加 `WHERE revoked_at IS NULL`，否则会查到已删除数据。

## JSON / JSONB

- PostgreSQL 用 **JSONB**（有索引、可查询），不用 JSON
- JSONB 字段适合：配置、非结构化元数据
- 不适合：需要频繁 JOIN 的关系数据、需要强一致约束的字段
- JSONB 索引用 GIN

## 输出要求

做数据库设计时，必须说明：

1. 主要查询模式是什么（读多写少？哪些 WHERE 条件？）
2. 索引策略：加哪些索引、为什么
3. 数据量级预估（决定要不要分表/分区）
4. 迁移风险：会不会锁表、要不要分批
