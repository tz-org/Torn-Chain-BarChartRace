package flourish

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"
)

func TestLabelsFormats(t *testing.T) {
	start := time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC)
	cols := []time.Time{
		start,
		start.Add(5 * time.Minute),
		start.Add(65 * time.Minute),
	}

	tests := []struct {
		format TimeFormat
		want   []string
	}{
		{TimeDateTime, []string{"2026-10-25 18:00", "2026-10-25 18:05", "2026-10-25 19:05"}},
		{TimeClock, []string{"18:00", "18:05", "19:05"}},
		{TimeElapsed, []string{"+0:00", "+0:05", "+1:05"}},
		{TimeIndex, []string{"T1", "T2", "T3"}},
	}

	for _, tc := range tests {
		got := Labels(cols, tc.format, start)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: 列头数 = %d，期望 %d", tc.format, len(got), len(tc.want))
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: Labels[%d] = %q，期望 %q", tc.format, i, got[i], tc.want[i])
			}
		}
	}
}

func TestParseTimeFormat(t *testing.T) {
	for s, want := range map[string]TimeFormat{
		"":         TimeDateTime,
		"datetime": TimeDateTime,
		"date":     TimeDateTime,
		"clock":    TimeClock,
		"time":     TimeClock,
		"elapsed":  TimeElapsed,
		"index":    TimeIndex,
	} {
		got, err := ParseTimeFormat(s)
		if err != nil || got != want {
			t.Errorf("ParseTimeFormat(%q) = %v, %v；期望 %v", s, got, err, want)
		}
	}
	if _, err := ParseTimeFormat("iso"); err == nil {
		t.Error("未知格式应当报错")
	}
}

func TestWriteCSVWideLayout(t *testing.T) {
	var buf bytes.Buffer
	err := WriteCSV(&buf, Table{
		Columns:  []string{"2026-10-25 18:00", "2026-10-25 18:05"},
		Decimals: 2,
		Rows: []Row{
			{Name: "evan1321", Values: []float64{0, 12.3456}},
			{Name: "Megistus", Values: []float64{0, 0}},
		},
	})
	if err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}

	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("输出不是合法 CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("行数 = %d，期望 3（1 表头 + 2 数据行）", len(records))
	}
	if records[0][0] != "Name" {
		t.Errorf("首列表头 = %q，期望 Name", records[0][0])
	}
	// 首列是名字，其余每列一个时间阶段 —— 这正是 Flourish 要求的宽表形态。
	if got := records[1]; got[0] != "evan1321" || got[1] != "0.00" || got[2] != "12.35" {
		t.Errorf("首行 = %v，期望 [evan1321 0.00 12.35]", got)
	}
}

func TestWriteCSVQuotesNamesWithComma(t *testing.T) {
	var buf bytes.Buffer
	err := WriteCSV(&buf, Table{
		Columns:  []string{"T1"},
		Decimals: 0,
		Rows:     []Row{{Name: "weird,name", Values: []float64{3}}},
	})
	if err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}
	if !strings.Contains(buf.String(), `"weird,name"`) {
		t.Errorf("含逗号的名字应当被引号包裹，实际输出：%s", buf.String())
	}
}

func TestWriteCSVIntegerDecimals(t *testing.T) {
	var buf bytes.Buffer
	err := WriteCSV(&buf, Table{
		Columns:  []string{"T1"},
		Decimals: 0,
		Rows:     []Row{{Name: "a", Values: []float64{42}}},
	})
	if err != nil {
		t.Fatalf("WriteCSV 失败: %v", err)
	}
	if !strings.Contains(buf.String(), "a,42") {
		t.Errorf("小数位为 0 时应输出整数，实际输出：%s", buf.String())
	}
}
