package attacklog

import (
	"bytes"
	"encoding/csv"
	"reflect"
	"testing"
	"time"

	"Torn-Chain-BarChartRace/internal/torn"
)

// cst 固定时区，让 RFC3339 断言不随运行环境的本地时区变化。
var cst = time.FixedZone("CST", 8*3600)

func at(hour, minute int) time.Time {
	return time.Date(2026, 10, 25, hour, minute, 0, 0, cst)
}

// mk 构造一条「发起于 started、3 秒后结束」的攻击。
func mk(id int64, started time.Time, result string, respect float64, attackerID, factionID int64) torn.Attack {
	return torn.Attack{
		ID:                id,
		Code:              "abc123",
		Started:           started,
		Ended:             started.Add(3 * time.Second),
		Result:            result,
		RespectGain:       respect,
		AttackerID:        attackerID,
		AttackerFactionID: factionID,
	}
}

func readCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("解析输出 CSV 失败: %v", err)
	}
	return rows
}

func TestWriteCSVHeader(t *testing.T) {
	var buf bytes.Buffer
	if _, err := WriteCSV(&buf, nil, Options{Start: at(18, 0), End: at(19, 0)}); err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}
	rows := readCSV(t, buf.Bytes())
	if len(rows) != 1 {
		t.Fatalf("只有表头时应有 1 行，实际 %d 行: %v", len(rows), rows)
	}
	want := []string{
		"AttackID", "Code", "AttackerID", "Name",
		"Started", "Ended", "Result", "RespectGain", "Counted",
	}
	if !reflect.DeepEqual(rows[0], want) {
		t.Fatalf("表头 = %v, want %v", rows[0], want)
	}
}

// TestWriteCSVDedupesAndOrders 钉住两条：同一攻击 id 只写一次；行按开始时间升序。
// 跨页重复是服务端分页边界的正常现象，重复行会让「行数 = 攻击总数」这条不变量失效。
func TestWriteCSVDedupesAndOrders(t *testing.T) {
	attacks := []torn.Attack{
		mk(3, at(18, 10), "Attacked", 1.5, 1, 100),
		mk(1, at(18, 0), "Attacked", 2.0, 1, 100),
		mk(2, at(18, 5), "Attacked", 3.0, 2, 100),
		mk(2, at(18, 5), "Attacked", 3.0, 2, 100), // 跨页重复
	}

	var buf bytes.Buffer
	st, err := WriteCSV(&buf, attacks, Options{
		Start:     at(18, 0),
		End:       at(18, 30),
		FactionID: 100,
	})
	if err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}
	if st.Rows != 3 || st.Duplicates != 1 {
		t.Fatalf("Rows=%d Duplicates=%d, want 3 / 1", st.Rows, st.Duplicates)
	}

	rows := readCSV(t, buf.Bytes())
	var ids []string
	for _, r := range rows[1:] {
		ids = append(ids, r[0])
	}
	if want := []string{"1", "2", "3"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("攻击 id 顺序 = %v, want %v", ids, want)
	}
}

// TestWriteCSVSelection 钉住导出范围：只保留与 chain 窗口相交、且属于本帮派的攻击；
// 窗口内失败的攻击要保留（它是「我方发起」的一部分），但不计入 Counted。
func TestWriteCSVSelection(t *testing.T) {
	attacks := []torn.Attack{
		// 上一场 chain 的收尾攻击：ended 早于 start，排除。
		{ID: 10, Started: at(17, 55), Ended: at(17, 59), Result: "Attacked", RespectGain: 9, AttackerID: 1, AttackerFactionID: 100},
		// 首击跨越左边界：started 早于 start 但 ended == start，按区间相交保留。
		{ID: 11, Started: at(17, 58), Ended: at(18, 0), Result: "Attacked", RespectGain: 2.27, AttackerID: 1, AttackerFactionID: 100},
		// 下一场 chain 的攻击：started 晚于 end，排除。
		{ID: 12, Started: at(18, 21), Ended: at(18, 22), Result: "Attacked", RespectGain: 5, AttackerID: 1, AttackerFactionID: 100},
		// 其他帮派的攻击（退帮成员的历史记录），排除。
		mk(13, at(18, 5), "Attacked", 4, 2, 200),
		// 窗口内打输：要保留行，但不算计入统计。
		mk(14, at(18, 6), "Lost", 0, 1, 100),
		// 窗口内命中：计入统计。
		mk(15, at(18, 7), "Attacked", 3.14, 1, 100),
	}

	var buf bytes.Buffer
	st, err := WriteCSV(&buf, attacks, Options{
		Start:     at(18, 0),
		End:       at(18, 20),
		FactionID: 100,
	})
	if err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}
	if st.Rows != 3 {
		t.Fatalf("Rows=%d, want 3（边界首击 + 打输 + 命中）", st.Rows)
	}
	// 边界首击与窗口内命中都计入统计：前者与窗口相交，后者完全落在窗口内。
	// 把 2.27 + 3.14 加在一起，顺带盯住「Counted 的 respect 必须能对上汇总」。
	if st.Counted != 2 || st.CountedRespect != 5.41 {
		t.Fatalf("Counted=%d CountedRespect=%v, want 2 / 5.41", st.Counted, st.CountedRespect)
	}

	rows := readCSV(t, buf.Bytes())
	var got []string
	for _, r := range rows[1:] {
		got = append(got, r[0]+":"+r[8]) // AttackID:Counted
	}
	want := []string{"11:true", "14:false", "15:true"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("(id:counted) = %v, want %v", got, want)
	}
}

// TestWriteCSVZeroEndedFallsBackToStarted 攻击缺 ended 时以 started 判定归属，
// 与 chain.Event.Overlaps 的回退规则保持一致。
func TestWriteCSVZeroEndedFallsBackToStarted(t *testing.T) {
	attacks := []torn.Attack{{
		ID: 20, Started: at(18, 5), Result: "Attacked", RespectGain: 1, AttackerID: 1, AttackerFactionID: 100,
	}}

	var buf bytes.Buffer
	st, err := WriteCSV(&buf, attacks, Options{Start: at(18, 0), End: at(18, 20), FactionID: 100})
	if err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}
	if st.Rows != 1 {
		t.Fatalf("Rows=%d, want 1", st.Rows)
	}
	rows := readCSV(t, buf.Bytes())
	if rows[1][5] != "" {
		t.Fatalf("Ended 缺失时应留空，实际 %q", rows[1][5])
	}
}

// TestWriteCSVNamesAndTimes 名字缺失时留空（绝不编造 User<id>），
// 含逗号的名字必须被正确转义，时间用带时区偏移的 RFC3339。
func TestWriteCSVNamesAndTimes(t *testing.T) {
	attacks := []torn.Attack{
		mk(30, at(18, 5), "Attacked", 2.27, 7, 100),
		mk(31, at(18, 6), "Lost", 0, 8, 100),
	}

	var buf bytes.Buffer
	if _, err := WriteCSV(&buf, attacks, Options{
		Start:     at(18, 0),
		End:       at(18, 20),
		FactionID: 100,
		Names:     map[int64]string{7: "Doe, John"},
	}); err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}

	rows := readCSV(t, buf.Bytes())
	if rows[1][3] != "Doe, John" {
		t.Fatalf("Name = %q, want %q", rows[1][3], "Doe, John")
	}
	if rows[2][3] != "" {
		t.Fatalf("查不到名字时应留空，实际 %q", rows[2][3])
	}
	if got, want := rows[1][4], "2026-10-25T18:05:00+08:00"; got != want {
		t.Fatalf("Started = %q, want %q", got, want)
	}
	if got, want := rows[1][7], "2.27"; got != want {
		t.Fatalf("RespectGain = %q, want %q", got, want)
	}
}
