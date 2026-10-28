package chain

import (
	"math"
	"sort"
	"strconv"
)

// RespectTolerance 是 respect 逐人比对的容差。
//
// 取 0.011 而不是 0：官方给的是两位小数的浮点值，我方是逐条累加，
// 累加顺序不同会带来 1e-12 量级的差异，卡零会制造大量假告警。
// 但也不能放宽到 0.1 —— 那会掩盖「少算一次小额攻击」这类真实问题。
const RespectTolerance = 0.011

// OfficialStat 是官方 chainreport 里一名参与者的汇总，作为校验基准。
//
// 之所以单独定义而不直接复用 torn 的类型：本包要保持零外部依赖，
// 这样校验逻辑可以被单元测试直接喂数据。
type OfficialStat struct {
	AttackerID int64
	Hits       int
	Respect    float64
}

// PersonCheck 是单名成员的校验结果。
type PersonCheck struct {
	AttackerID int64
	Name       string

	OurHits         int
	OfficialHits    int
	OurRespect      float64
	OfficialRespect float64
	OurBest         float64
	OfficialBest    float64

	// OnlyInOurs / OnlyInOfficial 标记「一方有、另一方完全没有」的参与者。
	// 这通常意味着口径或抓取范围出了问题，比单纯的数值偏差更严重。
	OnlyInOurs     bool
	OnlyInOfficial bool
}

// HitDiff 返回成功攻击数的差值（我方 - 官方）。
func (p PersonCheck) HitDiff() int { return p.OurHits - p.OfficialHits }

// RespectDiff 返回 respect 的差值（我方 - 官方）。
func (p PersonCheck) RespectDiff() float64 { return p.OurRespect - p.OfficialRespect }

// HitsOK 报告攻击数是否与官方一致。
func (p PersonCheck) HitsOK() bool {
	return !p.OnlyInOurs && !p.OnlyInOfficial && p.OurHits == p.OfficialHits
}

// RespectOK 报告 respect 是否在容差内与官方一致。
func (p PersonCheck) RespectOK() bool {
	return !p.OnlyInOurs && !p.OnlyInOfficial &&
		math.Abs(p.RespectDiff()) <= RespectTolerance
}

// OK 报告该成员的攻击数与 respect 是否都对得上。
func (p PersonCheck) OK() bool { return p.HitsOK() && p.RespectOK() }

// Validation 是一次逐人校验的完整结果。
type Validation struct {
	Rows []PersonCheck

	HitsMatched    int
	RespectMatched int
	Total          int

	// TotalOurHits / TotalOfficialHits 是全帮合计，用于快速判断整体量级是否对得上。
	TotalOurHits         int
	TotalOfficialHits    int
	TotalOurRespect      float64
	TotalOfficialRespect float64
}

// AllOK 报告是否每名成员的攻击数与 respect 都对得上。
func (v Validation) AllOK() bool {
	return v.HitsMatched == v.Total && v.RespectMatched == v.Total
}

// Mismatches 返回所有对不上的成员，差异大的排在前面。
func (v Validation) Mismatches() []PersonCheck {
	out := make([]PersonCheck, 0, len(v.Rows))
	for _, r := range v.Rows {
		if !r.OK() {
			out = append(out, r)
		}
	}
	return out
}

// Validate 用官方 chainreport 的逐人汇总校验我方聚合结果。
//
// 这是本工具**唯一能自证正确**的手段：抓取或分页一旦出问题，图表照样画得出来、
// 而且看起来毫无异常，只有这里逐人的攻击数会对不上。
//
// 之所以先比「攻击数」再比「respect」：攻击数是整数，差 1 就是差 1，没有解释空间；
// respect 是浮点，需要容差。先把整数对平，浮点的差异才有意义。
func Validate(res Result, official []OfficialStat, names map[int64]string) Validation {
	offByID := make(map[int64]OfficialStat, len(official))
	for _, o := range official {
		offByID[o.AttackerID] = o
	}

	v := Validation{Total: len(official)}
	seen := make(map[int64]bool, len(official))

	for _, o := range official {
		seen[o.AttackerID] = true
		ours, ok := res.PerAttacker[o.AttackerID]

		row := PersonCheck{
			AttackerID:      o.AttackerID,
			Name:            displayName(names, o.AttackerID),
			OfficialHits:    o.Hits,
			OfficialRespect: o.Respect,
			OnlyInOfficial:  !ok,
		}
		if ok {
			row.OurHits = ours.Hits
			row.OurRespect = ours.Respect
		}
		if row.HitsOK() {
			v.HitsMatched++
		}
		if row.RespectOK() {
			v.RespectMatched++
		}
		v.TotalOfficialHits += o.Hits
		v.TotalOfficialRespect += o.Respect
		v.Rows = append(v.Rows, row)
	}

	// 我方有、官方报告里没有的参与者（通常是退帮成员的历史攻击混进来了）。
	for id, ours := range res.PerAttacker {
		if seen[id] {
			continue
		}
		v.Rows = append(v.Rows, PersonCheck{
			AttackerID: id,
			Name:       displayName(names, id),
			OurHits:    ours.Hits,
			OurRespect: ours.Respect,
			OnlyInOurs: true,
		})
	}

	for _, r := range v.Rows {
		v.TotalOurHits += r.OurHits
		v.TotalOurRespect += r.OurRespect
	}

	// 排序：差异大的优先暴露，对得上的按官方攻击数降序垫后。
	sort.SliceStable(v.Rows, func(i, j int) bool {
		a, b := v.Rows[i], v.Rows[j]
		if a.OK() != b.OK() {
			return !a.OK() // 不一致的排前面
		}
		da := math.Abs(float64(a.HitDiff())) + math.Abs(a.RespectDiff())
		db := math.Abs(float64(b.HitDiff())) + math.Abs(b.RespectDiff())
		if da != db {
			return da > db
		}
		if a.OfficialHits != b.OfficialHits {
			return a.OfficialHits > b.OfficialHits
		}
		return a.AttackerID < b.AttackerID
	})
	return v
}

// displayName 取名字，缺失时降级成 "User<id>"。
func displayName(names map[int64]string, id int64) string {
	if n, ok := names[id]; ok && n != "" {
		return n
	}
	return "User" + strconv.FormatInt(id, 10)
}
