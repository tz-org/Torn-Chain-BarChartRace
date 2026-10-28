// Package collect 负责「把数据取到手」，并且把每一次成功响应原样落到磁盘缓存。
//
// 为什么必须有缓存：Torn 的配额是 100 次/分钟，而一条 10 万连击的 chain
// 需要翻上百页攻击记录。一旦解析口径要调整（比如换个统计粒度、换指标），
// 没有缓存就得把整套数据重抓一遍。raw-first 让「改口径」变成零配额操作。
package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"Torn-Chain-BarChartRace/internal/torn"
)

// Cache 是一个以 chain 为单位的原始响应缓存目录。
//
// 目录结构：
//
//	<root>/<chainID>/chainreport.json
//	<root>/<chainID>/members-<factionID>.json
//	<root>/<chainID>/attacks/manifest.json
//	<root>/<chainID>/attacks/page-0001.json
//	...
type Cache struct {
	Root string
}

// DefaultRoot 是缓存目录的默认位置（相对当前工作目录，已列入 .gitignore）。
const DefaultRoot = ".chainrace-cache"

func (c *Cache) chainDir(chainID int64) string {
	return filepath.Join(c.Root, strconv.FormatInt(chainID, 10))
}

func (c *Cache) chainReportPath(chainID int64) string {
	return filepath.Join(c.chainDir(chainID), "chainreport.json")
}

func (c *Cache) membersPath(chainID, factionID int64) string {
	return filepath.Join(c.chainDir(chainID), fmt.Sprintf("members-%d.json", factionID))
}

func (c *Cache) attacksDir(chainID int64) string {
	return filepath.Join(c.chainDir(chainID), "attacks")
}

func (c *Cache) attacksPagePath(chainID int64, page int) string {
	return filepath.Join(c.attacksDir(chainID), fmt.Sprintf("page-%04d.json", page))
}

// attacksSlicePagePath 是分片并发布局下某一分片某一页的缓存路径。
//
// 文件名里带的是**种子时间戳**而不是分片序号：分片序号只在某一次分片方案内
// 有意义，用户改过 -slices 之后同序号代表的时间区间完全不同。用序号命名会让
// 「上一次跑到一半的 2 分片缓存」被「这一次的 4 分片」误读，拼出一份有缺口的数据。
func (c *Cache) attacksSlicePagePath(chainID, seed int64, page int) string {
	return filepath.Join(c.attacksDir(chainID), fmt.Sprintf("s%012d-p%04d.json", seed, page))
}

func (c *Cache) attacksManifestPath(chainID int64) string {
	return filepath.Join(c.attacksDir(chainID), "manifest.json")
}

// Read 读取缓存文件；不存在或读失败时返回 ok=false（缓存问题绝不该中断流程）。
func (c *Cache) Read(path string) ([]byte, bool) {
	if c == nil || c.Root == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// Write 原子写入缓存文件（先写临时文件再改名，避免中断留下半个 JSON）。
func (c *Cache) Write(path string, data []byte) error {
	if c == nil || c.Root == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建缓存目录失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入缓存临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交缓存文件失败: %w", err)
	}
	return nil
}

// attacksManifest 记录这批攻击分页是「用什么查询条件」抓的。
//
// 没有它就会出现一类极难发现的错误：用户换了 chain 时间窗（或换了统计口径
// 导致窗口变化）后，旧分页被当作新数据复用，图表看起来完全正常但数字是错的。
// 因此每次读缓存前都要校验查询条件，不匹配就当缓存不存在。
type attacksManifest struct {
	ChainID   int64     `json:"chain_id"`
	From      int64     `json:"from"`
	To        int64     `json:"to"`
	Filters   string    `json:"filters"`
	Limit     int       `json:"limit"`
	Pages     int       `json:"pages"`
	Complete  bool      `json:"complete"`
	FetchedAt time.Time `json:"fetched_at"`

	// Layout 为空表示旧版「单游标串行」布局（attacks/page-0001.json）。
	// 为 "sliced" 时是分片并发布局（attacks/s0001-p0001.json）。
	// 保留该字段是为了让旧缓存继续可读 —— 升级工具不该让已花掉的配额作废。
	Layout string `json:"layout,omitempty"`
	Slices int    `json:"slices,omitempty"`
	// Seeds 是各分片的 to 上界，严格降序，首项等于主查询的 to。
	Seeds []int64 `json:"seeds,omitempty"`
	// SlicePages 是各分片实际落盘的页数，离线重放时用它发现缺页。
	SlicePages []int `json:"slice_pages,omitempty"`
}

// 缓存布局标识。
const (
	layoutSerial = ""       // 旧版：单游标顺序翻页
	layoutSliced = "sliced" // 分片并发：每个分片一条独立游标链
)

// 分片并发参数。
const (
	// maxSlices 是分片数上限。
	//
	// 上限取 4 而不是更大：实测单页 attacksfull 的 RTT 约 1.5s，
	// 而限流器要求每次请求间隔 666ms（90/min）。要让配额跑满，
	// 在途请求需要约 1.5s/666ms ≈ 2.3 个，4 个已足够覆盖抖动，
	// 再多只会在限流器上排队，不会更快。
	maxSlices = 4
	// sliceWindowThreshold 是启用分片的最小时间窗。
	//
	// 窗口过短时（短链通常一页就取完）开分片反而更慢：
	// 每多一个分片就多一次首屏请求，而它们仍受 666ms 的最小间隔约束。
	sliceWindowThreshold = 2 * time.Hour
)

// slicesForWindow 按时间窗决定分片数。
//
// 判据刻意只依赖窗口长度而不看攻击密度：密度要抓过才知道，
// 而抓过之后缓存已经就位，再改方案没有意义。
// 代价是「长但稀疏」的 chain 会多发几个请求（每个约 666ms），
// 换来的收益是「长且密集」的 chain 快一倍以上。
func slicesForWindow(from, to int64) int {
	if from <= 0 || to <= 0 || to <= from {
		return 1
	}
	switch d := time.Duration(to-from) * time.Second; {
	case d >= 8*time.Hour:
		return maxSlices
	case d >= sliceWindowThreshold:
		return 2
	default:
		return 1
	}
}

// sliceSeeds 返回各分片的 to 上界：严格降序，首项 == to，末项仍高于 from。
//
// 分片 i 负责区间 (seeds[i+1], seeds[i]]（最后一片下界为 from）。
// 相邻分片在边界上**重叠**而不是首尾相接：边界那条记录的 ended 可能等于种子值，
// 服务端的 to 是闭区间，两个分片都会取到它 —— 由去重吸收，宁可重复也不能漏。
func sliceSeeds(from, to int64, n int) []int64 {
	if n <= 1 {
		return []int64{to}
	}
	span := to - from
	seeds := make([]int64, n)
	for i := range seeds {
		seeds[i] = to - span*int64(i)/int64(n)
	}
	return seeds
}

// Collector 把「网络 + 缓存」组合成单一的取数入口。
//
// 分片并发时会被多个 goroutine 同时调用，因此日志必须串行化：
// 否则多个分片的进度行会互相插进对方中间，输出不可读。
type Collector struct {
	Client  *torn.Client
	Cache   *Cache
	Refresh bool // 忽略已有缓存，强制重新抓取
	Offline bool // 只用缓存，绝不发请求
	Log     func(format string, args ...any)

	logMu sync.Mutex
}

func (c *Collector) logf(format string, args ...any) {
	if c.Log == nil {
		return
	}
	c.logMu.Lock()
	defer c.logMu.Unlock()
	c.Log(format, args...)
}

// ChainReport 取 chain 报告（优先缓存）。
func (c *Collector) ChainReport(ctx context.Context, chainID int64) (*torn.ChainReport, bool, error) {
	path := c.Cache.chainReportPath(chainID)

	if !c.Refresh {
		if raw, ok := c.Cache.Read(path); ok {
			if rep, err := torn.DecodeChainReport(raw, time.Now()); err == nil {
				c.logf("chain 报告：命中缓存")
				return rep, true, nil
			}
			// 缓存解不开（可能是旧版本写的）→ 当作未命中，重新抓。
			c.logf("chain 报告：缓存无法解析，改为重新抓取")
		}
	}
	if c.Offline {
		return nil, false, fmt.Errorf("离线模式下没有可用的 chain 报告缓存（%s）", path)
	}

	rep, raw, err := c.Client.ChainReport(ctx, chainID)
	if err != nil {
		return nil, false, err
	}
	if err := c.Cache.Write(path, raw); err != nil {
		c.logf("⚠️ 写入 chain 报告缓存失败：%v", err)
	}
	return rep, false, nil
}

// Members 取帮派成员名字映射（优先缓存）。
//
// 名字只用于展示，取不到不应中断流程 —— 失败时返回空映射 + 错误，
// 由调用方降级成 "User<id>"。
func (c *Collector) Members(ctx context.Context, chainID, factionID int64) (map[int64]string, error) {
	path := c.Cache.membersPath(chainID, factionID)

	if !c.Refresh {
		if raw, ok := c.Cache.Read(path); ok {
			if m, err := torn.DecodeMembers(raw); err == nil {
				c.logf("成员名单：命中缓存（%d 人）", len(m))
				return m, nil
			}
		}
	}
	if c.Offline {
		return nil, fmt.Errorf("离线模式下没有可用的成员名单缓存（%s）", path)
	}

	m, raw, err := c.Client.Members(ctx, factionID)
	if err != nil {
		return nil, err
	}
	if err := c.Cache.Write(path, raw); err != nil {
		c.logf("⚠️ 写入成员名单缓存失败：%v", err)
	}
	c.logf("成员名单：已抓取（%d 人）", len(m))
	return m, nil
}

// AttacksResult 是翻页取数后的完整结果。
type AttacksResult struct {
	Attacks []torn.Attack
	Pages   int
	// Slices 是本次取数用的并行分片数；1 表示单游标顺序翻页。
	Slices int
	// FromCache 表示全部数据来自磁盘缓存（本次没有消耗配额）。
	FromCache bool
	// Partial 表示翻页没有走到底（撞上页数上限或缓存不完整）。
	Partial bool
	// Warnings 是翻页过程中发现的异常，调用方必须原样展示。
	Warnings []string
}

// QueryMargin 是查询窗口两端必须额外放宽的安全余量。
//
// ⚠️ 这是本工具最关键的一条经验性结论（2026-10-28 实测确证）：
//
//	`/faction/attacksfull` 的 from / to **不是**严格的「started 落在区间内」过滤，
//	而是服务端内部游标。把 to 恰好设成 chain 的 end，会**静默丢掉边界附近的记录**。
//
//	实测某 chain（官方 100000 连击 / 324506.28 respect）：to = chain.end 时只拿到
//	99998 次命中、283544.08 respect，少了一条 40960 respect 的收尾 bonus 命中；
//	把 to 放宽 300 秒后，同样的查询立刻把那条记录带回来了。
//
//	更迷惑的是 from 也不严格：实测有一条 started 比 from 早 3 秒的攻击照样被返回。
//
// 因此策略是「**宽进严出**」：查询窗口两端各放宽 QueryMargin，
// 再由调用方按 chain 的真实 start/end 做精确的客户端过滤。
// 放宽只会多拿几条相邻 chain 的攻击（随后被过滤掉），代价可以忽略；
// 收紧则会静默丢数据 —— 而丢了数据的图表看起来完全正常。
const QueryMargin = 10 * time.Minute

// AttacksOptions 控制翻页行为。
type AttacksOptions struct {
	ChainID int64
	Query   torn.AttacksQuery
	// StopWhenPageOlderThan 是翻页停止阈值：当某一页**最新**的一条都早于该时间戳时，
	// 说明整页及其后所有页都已经落在窗口之外，可以收工。
	//
	// 用「整页最新」而不是「整页最老」作为判据，是因为游标语义下页内顺序不保证
	// 严格按 started 单调 —— 以「最老」为准可能在窗口内还有数据时提前收手。
	// 仅在顺序翻页时使用；分片模式改用「本片下界」判断。
	StopWhenPageOlderThan int64
	// MaxPages 是页数上限，防止异常情况把配额打空。
	MaxPages int
	// Slices 覆盖自动分片数：0 表示按窗口长度自动决定，1 表示强制串行。
	// 大于 maxSlices 时按 maxSlices 处理 —— 再多的并发只会在限流器上排队。
	Slices int
}

// DefaultMaxPages 是翻页上限。limit=1000 时 200 页 = 20 万条，
// 足以覆盖 Torn 最长的连击（10 万次命中）。
const DefaultMaxPages = 200

// Attacks 抓取整场 chain 内我方发起的攻击（优先缓存，支持离线重算）。
func (c *Collector) Attacks(ctx context.Context, opt AttacksOptions) (*AttacksResult, error) {
	if opt.MaxPages <= 0 {
		opt.MaxPages = DefaultMaxPages
	}
	if opt.Query.Limit <= 0 || opt.Query.Limit > torn.MaxAttacksFullLimit {
		opt.Query.Limit = torn.MaxAttacksFullLimit
	}

	manifest := attacksManifest{
		ChainID: opt.ChainID,
		From:    opt.Query.From,
		To:      opt.Query.To,
		Filters: strings.Join(opt.Query.Filters, ","),
		Limit:   opt.Query.Limit,
	}

	if c.Offline {
		return c.attacksFromCache(opt, manifest)
	}
	return c.attacksFromNetwork(ctx, opt, manifest)
}

// cachedManifest 读取并校验缓存清单；不匹配时返回 ok=false。
func (c *Collector) cachedManifest(chainID int64, want attacksManifest) (attacksManifest, bool) {
	raw, ok := c.Cache.Read(c.Cache.attacksManifestPath(chainID))
	if !ok {
		return attacksManifest{}, false
	}
	var got attacksManifest
	if err := json.Unmarshal(raw, &got); err != nil {
		return attacksManifest{}, false
	}
	if got.ChainID != want.ChainID || got.From != want.From || got.To != want.To ||
		got.Filters != want.Filters || got.Limit != want.Limit {
		c.logf("攻击记录：缓存清单与本次查询条件不符，将重新抓取")
		return attacksManifest{}, false
	}
	return got, true
}

// attacksFromCache 完全离线地读回已落盘的分页。
//
// 支持两种布局：旧版单游标（page-N）与分片并发（sN-pM）。
// 布局必须由清单里的 Layout 决定，不能靠「文件在不在」去猜 ——
// 猜错会静默地只读回一部分数据，而图表看起来毫无异常。
func (c *Collector) attacksFromCache(opt AttacksOptions, manifest attacksManifest) (*AttacksResult, error) {
	got, ok := c.cachedManifest(opt.ChainID, manifest)
	if !ok {
		return nil, fmt.Errorf("离线模式下没有可用的攻击记录缓存（%s）", c.Cache.attacksDir(opt.ChainID))
	}

	res := &AttacksResult{FromCache: true, Partial: !got.Complete, Slices: max(got.Slices, 1)}
	seen := make(map[int64]struct{}, 1024)

	if got.Layout == layoutSliced {
		if got.Slices <= 0 || len(got.SlicePages) != got.Slices || len(got.Seeds) != got.Slices {
			return nil, fmt.Errorf(
				"攻击记录缓存清单与本版本不兼容（slices=%d，页数=%d 项，种子=%d 项），请去掉 -offline 重新抓取",
				got.Slices, len(got.SlicePages), len(got.Seeds))
		}
		for slice := 0; slice < got.Slices; slice++ {
			for page := 1; page <= got.SlicePages[slice]; page++ {
				raw, ok := c.Cache.Read(c.Cache.attacksSlicePagePath(opt.ChainID, got.Seeds[slice], page))
				if !ok {
					res.Warnings = append(res.Warnings, fmt.Sprintf(
						"缓存缺少分片 %d/%d 的第 %d 页（共 %d 页），数据不完整",
						slice+1, got.Slices, page, got.SlicePages[slice]))
					res.Partial = true
					continue
				}
				parsed, err := torn.DecodeAttacksPage(raw)
				if err != nil {
					return nil, fmt.Errorf("解析缓存（分片 %d 第 %d 页）失败: %w", slice+1, page, err)
				}
				mergeAttacks(res, parsed, seen)
				res.Pages++
			}
		}
	} else {
		for page := 1; page <= got.Pages; page++ {
			raw, ok := c.Cache.Read(c.Cache.attacksPagePath(opt.ChainID, page))
			if !ok {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("缓存缺少第 %d 页（共 %d 页），数据不完整", page, got.Pages))
				res.Partial = true
				continue
			}
			parsed, err := torn.DecodeAttacksPage(raw)
			if err != nil {
				return nil, fmt.Errorf("解析缓存第 %d 页失败: %w", page, err)
			}
			mergeAttacks(res, parsed, seen)
			res.Pages++
		}
	}

	if !got.Complete {
		res.Warnings = append(res.Warnings,
			"缓存标记为「未抓完」，数据可能不完整；建议去掉 --offline 重新抓取")
	}
	sortAttacks(res.Attacks)
	return res, nil
}

// attacksFromNetwork 在线取数：先看缓存，再决定用串行还是分片并发。
func (c *Collector) attacksFromNetwork(ctx context.Context, opt AttacksOptions, manifest attacksManifest) (*AttacksResult, error) {
	// 先看缓存是否已经完整抓过同一查询条件 —— 命中则零配额返回。
	if !c.Refresh {
		if got, ok := c.cachedManifest(opt.ChainID, manifest); ok && got.Complete {
			if cached, err := c.attacksFromCache(opt, manifest); err == nil {
				c.logf("攻击记录：命中完整缓存（%d 页 / %d 条）", cached.Pages, len(cached.Attacks))
				return cached, nil
			}
		}
	}

	n := slicesForWindow(opt.Query.From, opt.Query.To)
	if opt.Slices > 0 {
		n = min(opt.Slices, maxSlices)
	}
	if n > 1 {
		return c.fetchSliced(ctx, opt, manifest, n)
	}
	return c.fetchSerial(ctx, opt, manifest)
}

// fetchSerial 是单游标顺序翻页，也是旧缓存的布局。
func (c *Collector) fetchSerial(ctx context.Context, opt AttacksOptions, manifest attacksManifest) (*AttacksResult, error) {
	res := &AttacksResult{Slices: 1}
	seen := make(map[int64]struct{}, 1024)

	var prevURL string
	for page := 1; page <= opt.MaxPages; page++ {
		var (
			parsed   *torn.AttacksPage
			raw      []byte
			err      error
			fromDisk bool
		)

		// 第 1 页与后续页都先试缓存：中断后续跑时能省下大量配额。
		path := c.Cache.attacksPagePath(opt.ChainID, page)
		if !c.Refresh {
			if b, ok := c.Cache.Read(path); ok {
				if p, derr := torn.DecodeAttacksPage(b); derr == nil {
					parsed, raw, fromDisk = p, b, true
				}
			}
		}
		if parsed == nil {
			if prevURL != "" {
				parsed, raw, err = c.Client.AttacksPageByURL(ctx, prevURL)
			} else {
				parsed, raw, err = c.Client.AttacksFirstPage(ctx, opt.Query)
			}
			if err != nil {
				return nil, fmt.Errorf("抓取攻击列表第 %d 页失败: %w", page, err)
			}
			if werr := c.Cache.Write(path, raw); werr != nil {
				c.logf("⚠️ 写入第 %d 页缓存失败：%v", page, werr)
			}
		} else {
			c.logf("第 %d 页：命中缓存", page)
		}

		mergeAttacks(res, parsed, seen)
		res.Pages = page
		c.logf("第 %d 页：%d 条（累计去重后 %d 条）%s", page, len(parsed.Attacks), len(res.Attacks), mark(fromDisk))

		if len(parsed.Attacks) == 0 {
			manifest.Pages = page
			manifest.Complete = true
			break
		}

		// 收工判据 1：服务端不再给 prev 链接 = 已经翻到最早一页。
		if !parsed.HasPrev {
			manifest.Pages = page
			manifest.Complete = true
			break
		}
		// 收工判据 2：整页都早于时间下限，说明已经翻过了 chain 的起点。
		if newest := newestStarted(parsed.Attacks); opt.StopWhenPageOlderThan > 0 && newest < opt.StopWhenPageOlderThan {
			manifest.Pages = page
			manifest.Complete = true
			break
		}

		// ⚠️ 原样使用服务端给的 prev 链接，**不要改写其中的 to**。
		//
		// 曾经尝试过「把 to 再往前推 60 秒以制造更大重叠」，实测反而更糟：
		// 同一条 chain 从 101,399 条掉到 97,415 条（少 4,000 条）。
		// 说明 to 不是纯粹的「时间上界」，而是一个内部游标 ——
		// 在已有游标链上平移它会整体跳页。
		//
		// 分片并发里的 to 是**新建**一条游标链的起点，性质不同：
		// 已用本地 102 页完整数据逐 id 验证过，种子 to 取任意时刻都能精确
		// 复现「ended <= to 的最新 1000 条」。
		prevURL = parsed.PrevLink
	}

	if !manifest.Complete {
		res.Partial = true
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"翻页达到上限 %d 页仍未翻完（已收 %d 条）：数据可能不完整", opt.MaxPages, len(res.Attacks)))
		manifest.Pages = opt.MaxPages
	}

	manifest.FetchedAt = time.Now()
	c.writeManifest(opt.ChainID, manifest)

	sortAttacks(res.Attacks)
	return res, nil
}

// sliceResult 是单个分片的取数结果。
type sliceResult struct {
	attacks  []torn.Attack
	pages    int
	partial  bool
	warnings []string
	err      error
}

// fetchSliced 把时间窗切成 n 段，每段一条独立游标链，并发取数。
//
// 为什么能并行：服务端每页由 `to`（= 上一页最小 ended）唯一确定，
// 因此「在窗口中间新建一条游标链」与「顺着 prev 翻到那里」的结果相同。
// 已用完整缓存逐 id 验证（见 README 7.5）。
//
// 各分片只负责自己的闭区间，边界记录会被相邻分片重复取到，由去重吸收 ——
// 宁可多取一条，也不能在边界上留缝。
func (c *Collector) fetchSliced(ctx context.Context, opt AttacksOptions, manifest attacksManifest, n int) (*AttacksResult, error) {
	seeds := sliceSeeds(opt.Query.From, opt.Query.To, n)
	// 页数上限按分片均摊，避免异常情况下把配额打空。
	perSliceCap := opt.MaxPages/n + 2

	out := make([]sliceResult, n)

	// 一个分片失败就取消其余分片：继续取只会白烧配额。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lower := opt.Query.From
			if i+1 < n {
				lower = seeds[i+1]
			}
			r := c.fetchSlice(runCtx, opt, i, n, seeds[i], lower, perSliceCap)
			out[i] = r
			if r.err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = r.err
				}
				errMu.Unlock()
				cancel()
			}
		}(i)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	res := &AttacksResult{Slices: n}
	seen := make(map[int64]struct{}, 1024)
	manifest.Layout = layoutSliced
	manifest.Slices = n
	manifest.Seeds = seeds
	manifest.SlicePages = make([]int, n)
	manifest.Complete = true

	for i := range out {
		mergeAttackList(res, out[i].attacks, seen)
		manifest.SlicePages[i] = out[i].pages
		res.Pages += out[i].pages
		if out[i].partial {
			manifest.Complete = false
			res.Partial = true
		}
		res.Warnings = append(res.Warnings, out[i].warnings...)
	}
	manifest.Pages = res.Pages
	manifest.FetchedAt = time.Now()

	c.logf("分片并发：%d 路并行 / 共 %d 页 / 去重后 %d 条", n, res.Pages, len(res.Attacks))
	c.writeManifest(opt.ChainID, manifest)

	sortAttacks(res.Attacks)
	return res, nil
}

// fetchSlice 取第 idx 个分片：从 to=upper 起顺着 prev 翻到 lower 以下为止。
func (c *Collector) fetchSlice(ctx context.Context, opt AttacksOptions, idx, total int, upper, lower int64,
	maxPages int,
) sliceResult {
	var out sliceResult
	q := opt.Query
	q.To = upper

	var prevURL string
	for page := 1; page <= maxPages; page++ {
		var (
			parsed   *torn.AttacksPage
			raw      []byte
			err      error
			fromDisk bool
		)

		path := c.Cache.attacksSlicePagePath(opt.ChainID, upper, page)
		if !c.Refresh {
			if b, ok := c.Cache.Read(path); ok {
				if p, derr := torn.DecodeAttacksPage(b); derr == nil {
					parsed, raw, fromDisk = p, b, true
				}
			}
		}
		if parsed == nil {
			if prevURL != "" {
				parsed, raw, err = c.Client.AttacksPageByURL(ctx, prevURL)
			} else {
				parsed, raw, err = c.Client.AttacksFirstPage(ctx, q)
			}
			if err != nil {
				out.err = fmt.Errorf("抓取第 %d/%d 分片第 %d 页失败: %w", idx+1, total, page, err)
				return out
			}
			if werr := c.Cache.Write(path, raw); werr != nil {
				c.logf("⚠️ 写入分片 %d/%d 第 %d 页缓存失败：%v", idx+1, total, page, werr)
			}
		} else {
			c.logf("分片 %d/%d · 第 %d 页：命中缓存", idx+1, total, page)
		}

		out.attacks = append(out.attacks, parsed.Attacks...)
		out.pages = page
		c.logf("分片 %d/%d · 第 %d 页：%d 条（本片累计 %d 条）%s",
			idx+1, total, page, len(parsed.Attacks), len(out.attacks), mark(fromDisk))

		// 本片已取完：空页，或服务端没有更早的页了。
		if len(parsed.Attacks) == 0 || !parsed.HasPrev {
			return out
		}
		// 本片已跨过自己的下界：再往后翻只会在下一片的区间里重复。
		// 用 <= 而不是 <：边界那条记录的 ended 恰好等于 lower 时，
		// 下一片（to=lower，闭区间）同样会取到它，不会因此留缝。
		if lower > 0 && oldestEnded(parsed.Attacks) <= lower {
			return out
		}

		prevURL = parsed.PrevLink
	}

	out.partial = true
	out.warnings = append(out.warnings, fmt.Sprintf(
		"第 %d/%d 分片达到页数上限 %d 仍未取完，数据可能不完整", idx+1, total, maxPages))
	return out
}

// writeManifest 落盘缓存清单；失败只记日志，不影响已经取到的数据。
func (c *Collector) writeManifest(chainID int64, manifest attacksManifest) {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return
	}
	if werr := c.Cache.Write(c.Cache.attacksManifestPath(chainID), raw); werr != nil {
		c.logf("⚠️ 写入缓存清单失败：%v", werr)
	}
}

func mark(fromDisk bool) string {
	if fromDisk {
		return " [缓存]"
	}
	return ""
}

// mergeAttacks 把一页并入结果，按攻击 id 去重。
//
// 跨页重复是服务端分页边界的正常现象（同一秒的多条攻击可能被切在两页），
// 去重是必需的，否则累计值会虚高。
func mergeAttacks(res *AttacksResult, page *torn.AttacksPage, seen map[int64]struct{}) {
	mergeAttackList(res, page.Attacks, seen)
}

// mergeAttackList 把一批攻击并入结果，按 id 去重（分片结果按片合并时复用）。
func mergeAttackList(res *AttacksResult, attacks []torn.Attack, seen map[int64]struct{}) {
	for _, a := range attacks {
		if _, dup := seen[a.ID]; dup {
			continue
		}
		seen[a.ID] = struct{}{}
		res.Attacks = append(res.Attacks, a)
	}
}

// oldestEnded 返回一页里最早的结束时间戳；ended 缺失时回退到 started。
//
// 用 ended 而不是 started 作分片边界：服务端游标的 to 就等于当页最小 ended
// （实测 page-0001 的 prev.to 与 min(ended) 完全相等），跟随同一个口径才不会错位。
func oldestEnded(attacks []torn.Attack) int64 {
	var oldest int64
	for _, a := range attacks {
		t := a.Ended
		if t.IsZero() || t.Before(a.Started) {
			t = a.Started
		}
		if ts := t.Unix(); oldest == 0 || ts < oldest {
			oldest = ts
		}
	}
	return oldest
}

// newestStarted 返回一页里最晚的开始时间戳；空页返回 0。
func newestStarted(attacks []torn.Attack) int64 {
	if len(attacks) == 0 {
		return 0
	}
	newest := attacks[0].Started.Unix()
	for _, a := range attacks[1:] {
		if ts := a.Started.Unix(); ts > newest {
			newest = ts
		}
	}
	return newest
}

// sortAttacks 按开始时间升序排列，保证多次运行输出完全一致。
func sortAttacks(attacks []torn.Attack) {
	sort.Slice(attacks, func(i, j int) bool {
		if !attacks[i].Started.Equal(attacks[j].Started) {
			return attacks[i].Started.Before(attacks[j].Started)
		}
		return attacks[i].ID < attacks[j].ID
	})
}
