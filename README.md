# Torn Chain → Flourish Bar Chart Race

把 Torn 帮派连击（chain）的攻击记录，导出成 [Flourish](https://flourish.studio)「Bar chart race」模板可直接读取的 CSV。

输入一个 Torn API key 和 chain id，工具会：

1. 自动读取该 chain 的**开始与结束时间**；
2. 抓取窗口内**我方发起的全部攻击**；
3. 按自定义间隔（默认 5 分钟）切分时间轴，计算每名成员的**累计值**；
4. 输出宽表 CSV —— 首列是参与者，其余每列是一个时间阶段。

然后手动把 CSV 上传到 Flourish 即可出图。

---

## 1. 环境要求

- Go 1.24 及以上（开发时使用 1.27）
- 一个 Torn API key，权限要求：

| 权限项 | 最低要求 | 用途 |
| --- | --- | --- |
| Access level | **Limited Access** | 访问帮派数据 |
| Faction → `Attacks` / `attacksfull` | 必需 | 逐条攻击记录（唯一带时间戳的数据源） |
| Faction → `Chain report` | 必需 | chain 的 start / end 与官方汇总 |
| Faction → `Members` | 建议 | 成员 id → 名字映射 |

> key 所属账号必须**在该帮派内**。`/faction/attacksfull` 只返回「自己帮派」的攻击，
> 拿别帮的 key 去查这条 chain 会得到 0 条记录。

## 2. 构建

```bash
git clone git@github.com:tz-org/Torn-Chain-BarChartRace.git
cd Torn-Chain-BarChartRace
go build -o chainrace .
```

## 3. 使用

### 3.1 交互式（推荐首次使用）

```bash
./chainrace
```

会依次询问：API key（输入不回显）→ chain id → 统计间隔 → 指标。
key 校验通过后会被记住，存于**当前工作目录**下的 `.chainrace/config.json`，下次不必重输。

> **该文件内含明文 API key**，已在 `.gitignore` 中忽略。不要把 `.chainrace/` 移出忽略列表，也不要提交它。
> 想让 key 完全不落盘：`export TORN_API_KEY=...` 或加 `-no-save`。

### 3.2 非交互式

```bash
./chainrace -chain 1234567 -interval 5m
```

或把 chain id 作为位置参数：

```bash
./chainrace 1234567 -interval 10m -metric respect
```

### 3.3 参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-api` | 空 | API key；也可用环境变量 `TORN_API_KEY` |
| `-chain` | 空 | chain id；省略则交互询问 |
| `-interval` | `5m` | 统计间隔，支持 `5m` / `300` / `90s` |
| `-metric` | `both` | `respect` / `hits` / `both` |
| `-out` | `out` | 输出目录 |
| `-time-format` | `datetime` | 时间列头格式：`datetime` / `clock` / `elapsed` / `index` |
| `-cache` | `.chainrace-cache` | 缓存目录 |
| `-refresh` | `false` | 忽略缓存，强制重新抓取 |
| `-offline` | `false` | 只用缓存，不发起任何请求 |
| `-resolve-names` | `true` | 成员名单查不到时逐个查询用户名 |
| `-rate` | `90` | 每分钟请求上限（Torn 官方上限 100） |
| `-no-save` | `false` | 不把 API key 写入配置文件 |

### 3.4 环境变量

```bash
export TORN_API_KEY=xxxxxxxxxxxxxxxx   # 优先级高于配置文件，且不落盘
```

## 4. 输出

```
out/chain-<id>-respect.csv     累计 respect
out/chain-<id>-hits.csv        累计成功攻击数
out/chain-<id>-validation.csv  逐人校验明细（我方 vs 官方 chainreport）
```

两份图表数据都是 **Flourish 宽表格式**：

```csv
Name,2026-10-25 18:00,2026-10-25 18:05,2026-10-25 18:10
evan1321,0.00,0.00,12.35
Megistus,0.00,4.10,9.80
```

- 第 1 列 `Name` 是参与者名字；
- 其余每列是一个时间阶段，列头即时间轴标签；
- 数值是**截至该时刻的累计值**，单调不减；
- 成员在首次命中之前的列填 `0`，让柱状图从 0 起跑而不是凭空出现；
- 行按最终值降序排列。

### 只统计成功的攻击

以下结算结果**不计入**次数与 respect：

| 结果 | 含义 |
| --- | --- |
| `Lost` | 打输 |
| `Stalemate` | 僵持 |
| `Timeout` | 超时未结束 |
| `Escape` | 目标逃跑 |
| `Assist` | 助攻 —— 给 respect 但不推进连击，也不算自己打中 |
| `Interrupted` | 被打断（目标先被别人打进医院等） |

其余结算（`Attacked` / `Mugged` / `Hospitalized` / `Arrested` / `Looted` / `Bounty` …）均计入。
实测这些被排除的结果 `respect_gain` 恒为 0，因此排除它们不影响 respect 总量。

### 时间列头格式

| `-time-format` | 示例 | 适用场景 |
| --- | --- | --- |
| `datetime`（默认） | `2026-10-25 18:05` | 无歧义，跨天 chain 必选 |
| `clock` | `18:05` | 单日内的短链，列头更短 |
| `elapsed` | `+1:05` | 强调「打到第几分钟」 |
| `index` | `T13` | 完全隐藏时间，纯阶段序号 |

## 5. 在 Flourish 里出图

1. 打开 <https://app.flourish.studio/> → **New** → **Bar chart race**；
2. **Data** 面板里上传（或粘贴）生成的 CSV；
3. Flourish 会自动把第 0 列绑定为 `label`，其余列识别为时间阶段；
4. 在 **Preview** 里调整柱子数量、配色、动画时长与时间轴标签格式；
5. 需要视频时用 **Export** 导出 MP4。

## 6. 配额与缓存

Torn 的配额是 **100 次请求/分钟**。一条 10 万连击的 chain 大约需要 100 页攻击记录
（每页 1000 条），即约 100 次请求，耗时 1–2 分钟。

每次成功响应都会原样落盘到 `.chainrace-cache/<chain-id>/`，因此：

- **重复运行零配额**：同样的 chain 与时间窗，第二次直接读缓存；
- **改口径零配额**：换间隔、换指标、换时间列头格式都不需要重新抓取；
- **可离线重算**：`-offline` 完全不联网。

想强制刷新：加 `-refresh`。想清空：删掉 `.chainrace-cache/` 即可。

## 7. 实现要点

### 7.1 为什么用 `attacksfull` 而不是 `attacks`

| 端点 | limit 上限 | 字段 |
| --- | --- | --- |
| `/faction/attacks` | 100 | 含 `modifiers`、`is_ranked_war`、完整 attacker 对象 |
| `/faction/attacksfull` | **1000** | 仅 `id/code/started/ended/result/respect_gain`，actor 是扁平 `{id, faction_id}` |

本工具只需要「谁、何时、拿到多少 respect」，精简版字段完全够用，
而 limit 是完整版的 10 倍 —— 配额消耗相差一个数量级。

### 7.2 ⚠️ `from` / `to` 不是严格时间过滤

这是实测确证的一条重要结论，也是本项目最容易踩的坑：

> `/faction/attacksfull` 的 `from` / `to` 是服务端**内部游标**，不是
> 「`started` 落在区间内」的过滤条件。把 `to` 恰好设成 chain 的 `end`，
> 会**静默丢掉边界附近的记录**。

实测某条 100,000 连击的 chain：

| 查询窗口 | 取到命中数 | 取到 respect |
| --- | --- | --- |
| `to = chain.end` | 99,998 | 283,544.08 |
| `to = chain.end + 300s` | 100,000 | 324,506.28 |
| 官方 chainreport | 100,000 | 324,506.28 |

丢失的是一条价值 40,960 respect 的收尾 bonus 命中 —— 占总量 12.6%。
图表照样能画出来，而且看起来毫无异常。

因此工具采取「**宽进严出**」：查询窗口两端各放宽 10 分钟
（`collect.QueryMargin`），再由客户端按 chain 的真实 `start`/`end` 精确过滤。

### 7.3 ⚠️ `chain.start` / `end` 与攻击时间戳不是同一个口径

第二处会静默丢数据的地方，比游标问题更隐蔽，而且**两端的偏差方向相反**。

`chainreport` 的时间戳记的是「命中」这一事件，而攻击记录里的
`started` / `ended` 是攻击自身的发起 / 结束时刻。实测（同一 chain）：

| 记录 | `started` | `ended` | 与 chain 边界的关系 |
| --- | --- | --- | --- |
| 首击 `526836524` | 1792922363 | **1792922366** | `chain.start` = 1792922366 → **等于首击的 ended**，比 started 晚 3 秒 |
| 末击 `538101353` | 1793135290 | **1793135316** | `chain.end` = 1793135315 → **比末击的 ended 早 1 秒** |

于是：

- 按 `started >= start` 过滤 → 丢掉**首击**（差 1 次攻击 / 2.27 respect）；
- 按 `ended <= end` 过滤 → 丢掉**末击**（差 1 次攻击 / 40,960 respect，占总量的 12.6%）。

两者都不能用。正确判据是**区间相交**：

```go
started <= end && ended >= start
```

它同时满足四个方向：上一场 chain 的攻击（`ended < start`）被排除、
下一场 chain 的攻击（`started > end`）被排除、恰好跨越边界的首击与末击被保留。
分桶时刻取 `ended`（连击在命中瞬间推进），并夹紧到窗口内，
避免跨越右边界的命中落到最后一列之外。实现见 `chain.Event.Overlaps` / `EffectiveAt`。

> 这两个偏差在图表上完全看不出来 —— 是逐人校验把它们揪出来的。
> 这也是「校验必须存在」的最好理由。

### 7.4 逐人校验（唯一能自证正确的手段）

每次运行结束都会把**我方统计**与**官方 chainreport** 逐人对照：

```
▸ 逐人校验（对比官方 chainreport）
  成功攻击数一致：100/100 人
  respect 一致：  100/100 人（容差 0.01）
  合计：成功攻击 100,000 / chain 长度 100,000（差 0）· respect 324,506.28 / 官方 324,506.28（差 0.00）
  ✓ 全部参与者的攻击数与 respect 均与官方一致
```

连击长度 N 就意味着 N 次命中，因此「成功攻击数」应当正好等于 `chain` 长度 ——
这是最硬的一条不变量。对不上时工具会逐人列出差异：

校验三个量：

| 校验项 | 基准 | 性质 |
| --- | --- | --- |
| 成功攻击数 | `chainreport.attackers[].attacks.total` | 整数，差 1 就是差 1 |
| respect | `chainreport.attackers[].respect.total` | 浮点，容差 0.01 |
| 最高单次 respect | `chainreport.attackers[].respect.best` | 浮点，用于定位异常大额记录 |

先比攻击数再比 respect：整数对平之后，浮点的差异才有解释意义。
完整明细落在 `out/chain-<id>-validation.csv`，可逐行核对。

这一步是必要的：抓取一旦出问题，图表照样画得出来、而且看起来毫无异常，
只有逐人的攻击数会对不上。

### 7.4 指标口径

- **respect**：累计 `respect_gain` 之和（仅成功攻击）。链上 bonus 命中的 respect
  **已包含在** `respect_gain` 里（实测 13 个 bonus 中 12 个精确匹配），不需要额外补算。
- **hits**：成功攻击次数，判据见上文「只统计成功的攻击」。

## 8. 排错

| 现象 | 原因与处理 |
| --- | --- |
| `key 缺少必需的 faction 权限` | 到 Torn 的 Preferences → API 为 key 勾选 Faction → Attacks / Chain report |
| 窗口内没有取到任何攻击 | chain 太旧（Torn 只保留有限历史）；或 key 不属于该帮派 |
| `respect 与官方口径相差 x%` | 加 `-refresh` 重抓；持续不一致请附 chain id 反馈 |
| 时间轴列数过多（>300） | 把 `-interval` 调大（如 `10m` / `15m`）压缩帧数 |
| `离线模式下没有可用的缓存` | 先联网跑一次，或用 `-offline` 前确认缓存目录存在 |

## 9. 目录结构

```
main.go                 CLI 入口与参数解析
internal/torn/          Torn API 客户端（限流、重试、错误码、wire 类型）
internal/collect/       抓取编排与 raw-first 磁盘缓存
internal/chain/         时间分桶与累计聚合（纯函数，无网络依赖）
internal/flourish/      Flourish 宽表 CSV 渲染
internal/cli/           交互提示、配置持久化、流程编排
```

### 9.1 配置文件位置

```
<当前工作目录>/.chainrace/config.json     ← API key、默认间隔/指标/输出目录
<当前工作目录>/.chainrace-cache/          ← 原始 API 响应缓存
<当前工作目录>/out/                       ← 生成的 CSV
```

三者都以**当前工作目录**为基准，跟着项目走。`.chainrace/` 与 `.chainrace-cache/`、`out/` 均已在 `.gitignore` 中忽略。

删除 `.chainrace/config.json` 即可让工具下次重新询问 key，不会报错。

## 10. 测试

```bash
go test ./...
```

分桶口径与 CSV 输出都有单元测试覆盖（`internal/chain`、`internal/flourish`）。
