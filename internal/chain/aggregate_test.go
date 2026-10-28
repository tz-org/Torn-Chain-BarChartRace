package chain

import (
	"math"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC)

func at(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }

func TestPlanColumns(t *testing.T) {
	tests := []struct {
		name      string
		start     time.Time
		end       time.Time
		interval  time.Duration
		wantCols  int
		wantFirst time.Time
		wantLast  time.Time
	}{
		{
			name:      "零时长只留起跑列",
			start:     at(0),
			end:       at(0),
			interval:  5 * time.Minute,
			wantCols:  1,
			wantFirst: at(0),
			wantLast:  at(0),
		},
		{
			name:      "恰好整除不补尾列",
			start:     at(0),
			end:       at(15),
			interval:  5 * time.Minute,
			wantCols:  4, // 0,5,10,15
			wantFirst: at(0),
			wantLast:  at(15),
		},
		{
			name:      "有余数时补一列 end",
			start:     at(0),
			end:       at(17),
			interval:  5 * time.Minute,
			wantCols:  5, // 0,5,10,15,17
			wantFirst: at(0),
			wantLast:  at(17),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cols, err := PlanColumns(tc.start, tc.end, tc.interval)
			if err != nil {
				t.Fatalf("PlanColumns 返回错误: %v", err)
			}
			if len(cols) != tc.wantCols {
				t.Fatalf("列数 = %d，期望 %d（cols=%v）", len(cols), tc.wantCols, cols)
			}
			if !cols[0].Equal(tc.wantFirst) {
				t.Errorf("首列 = %s，期望 %s", cols[0], tc.wantFirst)
			}
			if !cols[len(cols)-1].Equal(tc.wantLast) {
				t.Errorf("末列 = %s，期望 %s", cols[len(cols)-1], tc.wantLast)
			}
			for i := 1; i < len(cols); i++ {
				if !cols[i].After(cols[i-1]) {
					t.Fatalf("列未严格递增：cols[%d]=%s 不晚于 cols[%d]=%s", i, cols[i], i-1, cols[i-1])
				}
			}
		})
	}
}

func TestPlanColumnsRejectsBadInput(t *testing.T) {
	if _, err := PlanColumns(at(0), at(10), 0); err == nil {
		t.Error("间隔为 0 应当报错")
	}
	if _, err := PlanColumns(at(0), at(10), -time.Minute); err == nil {
		t.Error("间隔为负应当报错")
	}
	if _, err := PlanColumns(at(10), at(0), time.Minute); err == nil {
		t.Error("结束早于开始应当报错")
	}
}

func TestAggregateRespectIsCumulativeAndStartsAtZero(t *testing.T) {
	cols, err := PlanColumns(at(0), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{AttackerID: 1, Started: at(1), Respect: 10, Result: "Attacked"},
		{AttackerID: 1, Started: at(6), Respect: 5, Result: "Mugged"},
		{AttackerID: 2, Started: at(9), Respect: 7, Result: "Hospitalized"},
	}

	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricRespect, Start: at(0), End: at(10),
	})

	if res.TotalRespect != 22 {
		t.Errorf("TotalRespect = %v，期望 22", res.TotalRespect)
	}
	if len(res.Series) != 2 {
		t.Fatalf("序列数 = %d，期望 2", len(res.Series))
	}

	// 按总量降序：id 1 合计 15，id 2 合计 7。
	if res.Series[0].AttackerID != 1 {
		t.Fatalf("首条序列应为 id=1，实际 %d", res.Series[0].AttackerID)
	}
	want := []float64{0, 10, 15} // 起跑 0 → 第 5 分钟前只有 10 → 第 10 分钟累计 15
	for i, w := range want {
		if math.Abs(res.Series[0].Values[i]-w) > 1e-9 {
			t.Errorf("id=1 Values[%d] = %v，期望 %v", i, res.Series[0].Values[i], w)
		}
	}

	// 第二名在第一次命中之前必须填 0，否则 Flourish 会让他凭空出现。
	if res.Series[1].Values[0] != 0 || res.Series[1].Values[1] != 0 {
		t.Errorf("id=2 的前两列应为 0，实际 %v", res.Series[1].Values[:2])
	}
	if res.Series[1].Values[2] != 7 {
		t.Errorf("id=2 末列 = %v，期望 7", res.Series[1].Values[2])
	}
}

func TestAggregateHitsExcludesNonRespectAndAssists(t *testing.T) {
	cols, err := PlanColumns(at(0), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{AttackerID: 1, Started: at(1), Respect: 3, Result: "Attacked"},     // 计入
		{AttackerID: 1, Started: at(2), Respect: 0, Result: "Lost"},         // 无 respect，不计
		{AttackerID: 1, Started: at(3), Respect: 0, Result: "Stalemate"},    // 无 respect，不计
		{AttackerID: 1, Started: at(4), Respect: 2, Result: "Assist"},       // 助攻，不计入连击
		{AttackerID: 1, Started: at(6), Respect: 4, Result: "Hospitalized"}, // 计入
	}

	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricHits, Start: at(0), End: at(10),
	})

	// 计数口径下只保留两次真正的连击命中。
	if res.TotalHits != 2 {
		t.Errorf("TotalHits = %d，期望 2", res.TotalHits)
	}
	// 但 TotalEvents / TotalRespect 仍按「全部攻击」统计，供汇总对照。
	if res.TotalEvents != 5 {
		t.Errorf("TotalEvents = %d，期望 5", res.TotalEvents)
	}
	if res.Series[0].Total != 2 {
		t.Errorf("末列累计 = %v，期望 2", res.Series[0].Total)
	}
}

func TestAggregateDropsEventsOutsideWindow(t *testing.T) {
	cols, err := PlanColumns(at(5), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{AttackerID: 1, Started: at(4), Respect: 100, Result: "Attacked"},  // 窗口前，丢弃
		{AttackerID: 1, Started: at(5), Respect: 1, Result: "Attacked"},    // 恰在起点，保留
		{AttackerID: 1, Started: at(10), Respect: 2, Result: "Attacked"},   // 恰在终点，保留
		{AttackerID: 1, Started: at(11), Respect: 200, Result: "Attacked"}, // 窗口后，丢弃
	}

	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricRespect, Start: at(5), End: at(10),
	})

	if res.TotalEvents != 2 {
		t.Errorf("窗口内事件数 = %d，期望 2", res.TotalEvents)
	}
	if res.TotalRespect != 3 {
		t.Errorf("窗口内 respect = %v，期望 3", res.TotalRespect)
	}
}

// TestAggregateCountsFirstHitUsingEndTime 锁定一条用真实数据换来的教训：
// chainreport.start 是**首击完成**的时刻，比该次攻击的 started 晚若干秒。
// 若按 started 判窗口，首击会被判为「窗口之前」而丢掉。
func TestAggregateCountsFirstHitUsingEndTime(t *testing.T) {
	cols, err := PlanColumns(at(0), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		// 首击：started 早于窗口起点 1 分钟，但 ended 恰好等于起点。
		{AttackerID: 1, Started: at(-1), Ended: at(0), Respect: 2.27, Result: "Attacked"},
		// 上一场 chain 的攻击：ended 也早于起点，必须排除。
		{AttackerID: 2, Started: at(-5), Ended: at(-2), Respect: 99, Result: "Attacked"},
	}

	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricRespect, Start: at(0), End: at(10),
	})

	if res.TotalHits != 1 {
		t.Fatalf("TotalHits = %d，期望 1（只应保留首击）", res.TotalHits)
	}
	if _, ok := res.PerAttacker[1]; !ok {
		t.Error("首击（ended == start）必须被计入")
	}
	if _, ok := res.PerAttacker[2]; ok {
		t.Error("上一场 chain 的攻击（ended < start）必须被排除")
	}
	// 首击落在第 0 列（起点），因此第 0 列就应有值。
	if got := res.Series[0].Values[0]; got != 2.27 {
		t.Errorf("首列累计 = %v，期望 2.27", got)
	}
}

// TestAggregateCountsLastHitEndingAfterChainEnd 锁定终点侧的反向不对称：
// chainreport.end 比末次命中的 ended **早**，若用 `ended <= end` 会把收尾命中排除。
func TestAggregateCountsLastHitEndingAfterChainEnd(t *testing.T) {
	cols, err := PlanColumns(at(0), at(10), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{AttackerID: 1, Started: at(9), Ended: at(11), Respect: 40960, Result: "Attacked"},
		// 下一场 chain 的攻击：started 晚于 end，必须排除。
		{AttackerID: 2, Started: at(12), Ended: at(13), Respect: 77, Result: "Attacked"},
	}

	res := Aggregate(events, AggregateOptions{
		Columns: cols, Metric: MetricRespect, Start: at(0), End: at(10),
	})

	if res.TotalHits != 1 {
		t.Fatalf("TotalHits = %d，期望 1（只应保留跨越右边界的末击）", res.TotalHits)
	}
	if _, ok := res.PerAttacker[2]; ok {
		t.Error("下一场 chain 的攻击（started > end）必须被排除")
	}
	// ended 晚于 end 时必须夹紧到最后一列，否则这次命中会落在列外、总量偏小。
	last := res.Series[0].Values[len(res.Series[0].Values)-1]
	if last != 40960 {
		t.Errorf("末列累计 = %v，期望 40960", last)
	}
}

func TestEffectiveAtFallsBackToStarted(t *testing.T) {
	started := at(5)

	if got := (Event{Started: started}).EffectiveAt(); !got.Equal(started) {
		t.Errorf("Ended 为零时应回退到 Started，实际 %s", got)
	}
	if got := (Event{Started: started, Ended: started.Add(-time.Minute)}).EffectiveAt(); !got.Equal(started) {
		t.Errorf("Ended 早于 Started 时应回退到 Started，实际 %s", got)
	}
	if got := (Event{Started: started, Ended: started.Add(time.Second)}).EffectiveAt(); !got.Equal(started.Add(time.Second)) {
		t.Errorf("正常情况应取 Ended，实际 %s", got)
	}
}

func TestAggregateSkipsZeroAttacker(t *testing.T) {
	cols, err := PlanColumns(at(0), at(5), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	res := Aggregate([]Event{
		{AttackerID: 0, Started: at(1), Respect: 5, Result: "Attacked"},
	}, AggregateOptions{Columns: cols, Metric: MetricRespect, Start: at(0), End: at(5)})

	if len(res.Series) != 0 {
		t.Errorf("attacker id 为 0 的事件应被丢弃，实际产生 %d 条序列", len(res.Series))
	}
}

func TestParseMetric(t *testing.T) {
	for _, s := range []string{"", "respect", "RESPECT", "resp"} {
		if m, err := ParseMetric(s); err != nil || m != MetricRespect {
			t.Errorf("ParseMetric(%q) = %v, %v；期望 MetricRespect", s, m, err)
		}
	}
	for _, s := range []string{"hits", "HIT", "count"} {
		if m, err := ParseMetric(s); err != nil || m != MetricHits {
			t.Errorf("ParseMetric(%q) = %v, %v；期望 MetricHits", s, m, err)
		}
	}
	if _, err := ParseMetric("respects"); err == nil {
		t.Error("未知指标应当报错")
	}
}
