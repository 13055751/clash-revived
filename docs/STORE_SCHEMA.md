# store（SQLite）表结构契约 — P1c `/profiles` 的前置

> 来源：`apk/lib/arm64-v8a/libclash.so` 中的 SQL 字符串常量，逐条 `CREATE TABLE` 抓取。
> 库文件位置：`<DatabasePath>/store.db`，其中 `DatabasePath` 由 `initialize` 的 **slot1** 给出
> （见 PROGRESS 第 5 阶段：`slot1 = getDatabasePath("store.db")`）。

## 1. 完整 DDL（原文，未改写）

```sql
CREATE TABLE preferences (
    key VARCHAR(128) PRIMARY KEY,
    value BLOB
);

CREATE TABLE profiles (
    id UUID PRIMARY KEY,
    version INTEGER,
    name TEXT,
    remoteURL TEXT,
    updateExpr TEXT,
    updatedAt TIMESTAMP,
    content BLOB,
    modeOverride VARCHAR(32)
);

CREATE TABLE user_infos (
    profileID UUID PRIMARY KEY,
    uploaded INTEGER,
    downloaded INTEGER,
    total INTEGER,
    expireAt TIMESTAMP,
    FOREIGN KEY(profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE
);

CREATE TABLE providers (
    profileID UUID,
    type VARCHAR(16),
    name TEXT,
    updatedAt TIMESTAMP,
    content BLOB,
    FOREIGN KEY (profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE,
    PRIMARY KEY (profileID, type, name)
);

CREATE TABLE proxy_selections (
    profileID UUID,
    groupName TEXT,
    proxyName TEXT,
    FOREIGN KEY (profileID) REFERENCES profiles(id) ON DELETE CASCADE ON UPDATE CASCADE,
    PRIMARY KEY (profileID, groupName)
);
```

## 2. API 模型 ↔ 列映射（P1c 直接照这个实现）

```
Profiles$Profile
├── id              ← profiles.id
├── attributes      → Profiles$Profile$Attributes
│   ├── name        ← profiles.name
│   ├── remoteURL   ← profiles.remoteURL
│   ├── updateExpr  ← profiles.updateExpr
│   └── updatedAt   ← profiles.updatedAt
└── userInfo        → Profiles$Profile$UserInfo
    ├── upload      ← user_infos.uploaded      ← 注意：列名多一个 ed
    ├── download    ← user_infos.downloaded    ← 注意：列名多一个 ed
    ├── total       ← user_infos.total
    └── expireAt    ← user_infos.expireAt
```

`GET /profiles` 的响应是 **包装体** `{"profiles":[...]}`（`Profiles$Profile$All` 只有一个字段 `profiles: List`）。

## 3. 陷阱清单

### 3.1 命名差异（最容易错）
| API JSON 键 | SQLite 列 |
|---|---|
| `upload` | `uploaded` |
| `download` | `downloaded` |

少一个 `ed` 就是空值，而且不会报错——`SELECT uploaded` 成功，只是映射时取不到。

### 3.2 三列不暴露给 API，但不可丢
- `profiles.version` INTEGER —— 订阅版本号，用于判断是否需要更新
- `profiles.content` BLOB —— **配置原文**，mihomo 的实际输入就是它
- `profiles.modeOverride` VARCHAR(32) —— 该 profile 覆盖的 mode（可能是 `rule`/`global`/`direct`/`script` 的小写形式）

`modeOverride` 值得注意：它可能是**小写**。我们已确认 `General.Config.Mode` 的线上值是 `direct/global/rule/script`（唯一带 `@SerialName` 的名字数组），这列大概率和它同源。

### 3.3 `providers.content` 是缓存，不是配置
`providers` 表存的是每个 profile 对应的 provider **已下载内容**（代理集合 / 规则集）。`updatedAt` 是下载时刻。

这解释了 `/providers/*` 端点的 `updatedAt` 该从哪来：**如果 profile 已加载，优先读这张表**，而不是 engine 里"版本号变了就刷 now"的近似值。这也回答了我上一轮否掉"报启动时刻"的理由。

### 3.4 UUID 的存储类型（已实测，两种都存在）

DDL 写的是 `id UUID`，但 **SQLite 没有 UUID 类型**，会落到 NUMERIC 亲和性。

**实测结论（engine-dev 用合成库验证）**：两种形态都可能出现——36 字符带横线的 TEXT，以及 **16 字节 BLOB**。读取时必须两种都处理：

```go
switch v := raw.(type) {
case []byte:
    if len(v) == 16 { id = uuid16ToText(v) } else { id = string(v) }
case string:
    id = v
}
```

### 3.5 `TIMESTAMP` 列会被 driver 直接转成 `time.Time`（已实测）

这是**实际测试中发现、静态分析看不出来的**：`modernc.org/sqlite` 对声明为 `TIMESTAMP` 的列会做类型转换，`Scan(&any)` 拿到的直接就是 `time.Time`，不是字符串也不是 int64。

一开始只处理 string/int64，结果 `updatedAt` 全是零值。`decodeTime` 必须覆盖：`time.Time` / int64 / float64 / string / []byte` 五种。

**注意**：如果换 driver（比如以后改用 `mattn/go-sqlite3`），这条不成立——类型转换行为是 driver 决定的，不是 SQLite 决定的。

### 3.6 旧版的类型假设

读的时候不要假设，建议：
```go
var raw any
_ = rows.Scan(&raw)
switch v := raw.(type) {
case string:  id = v
case []byte:  id = uuidFromBytes(v)  // 可能是 16 字节二进制，也可能是 []byte("...")
}
```

### 3.5 legacy 迁移表 `imported`
存在一条 `SELECT uuid, name, type, source, interval FROM imported`。这是 2.1x/2.3x 时代的旧表，对应 Go 侧的 `store.migrateFrom21x` / `migrateFrom23x`。

**新内核不需要实现迁移**——原版已经迁移过，用户库里不会再有 `imported`。但读 `profiles` 前最好先确认 `sqlite_master` 里有那张表，避免旧库直接崩。

## 4. 其它已知事实

- 表名全小写复数：`preferences` / `profiles` / `user_infos` / `providers` / `proxy_selections`
- 外键全带 `ON DELETE CASCADE ON UPDATE CASCADE`，删除 profile 会级联清 user_infos / providers / proxy_selections
- `proxy_selections` 是「每个 profile 下每个 group 选了哪个节点」，对应 `PUT /proxies/{name}` 的持久化。**P1c 之外也要用**：selector 切换后要落这张表，否则重启丢选择。
- `preferences` 是通用 KV（`key VARCHAR(128) PRIMARY KEY, value BLOB`），具体有哪些 key 没有从 DDL 看出来，需要读现有库的 `SELECT key FROM preferences` 才知道。

## 5. 对 P1c 的实现建议

1. **只能用纯 Go 的 `modernc.org/sqlite`**（`mattn/go-sqlite3` 需要 CGO，而 `CGO_ENABLED=0` 是硬约束）。
   **版本也被工具链卡死：只能用 `v1.38.2`**。更新的版本 `go` directive 超出我们的 go1.23.4：
   ```
   v1.61.0  go 1.26.0
   v1.50.0  go 1.25.0
   v1.38.2  go 1.23.0   ← 可用上限
   ```
   将来升 sqlite 必须先升 Go 工具链。
2. 打开时加 `?_pragma=foreign_keys(1)`，否则 CASCADE 不生效。
3. **只读优先**：P1c 第一阶段先实现 `GET /profiles`（只读），写操作（PUT/DELETE）等只读验证过再做。读错了顶多显示异常，写错了会毁用户的配置。
4. 打开前对 `store.db` 做一次 `PRAGMA integrity_check` 的成本太高，跳过；但要用 `mode=ro` 打开只读阶段，物理上防止误写。
