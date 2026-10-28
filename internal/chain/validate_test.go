package chain

import (
	"testing"
	"time"
)

func TestIsSuccessfulExcludesListedResults(t *testing.T) {
	// 用户明确要求：失败、僵持、超时、逃跑、助攻不算；中断同样不算。
	excluded := []string{"Lost", "Stalemate", "Timeout", "Escape", "Assist", "Interrupted"}
	for _, r := range excluded {
		if IsSuccessful(Event{Result: r}) {
			t.Errorf("结果 %q 不应计入统计", r)
		}
	}

	// 其余结算都算成功。用黑名单而不是白名单，
	// 是为了让 Torn 以后新增的成功类型自动被算进来。
	successful := []string{"Attacked", "Mugged", "Hospitalized", "Arrested", "Looted", "Bounty"}
	for _, r := range successful {
		if !IsSuccessful(Event{Result: r}) {
			t.Errorf("结果 %q 应计入统计", r)
		}
	}
}

func TestAggregateCountsOnlySuccessfulAttacks(t *testing.T) {
	cols, err := PlanColumns(at(0), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{AttackerID: 1, Started: at(1), Respect: 3, Result: "Attacked"},
		{AttackerID: 1, Started: at(2), Respect: 0, Result: "Lost"},
		{AttackerID: 1, Started: at(3), Respect: 0, Result: "Stalemate"},
		{AttackerID: 1, Started: at(4), Respect: 0, Result: "Escape"},
		{AttackerID: 1, Started: at(5), Respect: 0, Result: "Timeout"},
		{AttackerID: 1, Started: at(6), Respect: 0, Result: "Assist"},
		{AttackerID: 1, Started: at(7), Respect: 0, Result: "Interrupted"},
		{AttackerID: 1, Started: at(8), Respect: 4, Result: "Mugged"},
	}

	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricHits, Start: at(0), End: at(10),
	})

	if res.TotalEvents != 8 {
		t.Errorf("TotalEvents = %d，期望 8（全部我方攻击）", res.TotalEvents)
	}
	if res.TotalHits != 2 {
		t.Errorf("TotalHits = %d，期望 2（只有 Attacked 与 Mugged）", res.TotalHits)
	}
	if res.TotalRespect != 7 {
		t.Errorf("TotalRespect = %v，期望 7", res.TotalRespect)
	}
	if got := res.PerAttacker[1]; got.Hits != 2 || got.Respect != 7 {
		t.Errorf("PerAttacker[1] = %+v，期望 {Hits:2 Respect:7 Best:4}", got)
	}
}

func TestValidateMatchesOfficialStats(t *testing.T) {
	cols, err := PlanColumns(at(0), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{AttackerID: 1, Started: at(1), Respect: 3, Result: "Attacked"},
		{AttackerID: 1, Started: at(2), Respect: 2, Result: "Mugged"},
		{AttackerID: 1, Started: at(3), Respect: 0, Result: "Lost"}, // 不计
		{AttackerID: 2, Started: at(4), Respect: 5, Result: "Attacked"},
	}
	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricRespect, Start: at(0), End: at(10),
	})

	official := []OfficialStat{
		{AttackerID: 1, Hits: 2, Respect: 5},
		{AttackerID: 2, Hits: 1, Respect: 5},
	}
	names := map[int64]string{1: "alice", 2: "bob"}

	v := Validate(res, official, names)

	if v.Total != 2 {
		t.Errorf("Total = %d，期望 2", v.Total)
	}
	if v.HitsMatched != 2 || v.RespectMatched != 2 {
		t.Errorf("期望全部一致，实际 HitsMatched=%d RespectMatched=%d", v.HitsMatched, v.RespectMatched)
	}
	if !v.AllOK() {
		t.Error("AllOK() 应为 true")
	}
	if len(v.Mismatches()) != 0 {
		t.Errorf("不应有差异，实际 %v", v.Mismatches())
	}
	if v.Rows[0].Name != "alice" && v.Rows[1].Name != "alice" {
		t.Error("名字未正确注入")
	}
}

func TestValidateDetectsMissingAndExtra(t *testing.T) {
	cols, err := PlanColumns(at(0), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{AttackerID: 1, Started: at(1), Respect: 3, Result: "Attacked"},
		{AttackerID: 3, Started: at(2), Respect: 9, Result: "Attacked"}, // 官方报告里没有
	}
	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricRespect, Start: at(0), End: at(10),
	})

	official := []OfficialStat{
		{AttackerID: 1, Hits: 1, Respect: 3},
		{AttackerID: 2, Hits: 4, Respect: 20}, // 我方没取到
	}
	v := Validate(res, official, nil)

	if v.Total != 2 {
		t.Errorf("Total 应只统计官方名单人数 = 2，实际 %d", v.Total)
	}
	if len(v.Rows) != 3 {
		t.Fatalf("Rows 应为 3（2 官方 + 1 仅我方），实际 %d", len(v.Rows))
	}

	var sawOnlyOfficial, sawOnlyOurs bool
	for _, r := range v.Rows {
		switch {
		case r.OnlyInOfficial && r.AttackerID == 2:
			sawOnlyOfficial = true
			if r.HitsOK() {
				t.Error("仅官方有的成员不应被判为一致")
			}
		case r.OnlyInOurs && r.AttackerID == 3:
			sawOnlyOurs = true
			if r.HitsOK() {
				t.Error("仅我方有的成员不应被判为一致")
			}
		}
	}
	if !sawOnlyOfficial || !sawOnlyOurs {
		t.Errorf("未识别出单边成员：onlyOfficial=%v onlyOurs=%v", sawOnlyOfficial, sawOnlyOurs)
	}

	// 差异大的排最前，便于用户第一眼看到问题。
	if v.Rows[0].AttackerID != 2 {
		t.Errorf("首行应为差异最大的 id=2，实际 %d", v.Rows[0].AttackerID)
	}
}

func TestValidateRespectTolerance(t *testing.T) {
	cols, err := PlanColumns(at(0), at(5), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{{AttackerID: 1, Started: at(1), Respect: 100.004, Result: "Attacked"}}
	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricRespect, Start: at(0), End: at(5),
	})

	// 浮点累加噪声不应被判为差异。
	v := Validate(res, []OfficialStat{{AttackerID: 1, Hits: 1, Respect: 100.00}}, nil)
	if v.RespectMatched != 1 {
		t.Errorf("1e-3 级差异应落在容差内，实际 RespectMatched=%d", v.RespectMatched)
	}

	// 但真实缺漏必须报出来。
	v2 := Validate(res, []OfficialStat{{AttackerID: 1, Hits: 1, Respect: 99.50}}, nil)
	if v2.RespectMatched != 0 {
		t.Error("0.5 的差异应被判为不一致")
	}
}
