package cli

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"Torn-Chain-BarChartRace/internal/attacklog"
	"Torn-Chain-BarChartRace/internal/chain"
	"Torn-Chain-BarChartRace/internal/collect"
	"Torn-Chain-BarChartRace/internal/flourish"
	"Torn-Chain-BarChartRace/internal/torn"
)

// Options 是命令行与交互层合并后的最终参数。
type Options struct {
	APIKey       string
	ChainID      int64
	Interval     time.Duration
	Metric       string // respect / hits / both
	OutDir       string
	TimeFormat   string
	CacheDir     string
	Refresh      bool // 忽略缓存强制重抓
	Offline      bool // 只用缓存
	ResolveNames bool
	RateLimit    int // 每分钟请求上限
	NoSave       bool
	// DumpAttacks 为真时额外导出窗口内逐条攻击记录，供核对与留档。
	DumpAttacks bool
	// Slices 覆盖并发取数分片数：0 自动，1 强制串行。
	Slices    int
	AssumeYes bool
}

// Defaults 是各参数的默认值。
const (
	DefaultInterval   = 5 * time.Minute
	DefaultMetric     = "both"
	DefaultOutDir     = "out"
	DefaultTimeFormat = "datetime"
	DefaultRateLimit  = 90 // Torn 官方上限为 100 次/分钟，留出安全余量
)

// maxNameLookups 是「成员名单里查不到时，逐个调 /user/{id}/basic 兜底取名」的上限。
//
// 设上限的理由：这些调用每个都要 1 次配额，而它们只影响展示。
// 一条 100 人参与的 chain 最多也就 100 次，设成 60 能在「名字好看」和
// 「别把配额烧在装饰性数据上」之间取平衡。
const maxNameLookups = 60

// Run 执行完整流程：解析参数 → 抓取 → 分桶 → 输出。
func Run(ctx context.Context, opt Options, in io.Reader, out io.Writer) error {
	p := NewPrompter(in, out)
	interactive := p.Interactive()

	cfg, cfgErr := LoadConfig()
	if cfgErr != nil {
		fmt.Fprintf(out, "⚠️ %v\n", cfgErr)
	}
	applyConfigDefaults(&opt, cfg)

	printBanner(out)

	// ── 1. API key ────────────────────────────────────────────────
	key, keySource, promptedKey, err := resolveAPIKey(ctx, p, opt, cfg, interactive, out)
	if err != nil {
		return err
	}
	if promptedKey && !opt.NoSave {
		cfg.APIKey = key
		if err := SaveConfig(cfg); err != nil {
			fmt.Fprintf(out, "⚠️ 保存配置失败（下次仍需重新输入）：%v\n", err)
		} else if path, perr := ConfigPath(); perr == nil {
			fmt.Fprintf(out, "  ✓ 已记住该 key（%s，仅当前用户可读；用 -no-save 可关闭）\n", path)
		}
	}
	fmt.Fprintf(out, "  key %s · 来源：%s\n", MaskKey(key), keySource)

	// ── 2. 客户端 ─────────────────────────────────────────────────
	limiter := torn.NewLimiter(opt.RateLimit, time.Minute, time.Minute/time.Duration(max(opt.RateLimit, 1)))
	client, err := torn.NewClient(torn.Options{APIKey: key, Limiter: limiter})
	if err != nil {
		return err
	}

	// ── 3. 权限预检 ───────────────────────────────────────────────
	if !opt.Offline {
		if err := preflight(ctx, client, out); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(out, "  ⓘ 离线模式：跳过权限预检，只读本地缓存")
	}

	// ── 4. chain id ───────────────────────────────────────────────
	chainID := opt.ChainID
	if chainID <= 0 {
		if !interactive {
			return errors.New("缺少 chain id：请用 -chain 指定，或在终端里交互输入")
		}
		raw, aerr := p.Ask("Chain ID", "", func(s string) error {
			_, verr := ParseChainID(s)
			return verr
		})
		if aerr != nil {
			return aerr
		}
		chainID, _ = ParseChainID(raw)
	}

	// ── 5. 统计间隔 ───────────────────────────────────────────────
	interval := opt.Interval
	if interval <= 0 {
		def := int(DefaultInterval.Seconds())
		if interactive {
			raw, aerr := p.Ask("统计间隔（如 5m / 300 / 90s）", strconv.Itoa(def), func(s string) error {
				_, verr := ParseInterval(s)
				return verr
			})
			if aerr != nil {
				return aerr
			}
			sec, _ := ParseInterval(raw)
			interval = time.Duration(sec) * time.Second
		} else {
			interval = DefaultInterval
		}
	}

	// ── 6. 指标 ───────────────────────────────────────────────────
	metrics, err := resolveMetrics(opt.Metric, p, interactive, out)
	if err != nil {
		return err
	}

	timeFormat, err := flourish.ParseTimeFormat(opt.TimeFormat)
	if err != nil {
		return err
	}

	// ── 7. 取数 ───────────────────────────────────────────────────
	col := &collect.Collector{
		Client:  client,
		Cache:   &collect.Cache{Root: opt.CacheDir},
		Refresh: opt.Refresh,
		Offline: opt.Offline,
		Log: func(format string, args ...any) {
			fmt.Fprintf(out, "  "+format+"\n", args...)
		},
	}

	report, fromCache, err := col.ChainReport(ctx, chainID)
	if err != nil {
		return decorate(err)
	}
	_ = fromCache

	start, end, ongoing, err := resolveWindow(report, out)
	if err != nil {
		return err
	}
	printChainSummary(out, report, start, end, ongoing)

	columns, err := chain.PlanColumns(start, end, interval)
	if err != nil {
		return err
	}
	if len(columns) > 300 {
		fmt.Fprintf(out, "  ⚠️ 时间轴共 %d 列，Flourish 仍可渲染但动画会很长；"+
			"可把间隔调大（如 10m / 15m）来压缩帧数\n", len(columns))
	}

	fmt.Fprintf(out, "\n▸ 抓取攻击记录（chain 窗口内、我方发起）…\n")
	// 查询窗口两端各放宽 QueryMargin：Torn 的 from/to 是内部游标而非严格时间过滤，
	// 恰好卡在 chain 首尾会静默丢掉边界记录（详见 collect.QueryMargin 的实测说明）。
	// 放宽后由 Aggregate 按真实 start/end 做精确的客户端过滤，结果仍然精确。
	attacks, err := col.Attacks(ctx, collect.AttacksOptions{
		ChainID: chainID,
		Query: torn.AttacksQuery{
			Filters: []string{"outgoing"},
			Limit:   torn.MaxAttacksFullLimit,
			Sort:    "DESC",
			From:    start.Add(-collect.QueryMargin).Unix(),
			To:      end.Add(collect.QueryMargin).Unix(),
		},
		StopWhenPageOlderThan: start.Unix(),
		Slices:                opt.Slices,
	})
	if err != nil {
		return decorate(err)
	}
	for _, w := range attacks.Warnings {
		fmt.Fprintf(out, "  ⚠️ %s\n", w)
	}

	events := toEvents(attacks.Attacks, report.FactionID)
	if len(events) == 0 {
		fmt.Fprintf(out, "\n⚠️ 窗口内没有取到任何我方发起的攻击。\n"+
			"   可能原因：\n"+
			"   1. chain 太旧，Torn 已不再保留这段攻击记录（API 只提供有限的历史）；\n"+
			"   2. key 所属帮派与这条 chain 的帮派不同（attacksfull 只返回**自己帮派**的攻击）；\n"+
			"   3. 该 chain 的 start/end 解析异常。\n"+
			"   本次窗口：%s → %s，帮派 %d\n",
			start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"), report.FactionID)
		return errors.New("没有可用的攻击数据")
	}

	// ── 8. 取名 ───────────────────────────────────────────────────
	names, nameWarn := resolveNames(ctx, col, client, chainID, report.FactionID, events, opt, out)
	for _, w := range nameWarn {
		fmt.Fprintf(out, "  ⚠️ %s\n", w)
	}

	// ── 9. 聚合与输出 ─────────────────────────────────────────────
	labels := flourish.Labels(columns, timeFormat, start)
	outDir := opt.OutDir
	if outDir == "" {
		outDir = DefaultOutDir
	}

	fmt.Fprintf(out, "\n▸ 分桶与输出…\n")
	written := make([]string, 0, len(metrics))
	var baseResult chain.Result
	baseSet := false

	for _, m := range metrics {
		res := chain.Aggregate(events, chain.AggregateOptions{
			Columns: columns,
			Metric:  m,
			Start:   start,
			End:     end,
		})
		// PerAttacker 与指标无关（恒为「成功攻击」口径），
		// 因此任取一个结果即可用于逐人校验，这里取第一个。
		if !baseSet {
			baseResult, baseSet = res, true
		}
		if len(res.Series) == 0 {
			fmt.Fprintf(out, "  ⚠️ 指标 %s 没有任何数据，跳过\n", m)
			continue
		}

		assignNames(res.Series, names)
		table := flourish.Table{Columns: labels, Decimals: m.Decimals()}
		for _, s := range res.Series {
			table.Rows = append(table.Rows, flourish.Row{Name: s.Name, Values: s.Values})
		}

		path := filepath.Join(outDir, fmt.Sprintf("chain-%d-%s.csv", chainID, m))
		if err := flourish.WriteFile(path, table); err != nil {
			return err
		}
		written = append(written, path)
		fmt.Fprintf(out, "  ✓ %s（%d 行 × %d 列）\n", path, len(table.Rows), len(table.Columns)+1)
	}

	// ── 10. 逐人校验 ──────────────────────────────────────────────
	official := make([]chain.OfficialStat, 0, len(report.Attackers))
	for _, a := range report.Attackers {
		official = append(official, chain.OfficialStat{
			AttackerID: a.UserID,
			Hits:       a.Hits,
			Respect:    a.Respect,
		})
	}
	val := chain.Validate(baseResult, official, names)
	printValidation(out, val, report.ChainHits)

	valPath := filepath.Join(outDir, fmt.Sprintf("chain-%d-validation.csv", chainID))
	if err := writeValidationCSV(valPath, val); err != nil {
		fmt.Fprintf(out, "  ⚠️ 写出校验报告失败：%v\n", err)
	} else {
		written = append(written, valPath)
		fmt.Fprintf(out, "  ✓ %s（%d 行，逐人明细）\n", valPath, len(val.Rows))
	}

	// ── 11. 逐条攻击记录（可选） ───────────────────────────────────
	if opt.DumpAttacks {
		path := filepath.Join(outDir, fmt.Sprintf("chain-%d-attacks.csv", chainID))
		st, derr := attacklog.WriteFile(path, attacks.Attacks, attacklog.Options{
			Start:     start,
			End:       end,
			FactionID: report.FactionID,
			Names:     names,
		})
		if derr != nil {
			fmt.Fprintf(out, "  ⚠️ 写出攻击记录失败：%v\n", derr)
		} else {
			written = append(written, path)
			printAttackLogSummary(out, path, st, baseResult)
		}
	}

	printFinalSummary(out, report, baseResult, attacks, interval, limiter, written)
	return nil
}

// printAttackLogSummary 报告导出结果，并与汇总口径交叉核对。
//
// 核对是刻意的：导出与聚合用的是同一条筛选规则（窗口相交 + 本帮派），
// 一旦两处逻辑漂移，数字就会对不上 —— 而这种漂移在图表上完全看不出来。
func printAttackLogSummary(out io.Writer, path string, st attacklog.Stats, base chain.Result) {
	dup := ""
	if st.Duplicates > 0 {
		dup = fmt.Sprintf("，剔除跨页重复 %d 条", st.Duplicates)
	}
	fmt.Fprintf(out, "  ✓ %s（%s 条窗口内攻击%s；其中计入统计 %s 条 / %s respect）\n",
		path, commas(int64(st.Rows)), dup, commas(int64(st.Counted)), money(st.CountedRespect))

	if st.Rows != base.TotalEvents || st.Counted != base.TotalHits ||
		math.Abs(st.CountedRespect-base.TotalRespect) > chain.RespectTolerance {
		fmt.Fprintf(out, "  ⚠️ 攻击记录与统计口径不一致（行 %d/%d · 计入 %d/%d · respect %s/%s），"+
			"请附 chain id 反馈\n",
			st.Rows, base.TotalEvents, st.Counted, base.TotalHits,
			money(st.CountedRespect), money(base.TotalRespect))
	}
}

// applyConfigDefaults 用配置文件里的值填补未显式指定的参数。
func applyConfigDefaults(opt *Options, cfg *Config) {
	if opt.Metric == "" {
		opt.Metric = cfg.Metric
	}
	if opt.TimeFormat == "" {
		opt.TimeFormat = cfg.TimeFormat
	}
	if opt.OutDir == "" {
		opt.OutDir = cfg.OutDir
	}
	if opt.Interval <= 0 && cfg.IntervalSec > 0 {
		opt.Interval = time.Duration(cfg.IntervalSec) * time.Second
	}
	if opt.CacheDir == "" {
		opt.CacheDir = collect.DefaultRoot
	}
	if opt.RateLimit <= 0 {
		opt.RateLimit = DefaultRateLimit
	}
}

// resolveAPIKey 按 参数 → 环境变量 → 配置文件 → 交互输入 的优先级取 key。
func resolveAPIKey(ctx context.Context, p *Prompter, opt Options, cfg *Config,
	interactive bool, out io.Writer) (key, source string, prompted bool, err error,
) {
	if k := strings.TrimSpace(opt.APIKey); k != "" {
		return k, "-api 参数", false, nil
	}
	if k := strings.TrimSpace(os.Getenv("TORN_API_KEY")); k != "" {
		return k, "环境变量 TORN_API_KEY", false, nil
	}
	if k := strings.TrimSpace(cfg.APIKey); k != "" {
		return k, "配置文件", false, nil
	}
	if !interactive {
		return "", "", false, errors.New(
			"缺少 API key：请用 -api 指定、设置环境变量 TORN_API_KEY，或在终端里交互输入")
	}

	fmt.Fprintln(out, "\n需要一个 Torn API key（至少 Limited Access，并勾选 Faction → Attacks / Chain report）。")
	fmt.Fprintln(out, "可在 Torn 的 Preferences → API 页面生成。")

	for attempt := 1; attempt <= 3; attempt++ {
		k, aerr := p.AskSecret("API key（输入不回显）")
		if aerr != nil {
			return "", "", false, aerr
		}
		k = strings.TrimSpace(k)
		if k == "" {
			fmt.Fprintln(out, "  ✗ key 不能为空")
			continue
		}
		// 立即校验：key 写错却跑到抓取中途才失败，体验极差。
		if opt.Offline {
			return k, "交互输入", true, nil
		}
		c, cerr := torn.NewClient(torn.Options{APIKey: k})
		if cerr != nil {
			return "", "", false, cerr
		}
		ki, _, cerr := c.KeyInfo(ctx)
		if cerr != nil {
			fmt.Fprintf(out, "  ✗ 校验失败：%v\n", decorate(cerr))
			continue
		}
		fmt.Fprintf(out, "  ✓ key 有效 · 所有者 #%d · 权限 %s\n", ki.OwnerID, ki.AccessType)
		return k, "交互输入", true, nil
	}
	return "", "", false, errors.New("连续 3 次校验失败，已放弃")
}

// preflight 检查 key 是否具备本工具必需的权限。
func preflight(ctx context.Context, c *torn.Client, out io.Writer) error {
	ki, _, err := c.KeyInfo(ctx)
	if err != nil {
		return decorate(err)
	}
	if !ki.FactionAccess {
		return fmt.Errorf("该 key 没有 faction 权限：请在 Torn 的 API 页面为它勾选 Faction → Attacks / Chain report")
	}

	// chainreport 提供 start/end，attacksfull 提供逐条攻击，两者缺一不可。
	missing := make([]string, 0, 2)
	if !ki.HasFactionSelection("chainreport") {
		missing = append(missing, "chainreport")
	}
	if !ki.HasFactionSelection("attacksfull") {
		missing = append(missing, "attacksfull")
	}
	if len(missing) > 0 {
		return fmt.Errorf("key 缺少必需的 faction 权限：%s", strings.Join(missing, "、"))
	}

	fmt.Fprintf(out, "  ✓ 权限预检通过（%s，faction 权限已开启）\n", ki.AccessType)
	return nil
}

// resolveWindow 确定统计窗口。
//
// 已结束的 chain 直接用报告里的 start/end；进行中的 chain 用「现在」作为右端点，
// 并显式告知用户 —— 悄悄截断会让人以为图表是完整的。
func resolveWindow(r *torn.ChainReport, out io.Writer) (start, end time.Time, ongoing bool, err error) {
	if r.Start.IsZero() {
		return time.Time{}, time.Time{}, false, fmt.Errorf(
			"chain #%d 的报告里没有开始时间，无法确定统计窗口", r.ID)
	}
	start = r.Start
	if r.Ongoing {
		end = time.Now().Truncate(time.Second)
		if end.Before(start) {
			end = start
		}
		fmt.Fprintf(out, "  ⓘ 该 chain 仍在进行中：右端点取「现在」（%s）\n", end.Format("2006-01-02 15:04:05"))
		return start, end, true, nil
	}
	end = r.End
	if end.Before(start) {
		return time.Time{}, time.Time{}, false, fmt.Errorf(
			"chain #%d 的结束时间早于开始时间（start=%s end=%s），报告数据异常",
			r.ID, start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	return start, end, false, nil
}

// resolveMetrics 解析指标选择。
func resolveMetrics(raw string, p *Prompter, interactive bool, out io.Writer) ([]chain.Metric, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		if !interactive {
			raw = DefaultMetric
		} else {
			fmt.Fprintln(out, "\n指标含义：respect = 累计贡献的 respect；hits = 累计连击命中数")
			ans, err := p.Ask("指标（respect / hits / both）", DefaultMetric, func(s string) error {
				switch strings.ToLower(strings.TrimSpace(s)) {
				case "respect", "hits", "both":
					return nil
				}
				return fmt.Errorf("可选值：respect / hits / both")
			})
			if err != nil {
				return nil, err
			}
			raw = strings.ToLower(strings.TrimSpace(ans))
		}
	}

	switch raw {
	case "both":
		return []chain.Metric{chain.MetricRespect, chain.MetricHits}, nil
	default:
		m, err := chain.ParseMetric(raw)
		if err != nil {
			return nil, err
		}
		return []chain.Metric{m}, nil
	}
}

// toEvents 把 API 攻击记录转换成领域事件，并剔除不属于本帮派的攻击。
//
// 为什么还要再按 faction 过滤一次：attacksfull 返回的是「自己帮派的攻击」，
// 但成员退帮后其历史攻击仍会出现在列表里；若不过滤，
// 这些人的累计值会混进竞赛，且与官方 chainreport 的参与者名单对不上。
func toEvents(attacks []torn.Attack, factionID int64) []chain.Event {
	out := make([]chain.Event, 0, len(attacks))
	for _, a := range attacks {
		if a.AttackerID == 0 {
			continue
		}
		if a.AttackerFactionID != 0 && factionID != 0 && a.AttackerFactionID != factionID {
			continue
		}
		out = append(out, chain.Event{
			AttackerID: a.AttackerID,
			Started:    a.Started,
			Ended:      a.Ended,
			Respect:    a.RespectGain,
			Result:     a.Result,
		})
	}
	return out
}

// resolveNames 为每个参与者确定显示名。
//
// 优先用帮派成员名单；查不到的（多半是中途退帮的成员）可选地逐个查 /user/{id}/basic，
// 仍失败则降级成 "User<id>"。名字只影响展示，任何失败都不应中断流程。
func resolveNames(ctx context.Context, col *collect.Collector, client *torn.Client,
	chainID, factionID int64, events []chain.Event, opt Options, out io.Writer,
) (map[int64]string, []string) {
	names := make(map[int64]string)
	var warnings []string

	if factionID > 0 {
		m, err := col.Members(ctx, chainID, factionID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("取成员名单失败（将用 User<id> 代替）：%v", decorate(err)))
		} else {
			for id, n := range m {
				names[id] = n
			}
		}
	}

	// 收集缺失的 id（去重、保持稳定顺序）。
	missingSet := make(map[int64]struct{})
	var missing []int64
	for _, e := range events {
		if _, ok := names[e.AttackerID]; ok {
			continue
		}
		if _, seen := missingSet[e.AttackerID]; seen {
			continue
		}
		missingSet[e.AttackerID] = struct{}{}
		missing = append(missing, e.AttackerID)
	}

	if len(missing) > 0 && opt.ResolveNames && !opt.Offline {
		limit := min(len(missing), maxNameLookups)
		fmt.Fprintf(out, "  ⓘ %d 名参与者在当前成员名单里查不到，尝试逐个查询名字（上限 %d 次）…\n",
			len(missing), limit)
		for i := 0; i < limit; i++ {
			id := missing[i]
			n, err := client.UserBasicName(ctx, id)
			if err != nil {
				continue
			}
			names[id] = n
		}
	}

	for _, id := range missing {
		if _, ok := names[id]; !ok {
			names[id] = fmt.Sprintf("User%d", id)
		}
	}
	return names, warnings
}

// assignNames 把名字写进序列，并对重名做消歧。
//
// 重名必须处理：Flourish 以标签区分实体，两行同名会被合并成同一条柱子，
// 结果是两个人的数据被叠在一起而图表看起来毫无异常。
func assignNames(series []chain.Series, names map[int64]string) {
	count := make(map[string]int, len(series))
	for i := range series {
		n := names[series[i].AttackerID]
		if n == "" {
			n = fmt.Sprintf("User%d", series[i].AttackerID)
		}
		series[i].Name = n
		count[n]++
	}
	for i := range series {
		if count[series[i].Name] > 1 {
			series[i].Name = fmt.Sprintf("%s (%d)", series[i].Name, series[i].AttackerID)
		}
	}
}

// decorate 把 Torn 的 API 错误补上一句可执行的建议。
func decorate(err error) error {
	var apiErr *torn.APIError
	if errors.As(err, &apiErr) {
		if hint := apiErr.Hint(); hint != "" {
			return fmt.Errorf("%w\n   → %s", err, hint)
		}
	}
	return err
}

func printBanner(out io.Writer) {
	fmt.Fprintln(out, "┌──────────────────────────────────────────────────────────┐")
	fmt.Fprintln(out, "│  Torn Chain → Flourish Bar Chart Race                    │")
	fmt.Fprintln(out, "└──────────────────────────────────────────────────────────┘")
}

func printChainSummary(out io.Writer, r *torn.ChainReport, start, end time.Time, ongoing bool) {
	state := "已结束"
	if ongoing {
		state = "进行中"
	}
	fmt.Fprintf(out, "\n▸ chain #%d · 帮派 %d · %s\n", r.ID, r.FactionID, state)
	fmt.Fprintf(out, "  %s → %s（%s）\n",
		start.Format("2006-01-02 15:04:05"),
		end.Format("2006-01-02 15:04:05"),
		humanDuration(end.Sub(start)))
	fmt.Fprintf(out, "  官方口径：%s 连击 · %s respect · %d 名参与者 · %s 个目标\n",
		commas(int64(r.ChainHits)), money(r.Respect), r.Members, commas(int64(r.Targets)))
}

// printValidation 输出逐人校验结论。
//
// 只详细列出对不上的成员：100 人全对时刷屏没有信息量，
// 而对不上的人恰恰是排查的唯一起点。完整明细落在 CSV 里。
func printValidation(out io.Writer, v chain.Validation, chainHits int) {
	fmt.Fprintf(out, "\n▸ 逐人校验（对比官方 chainreport）\n")
	fmt.Fprintf(out, "  成功攻击数一致：%d/%d 人\n", v.HitsMatched, v.Total)
	fmt.Fprintf(out, "  respect 一致：  %d/%d 人（容差 %.2f）\n", v.RespectMatched, v.Total, chain.RespectTolerance)
	// 连击长度 N 就意味着 N 次命中，因此「成功攻击数」应当正好等于 chain 长度。
	fmt.Fprintf(out, "  合计：成功攻击 %s / chain 长度 %s（差 %s）· respect %s / 官方 %s（差 %s）\n",
		commas(int64(v.TotalOurHits)), commas(int64(chainHits)),
		commas(int64(v.TotalOurHits-chainHits)),
		money(v.TotalOurRespect), money(v.TotalOfficialRespect),
		money(v.TotalOurRespect-v.TotalOfficialRespect))

	mm := v.Mismatches()
	if len(mm) == 0 {
		fmt.Fprintf(out, "  ✓ 全部参与者的攻击数与 respect 均与官方一致\n")
		return
	}

	fmt.Fprintf(out, "  ⚠️ %d 人对不上：\n", len(mm))
	const maxShow = 10
	for i, r := range mm {
		if i == maxShow {
			fmt.Fprintf(out, "     … 其余 %d 人见校验 CSV\n", len(mm)-maxShow)
			break
		}
		switch {
		case r.OnlyInOfficial:
			fmt.Fprintf(out, "     %-20s (#%d)  官方有记录但我方未取到（官方 %d 次 / %s）\n",
				truncateName(r.Name, 20), r.AttackerID, r.OfficialHits, money(r.OfficialRespect))
		case r.OnlyInOurs:
			fmt.Fprintf(out, "     %-20s (#%d)  我方有记录但官方报告未列出（我方 %d 次 / %s）\n",
				truncateName(r.Name, 20), r.AttackerID, r.OurHits, money(r.OurRespect))
		default:
			fmt.Fprintf(out, "     %-20s (#%d)  攻击 %s/%s（%+d）· respect %s/%s（%+.2f）\n",
				truncateName(r.Name, 20), r.AttackerID,
				commas(int64(r.OurHits)), commas(int64(r.OfficialHits)), r.HitDiff(),
				money(r.OurRespect), money(r.OfficialRespect), r.RespectDiff())
		}
	}
}

// writeValidationCSV 把逐人校验明细写成 CSV，供人工核对或留档。
func writeValidationCSV(path string, v chain.Validation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建校验文件失败: %w", err)
	}
	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	header := []string{
		"Name", "UserID", "OurHits", "OfficialHits", "HitDiff",
		"OurRespect", "OfficialRespect", "RespectDiff",
		"Status",
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, r := range v.Rows {
		status := "OK"
		switch {
		case r.OnlyInOurs:
			status = "OnlyInOurs"
		case r.OnlyInOfficial:
			status = "OnlyInOfficial"
		case !r.HitsOK():
			status = "HitMismatch"
		case !r.RespectOK():
			status = "RespectMismatch"
		}
		rec := []string{
			r.Name,
			strconv.FormatInt(r.AttackerID, 10),
			strconv.Itoa(r.OurHits),
			strconv.Itoa(r.OfficialHits),
			strconv.Itoa(r.HitDiff()),
			num2(r.OurRespect),
			num2(r.OfficialRespect),
			num2(r.RespectDiff()),
			status,
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return f.Close()
}

// num2 把浮点数格式化成两位小数，并把 -0.00 归一成 0.00。
//
// 逐人校验里精确相等的两项相减会得到 -1e-12 这类浮点噪声，
// 直接格式化会打印成 "-0.00" —— 看起来像是「有一点点差异」，实际是完全相等。
func num2(f float64) string {
	r := math.Round(f*100) / 100
	if r == 0 {
		r = 0 // 赋字面量 0 可去掉负零符号
	}
	return strconv.FormatFloat(r, 'f', 2, 64)
}

// printFinalSummary 输出收尾汇总。
func printFinalSummary(out io.Writer, r *torn.ChainReport, res chain.Result,
	attacks *collect.AttacksResult, interval time.Duration, limiter *torn.Limiter, written []string,
) {
	line := strings.Repeat("─", 60)
	fmt.Fprintf(out, "\n%s\n", line)
	fmt.Fprintf(out, " 我方发起攻击 %s 次，其中成功 %s 次（失败/僵持/超时/逃跑/助攻/中断已剔除）\n",
		commas(int64(res.TotalEvents)), commas(int64(res.TotalHits)))
	fmt.Fprintf(out, " 成功攻击的 respect 合计 %s（官方口径 %s）\n",
		money(res.TotalRespect), money(r.Respect))
	sliced := ""
	if attacks.Slices > 1 {
		sliced = fmt.Sprintf("（%d 路并发分片）", attacks.Slices)
	}
	fmt.Fprintf(out, " 时间分桶：%s × %d 列 · 参与者 %d 人 · 攻击分页 %d 页%s\n",
		interval, len(res.Columns), len(res.Series), attacks.Pages, sliced)

	if len(res.Series) > 0 {
		fmt.Fprintf(out, "\n Top 10（%s）：\n", res.Metric)
		n := min(len(res.Series), 10)
		for i := 0; i < n; i++ {
			s := res.Series[i]
			if res.Metric == chain.MetricHits {
				fmt.Fprintf(out, "  %2d. %-24s %10s\n", i+1, truncateName(s.Name, 24), commas(int64(s.Total)))
			} else {
				fmt.Fprintf(out, "  %2d. %-24s %10s\n", i+1, truncateName(s.Name, 24), money(s.Total))
			}
		}
	}

	fmt.Fprintf(out, "\n 已写出：\n")
	for _, p := range written {
		fmt.Fprintf(out, "   %s\n", p)
	}
	if limiter != nil {
		fmt.Fprintf(out, "\n API 请求消耗：%d 次\n", limiter.RequestsTotal())
	}
	fmt.Fprintf(out, "%s\n", line)
	fmt.Fprintf(out, " 下一步：\n")
	fmt.Fprintf(out, "   1. 打开 https://app.flourish.studio/ → New → Bar chart race\n")
	fmt.Fprintf(out, "   2. 上传上面的 CSV（首列是参与者，其余每列是一个时间阶段）\n")
	fmt.Fprintf(out, "   3. 在预览里按需调整配色、柱子数量与动画时长\n")
	fmt.Fprintf(out, "%s\n", line)
}

// humanDuration 把时长渲染成「3天11小时9分」这种紧凑形式。
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)

	var b strings.Builder
	if days > 0 {
		fmt.Fprintf(&b, "%d 天 ", days)
	}
	if hours > 0 || days > 0 {
		fmt.Fprintf(&b, "%d 小时 ", hours)
	}
	fmt.Fprintf(&b, "%d 分", mins)
	return b.String()
}

// commas 给整数加千分位分隔符。
func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// money 把浮点数渲染成带千分位、两位小数的字符串。
func money(f float64) string {
	// 先四舍五入到 2 位：-0.004 这类微小负值若不归零，会打印成 "-0.00"。
	r := math.Round(f*100) / 100
	if r == 0 {
		return "0.00"
	}
	neg := r < 0
	if neg {
		r = -r
	}
	intPart := int64(r)
	frac := r - float64(intPart)
	s := commas(intPart) + "." + fmt.Sprintf("%02d", int(math.Round(frac*100)))
	if neg {
		return "-" + s
	}
	return s
}

// truncateName 按「显示宽度」截断名字，避免把汇总表撑歪。
func truncateName(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}
