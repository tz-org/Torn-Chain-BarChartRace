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

## 2. 安装

### 2.1 下载预编译二进制（推荐）

从 [Releases](../../releases/latest) 下载对应平台的产物，解压后直接运行：

| 平台 | 文件 |
| --- | --- |
| Windows x64 | `chainrace_<version>_windows_amd64.exe` |
| Linux x64 / arm64 | `chainrace_<version>_linux_amd64` / `_arm64` |
| macOS Intel / Apple Silicon | `chainrace_<version>_darwin_amd64` / `_arm64` |

每个 Release 附带 `SHA256SUMS.txt`，可用 `sha256sum -c SHA256SUMS.txt` 校验完整性。

### 2.2 从源码构建

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
./chainrace 1234567 -dump-attacks          # 额外导出窗口内逐条攻击记录
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
| `-dump-attacks` | `false` | 额外导出窗口内逐条攻击记录（核对与留档用） |
| `-slices` | `0` | 并发取数分片数：`0` 自动（≥8h 用 4、≥2h 用 2），`1` 强制顺序翻页 |

### 3.4 环境变量

```bash
export TORN_API_KEY=xxxxxxxxxxxxxxxx   # 优先级高于配置文件，且不落盘
```

## 4. 输出

```
out/chain-<id>-respect.csv     累计 respect
out/chain-<id>-hits.csv        累计成功攻击数
out/chain-<id>-validation.csv  逐人校验明细（我方 vs 官方 chainreport）
out/chain-<id>-attacks.csv     窗口内逐条攻击记录（仅 -dump-attacks 时生成）
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

### 逐条攻击记录（`-dump-attacks`）

加上 `-dump-attacks` 会额外写出 `out/chain-<id>-attacks.csv`：**统计实际用到的全部攻击**，
一条一行。它不是 Flourish 的数据源，而是核对与留档用的原始凭据 —— 汇总数字只能证明
「总量看起来对」，逐条记录才能回答「某一次攻击到底算没算进去」。

```csv
AttackID,Code,AttackerID,Name,Started,Ended,Result,RespectGain,Counted
123456789,a1b2c3,2776069,SoftRabbit,2026-10-25T20:54:50+08:00,2026-10-25T20:54:53+08:00,Attacked,2.27,true
```

| 列 | 含义 |
| --- | --- |
| `AttackID` | Torn 攻击 id，可与游戏内的战斗记录逐条对照 |
| `Code` | 战斗 code |
| `AttackerID` | 发起者用户 id |
| `Name` | 参与者显示名；查不到时**留空**，不编造 `User<id>` —— 在核对用的文件里，假名字比空值更容易被误读 |
| `Started` / `Ended` | 发起 / 结束时刻，RFC3339 带时区偏移，跨时区核对无歧义 |
| `Result` | 结算结果，原样保留（含 `Lost` / `Stalemate` / `Escape` 等） |
| `RespectGain` | 该次攻击的 respect，原样精度不做四舍五入 |
| `Counted` | 是否计入统计：窗口相交 + 本帮派 + 结算成功，三者同时满足才为 `true` |

口径与图表完全一致，因此有三条可对账关系：

| 关系 | 说明 |
| --- | --- |
| 行数 = 收尾汇总的「我方发起攻击」总数 | 含打输、僵持等未计入的攻击 |
| `Counted=true` 的行数 = 成功攻击数 = chain 长度 | 这张表能自证统计没有漏算或多算 |
| `Counted` 行的 `RespectGain` 合计 = 成功攻击 respect 合计 | 浮点差异在 0.01 以内 |

导出范围是「与 chain 窗口**区间相交**」的攻击，不是「时刻落在窗口内」—— 首击的
`started` 会早于 `chain.start`，末击的 `ended` 会晚于 `chain.end`（见 7.3）。
为避开服务端游标丢数而外扩的 ±10 分钟余量里的相邻 chain 攻击不会写入本文件。

同一攻击 id 只保留一行：跨页边界重复是服务端分页的正常现象，重复行会让「行数 = 攻击总数」失效。
行按 `Started` 升序、同一秒内按 `AttackID` 升序，同一份缓存任意次运行输出逐字节相同。

## 5. 在 Flourish 里出图

1. 打开 <https://app.flourish.studio/> → **New** → **Bar chart race**；
2. **Data** 面板里上传（或粘贴）生成的 CSV；
3. Flourish 会自动把第 0 列绑定为 `label`，其余列识别为时间阶段；
4. 在 **Preview** 里调整柱子数量、配色、动画时长与时间轴标签格式；
5. 需要视频时用 **Export** 导出 MP4。

## 6. 配额与缓存

Torn 的配额是 **100 次请求/分钟**（本工具默认压到 90，留安全余量）。
一条 10 万连击的 chain 大约需要 100 页攻击记录（每页 1000 条）。

配额不是唯一的限制。实测（本机 → `api.torn.com`）：

| 指标 | 实测值 |
| --- | --- |
| `key/info` 单请求 RTT | 341 ms |
| `attacksfull` 单页 RTT（1000 条） | 1.2 – 1.5 s |
| 90/min 限流的理论上限 | 90 次/分钟（68.7 s / 103 页） |
| 顺序翻页实际速率 | **50 次/分钟**（102 页 / 121.4 s） |
| 4 路分片并发实际速率 | **76 次/分钟**（103 页 / 81.2 s） |

顺序翻页跑不满配额：每一页都要等上一页返回（`prev` 游标），而单页要 1.2–1.5 s，
已经超过限流器 666 ms 的最小间隔。把时间窗切开、各跑一条独立游标链并发取数，
才能把在途请求填满 —— 原理与验证见 [7.6](#76-并发取数分片而不是多线程翻页)。

每个请求都带 `comment=Chain-BarChartRace`（跟随 `prev` 链接时也会补上），
可在 Torn 的 API 日志里把本工具的调用与其他脚本区分开。

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

补充一条后来才量准的细节：`to` 过滤的是 **`ended`**，且为闭区间 ——
与 `chainreport` 用的是同一口径。把它设成 `chain.end` 会丢掉 `ended` 比它晚 1 秒的末击，
正好解释了上表的差值。这条规则同时是 7.6 分片并发成立的前提：
**在已有游标链上平移 `to` 会跳页，但从任意 `to` 新建一条链是精确的。**

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

### 7.5 指标口径

- **respect**：累计 `respect_gain` 之和（仅成功攻击）。链上 bonus 命中的 respect
  **已包含在** `respect_gain` 里（实测 13 个 bonus 中 12 个精确匹配），不需要额外补算。
- **hits**：成功攻击次数，判据见上文「只统计成功的攻击」。

### 7.6 并发取数：分片，而不是「多线程翻页」

最直觉的异步方案 —— 同时请求第 2、3、4 页 —— **不可行**：第 N+1 页的 URL
来自第 N 页响应里的 `prev`，天然是一条串行链。

但游标本身是时间戳。逐页比对 102 个缓存页后得到确定规则：

> `prev` 里的 `to` **恒等于当页最小的 `ended`**，`from` 保持不变。
> 即：返回「`ended <= to` 的最新 1000 条」。

于是可以在窗口中间**新建**一条游标链，起点是一个自定义的 `to`。这条性质已用完整缓存
逐 id 验证：任取 `to` 请求一次，返回集合与串行翻到同一位置完全一致（两个种子各 1000 条，
缺失 0、多余 0）。

实现要点：

| 项 | 做法 |
| --- | --- |
| 分片数 | 窗口 ≥ 8h → 4 片；≥ 2h → 2 片；更短仍走顺序翻页（短链开分片反而多花请求）。`-slices` 可覆盖，`1` 强制串行 |
| 区间 | 第 i 片只负责 `(seeds[i+1], seeds[i]]`，下界用 `ended` 判定 |
| 边界 | 相邻片**重叠**而不是首尾相接：`to` 是闭区间，边界那条会被两片都取到，由 id 去重吸收 —— 宁可重复，不能留缝 |
| 收工 | 当页最小 `ended <= 本片下界` 即停，不再多翻一页 |
| 缓存 | `attacks/s<种子>-p<页码>.json`（用种子而不是分片序号命名，换 `-slices` 也不会串页）；清单记录 `layout` / `slices` / `seeds` / `slice_pages`。旧布局 `page-%04d.json` 继续可读，升级不会让已花掉的配额作废 |
| 失败 | 任一分片出错立刻取消其余分片，不继续烧配额 |

实测（同机同网络、同一 chain 72069198，10 万连击）：

| 取数方式 | 页数 | 墙钟时间 | 速率 |
| --- | --- | --- | --- |
| 顺序翻页（`-slices 1`） | 102 | 121.4 s | 50 次/分钟 |
| 4 路分片并发（默认） | 103 | 81.2 s | 76 次/分钟 |

三条取数路径 —— 旧缓存顺序、新缓存顺序、新缓存并发 —— 产出的
`respect` / `hits` / `validation` / `attacks` 四个 CSV 的 sha256 **完全一致**。
分片只改变「怎么取」，不改变「取什么」；逐人校验仍然对照官方 chainreport，
真丢数会以攻击数不一致的形式暴露，而不是悄悄画出一张错的图。

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
internal/attacklog/     逐条攻击记录导出（核对与留档，非 Flourish 数据源）
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

分桶口径与 CSV 输出都有单元测试覆盖（`internal/chain`、`internal/flourish`、`internal/attacklog`、
`internal/collect`），
命令行参数重排有 `main_test.go` 覆盖。

## 11. 发布

发布由 GitHub Actions 自动完成，见 [`.github/workflows/release.yml`](.github/workflows/release.yml)。

```bash
git tag v0.0.1
git push origin v0.0.1
```

推送 `v*` tag 后流水线会依次执行：

1. `go test ./...` —— 测试不过则终止，不产出 Release；
2. 交叉编译 5 个平台（windows/linux/darwin × amd64/arm64），版本号从 tag 名注入；
3. 生成 `SHA256SUMS.txt`；
4. 用 `GITHUB_TOKEN` 创建 Release 并上传全部产物，Release notes 由 `--generate-notes` 依据提交历史自动生成。

构建带 `-buildvcs=false`。这是刻意的：默认会把 `vcs.revision`、`vcs.time` 和模块伪版本编进二进制，
导致同一份源码在不同 checkout 下产出不同哈希，`SHA256SUMS.txt` 便失去可验证性。
关掉之后，本地用同样参数重建可以逐字节复现发布产物：

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOFLAGS=-trimpath \
  go build -buildvcs=false -ldflags "-s -w -X main.version=0.0.1" .
```

对已存在的 tag 需要重跑时，可在 Actions 页面用 **Run workflow** 手工触发并填入 tag 名；
若 Release 已存在，会走 `gh release upload --clobber` 覆盖资产而不是报错。
