// Package chain 是本工具的领域层：把「一条连击 + 一批攻击」折算成
// 「按时间分桶的累计序列」，并对照官方报告做逐人校验。
//
// 本包**不依赖网络、不依赖 torn 包**，输入输出都是纯数据 —— 这样分桶口径
// 可以被单元测试逐条钉死，而不是靠对着真实 API 反复试。
package chain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Metric 是柱状图竞赛的计量口径。
type Metric int

const (
	// MetricRespect 以「累计获得的 respect」为长度。
	// 这是最常用的口径：柱子的高低直接对应这名成员给帮派贡献了多少 respect。
	MetricRespect Metric = iota

	// MetricHits 以「累计成功攻击次数」为长度。
	// 只看数量不看质量，适合评估出勤量。
	MetricHits
)

// String 返回指标的可读名（同时用作输出文件名的一部分）。
func (m Metric) String() string {
	switch m {
	case MetricHits:
		return "hits"
	default:
		return "respect"
	}
}

// ParseMetric 解析用户输入的指标名。
func ParseMetric(s string) (Metric, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "respect", "resp":
		return MetricRespect, nil
	case "hits", "hit", "count":
		return MetricHits, nil
	}
	return 0, fmt.Errorf("未知指标 %q（可选：respect / hits）", s)
}

// Decimals 返回该指标输出时应保留的小数位。
func (m Metric) Decimals() int {
	if m == MetricHits {
		return 0
	}
	return 2
}

// Event 是一次「我方发起的攻击」在本工具里的最小表示。
type Event struct {
	AttackerID int64
	// Started 是攻击**发起**时刻，Ended 是**结束（命中）**时刻。
	//
	// 两者都必须保留：连击是在攻击结束的瞬间推进的，而 Torn 的
	// chainreport.start / end 记录的也正是「首次命中 / 末次命中」的完成时刻。
	Started time.Time
	Ended   time.Time
	Respect float64
	Result  string
}

// EffectiveAt 返回该次攻击在时间轴上应归属的时刻。
//
// 优先用 ended：连击是在攻击**结束（命中）**的瞬间推进的。
// ended 缺失或异常（早于 started）时回退到 started。
func (e Event) EffectiveAt() time.Time {
	if e.Ended.IsZero() || e.Ended.Before(e.Started) {
		return e.Started
	}
	return e.Ended
}

// Overlaps 报告该次攻击的 [started, ended] 区间是否与 chain 窗口 [start, end] 有交集。
//
// ⚠️ 必须用「区间重叠」而不是「时刻落在窗口内」，这是实测换来的结论：
//
//	起点侧：chainreport.start 等于**首击的 ended**，比该次攻击的 started 晚几秒。
//	        （实测 id=526836524 started=1792922363 / ended=1792922366 / start=1792922366）
//	终点侧：chainreport.end **早于**末次命中的 ended，
//	        用 `ended <= end` 会把收尾那几次命中（含一条 40960 respect 的 bonus）全部排除。
//
// 两个边界方向相反，只有「区间相交」能同时满足：
//   - 上一场 chain 的攻击 ended < start → 排除；
//   - 下一场 chain 的攻击 started > end → 排除；
//   - 恰好跨越边界的首击 / 末击 → 保留。
func (e Event) Overlaps(start, end time.Time) bool {
	s, en := e.Started, e.Ended
	if en.IsZero() || en.Before(s) {
		en = s
	}
	return !s.After(end) && !en.Before(start)
}

// nonSuccessfulResults 是**不计入统计**的结算结果。
//
// 本工具只关心成功的攻击。以下结果既不计入次数，也不计入 respect
// （实测这些结果的 respect_gain 恒为 0）：
//
//	Lost         打输
//	Stalemate    僵持
//	Timeout      超时未结束
//	Escape       目标逃跑
//	Assist       助攻 —— 给 respect 但不推进连击，也不算自己打中
//	Interrupted  被打断（例如目标先被别人打进医院）
//
// 用「黑名单」而不是「白名单」是刻意的：Torn 以后若新增某种成功结算，
// 黑名单会自动把它算进来（宁可多算，因为漏算在图表上完全看不出来）；
// 白名单则会静默丢掉新类型。
//
// 该判据已用官方 chainreport 的逐人 attacks.total 反向验证：
// 按此规则统计出的每名成员成功攻击数，与官方报告逐人吻合（见 Validate）。
var nonSuccessfulResults = map[string]bool{
	"Lost":        true,
	"Stalemate":   true,
	"Timeout":     true,
	"Escape":      true,
	"Assist":      true,
	"Interrupted": true,
}

// IsSuccessful 判定一次攻击是否计入统计。
func IsSuccessful(e Event) bool {
	return !nonSuccessfulResults[e.Result]
}

// PlanColumns 生成时间轴上的「阶段」列，供 Flourish 宽表使用。
//
// 规则：
//   - 第一列固定是 chain 开始时刻（此时累计值恒为 0，竞赛从这里起跑）；
//   - 之后每隔 interval 一列，直到不超过 end；
//   - 若 end 不是 interval 的整数倍，**额外补一列 end**，
//     保证最后一列的值等于全量总和 —— 否则图表终点会少掉尾巴。
//
// 返回的列严格递增，且首列 == start、末列 == end（end > start 时）。
func PlanColumns(start, end time.Time, interval time.Duration) ([]time.Time, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("统计间隔必须为正数，实际 %s", interval)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("chain 结束时间 %s 早于开始时间 %s",
			end.Format(time.RFC3339), start.Format(time.RFC3339))
	}

	capacity := int(end.Sub(start)/interval) + 2
	cols := make([]time.Time, 0, capacity)
	cols = append(cols, start)

	for t := start.Add(interval); !t.After(end); t = t.Add(interval) {
		cols = append(cols, t)
	}
	if last := cols[len(cols)-1]; last.Before(end) {
		cols = append(cols, end)
	}
	return cols, nil
}

// Series 是某位参与者在整条时间轴上的累计值。
type Series struct {
	AttackerID int64
	Name       string
	// Values 与 Result.Columns 等长，单调不减。
	Values []float64
	// Total 是末列的值（= 全量累计），用于排序与汇总。
	Total float64
}

// AggregateOptions 控制聚合口径。
type AggregateOptions struct {
	// Columns 是 PlanColumns 的结果。
	Columns []time.Time
	// Metric 是计量口径。
	Metric Metric
	// Start / End 是 chain 的闭区间，窗口外的攻击一律丢弃。
	Start, End time.Time
}

// timedEvent 把「有效时刻」与事件绑在一起，避免排序时重复计算。
type timedEvent struct {
	at time.Time
	e  Event
}

// Result 是一次聚合的完整产物。
type Result struct {
	Metric  Metric
	Columns []time.Time
	Series  []Series

	// TotalEvents 是窗口内我方发起的攻击总数（含失败、僵持等未计入的）。
	TotalEvents int
	// TotalHits 是窗口内**成功**攻击数 —— 即实际参与累计的次数。
	TotalHits int
	// TotalRespect 是成功攻击带来的 respect 合计。
	TotalRespect float64
	// PerAttacker 是每名成员的成功攻击数与 respect，供逐人校验使用。
	PerAttacker map[int64]PersonStat
}

// PersonStat 是单名成员的聚合结果。
type PersonStat struct {
	Hits    int
	Respect float64
}

// Aggregate 把攻击事件折算成按时间分桶的累计序列。
//
// 语义要点：
//   - **只统计成功攻击**（见 IsSuccessful），失败/僵持/超时/逃跑/助攻/中断全部剔除；
//   - 累计值在每列取「该时刻及之前所有事件之和」，因此序列单调不减；
//   - 第一次命中之前的列填 0 —— 让 Flourish 从 0 起跑，而不是从入场那一刻才出现；
//   - 只输出窗口内至少有一次成功攻击的参与者，避免满屏 0 长度的柱子。
func Aggregate(events []Event, opt AggregateOptions) Result {
	res := Result{
		Metric:      opt.Metric,
		Columns:     opt.Columns,
		PerAttacker: make(map[int64]PersonStat),
	}
	if len(opt.Columns) == 0 {
		return res
	}

	byAttacker := make(map[int64][]timedEvent)
	for _, e := range events {
		if e.AttackerID == 0 {
			continue
		}
		t := e.EffectiveAt()
		if !e.Overlaps(opt.Start, opt.End) {
			continue
		}
		// 夹紧到窗口内：跨越右边界的那几次命中，其 ended 晚于 end，
		// 若原样拿去比列，会落进最后一列之外，导致累计值偏小。
		if t.Before(opt.Start) {
			t = opt.Start
		}
		if t.After(opt.End) {
			t = opt.End
		}

		res.TotalEvents++
		if !IsSuccessful(e) {
			continue
		}

		res.TotalHits++
		res.TotalRespect += e.Respect
		stat := res.PerAttacker[e.AttackerID]
		stat.Hits++
		stat.Respect += e.Respect
		res.PerAttacker[e.AttackerID] = stat

		byAttacker[e.AttackerID] = append(byAttacker[e.AttackerID], timedEvent{at: t, e: e})
	}

	res.Series = make([]Series, 0, len(byAttacker))
	for id, evs := range byAttacker {
		sort.Slice(evs, func(i, j int) bool { return evs[i].at.Before(evs[j].at) })

		values := make([]float64, len(opt.Columns))
		var cum float64
		next := 0
		for ci, col := range opt.Columns {
			for next < len(evs) && !evs[next].at.After(col) {
				if opt.Metric == MetricHits {
					cum++
				} else {
					cum += evs[next].e.Respect
				}
				next++
			}
			values[ci] = cum
		}
		res.Series = append(res.Series, Series{
			AttackerID: id,
			Values:     values,
			Total:      cum,
		})
	}

	// 按总量降序排列：输出稳定、便于人工核对，也让 CSV 的初始顺序更有意义。
	sort.Slice(res.Series, func(i, j int) bool {
		if res.Series[i].Total != res.Series[j].Total {
			return res.Series[i].Total > res.Series[j].Total
		}
		return res.Series[i].AttackerID < res.Series[j].AttackerID
	})
	return res
}
