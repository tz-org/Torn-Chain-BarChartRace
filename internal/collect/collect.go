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
}

// Collector 把「网络 + 缓存」组合成单一的取数入口。
type Collector struct {
	Client  *torn.Client
	Cache   *Cache
	Refresh bool // 忽略已有缓存，强制重新抓取
	Offline bool // 只用缓存，绝不发请求
	Log     func(format string, args ...any)
}

func (c *Collector) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
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
	StopWhenPageOlderThan int64
	// MaxPages 是页数上限，防止异常情况把配额打空。
	MaxPages int
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
func (c *Collector) attacksFromCache(opt AttacksOptions, manifest attacksManifest) (*AttacksResult, error) {
	got, ok := c.cachedManifest(opt.ChainID, manifest)
	if !ok {
		return nil, fmt.Errorf("离线模式下没有可用的攻击记录缓存（%s）", c.Cache.attacksDir(opt.ChainID))
	}

	res := &AttacksResult{FromCache: true, Partial: !got.Complete}
	seen := make(map[int64]struct{}, 1024)
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
	if !got.Complete {
		res.Warnings = append(res.Warnings,
			"缓存标记为「未抓完」，数据可能不完整；建议去掉 --offline 重新抓取")
	}
	sortAttacks(res.Attacks)
	return res, nil
}

// attacksFromNetwork 在线翻页，并顺带把每页原始响应落盘。
func (c *Collector) attacksFromNetwork(ctx context.Context, opt AttacksOptions, manifest attacksManifest) (*AttacksResult, error) {
	res := &AttacksResult{}
	seen := make(map[int64]struct{}, 1024)

	// 先看缓存是否已经完整抓过同一查询条件 —— 命中则零配额返回。
	if !c.Refresh {
		if got, ok := c.cachedManifest(opt.ChainID, manifest); ok && got.Complete {
			if cached, err := c.attacksFromCache(opt, manifest); err == nil {
				c.logf("攻击记录：命中完整缓存（%d 页 / %d 条）", cached.Pages, len(cached.Attacks))
				return cached, nil
			}
		}
	}

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

		// ⚠️ 原样使用服务端给的 prev 链接，**不要自行改写其中的 to**。
		//
		// 曾经尝试过「把 to 再往前推 60 秒以制造更大重叠」，实测反而更糟：
		// 同一条 chain 从 101,399 条掉到 97,415 条（少 4,000 条）。
		// 说明 to 不是纯粹的「时间上界」，而是一个内部游标 ——
		// 改动它会整体平移游标，把整整一页的数据跳过去。
		prevURL = parsed.PrevLink
	}

	if !manifest.Complete {
		res.Partial = true
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"翻页达到上限 %d 页仍未翻完（已收 %d 条）：数据可能不完整", opt.MaxPages, len(res.Attacks)))
		manifest.Pages = opt.MaxPages
	}

	manifest.FetchedAt = time.Now()
	if raw, err := json.MarshalIndent(manifest, "", "  "); err == nil {
		if werr := c.Cache.Write(c.Cache.attacksManifestPath(opt.ChainID), raw); werr != nil {
			c.logf("⚠️ 写入缓存清单失败：%v", werr)
		}
	}

	sortAttacks(res.Attacks)
	return res, nil
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
	for _, a := range page.Attacks {
		if _, dup := seen[a.ID]; dup {
			continue
		}
		seen[a.ID] = struct{}{}
		res.Attacks = append(res.Attacks, a)
	}
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
