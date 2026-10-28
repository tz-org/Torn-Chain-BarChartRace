// Package flourish 把聚合结果渲染成 Flourish「Bar chart race」模板能直接读取的 CSV。
//
// 数据格式依据 Flourish 官方说明（app.flourish.studio/@flourish/bar-chart-race）：
//
//	「用第一列填参与者名字，其余每一列是竞赛的一个『阶段』—— 比如某个日期、某一周或某一年」
//	示例：name, Day 1, Day 2, Day 3
//
// 即**宽表**：一行一个参与者，一列一个时间阶段。列头就是时间轴上的刻度标签，
// 所以列头的可读性直接决定了成图的时间轴质量。
package flourish

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TimeFormat 是时间列头的呈现方式。
type TimeFormat int

const (
	// TimeDateTime 输出 "2026-10-25 18:04"。默认值：唯一无歧义的选择。
	TimeDateTime TimeFormat = iota
	// TimeClock 只输出 "18:04"，适合单日内的短链。
	TimeClock
	// TimeElapsed 输出相对开始的 "+0:05"，适合强调「打到第几分钟」。
	TimeElapsed
	// TimeIndex 输出 "T1/T2/…"，完全不暴露时间，纯阶段序号。
	TimeIndex
)

// String 返回格式名，用于日志与文件名。
func (f TimeFormat) String() string {
	switch f {
	case TimeClock:
		return "clock"
	case TimeElapsed:
		return "elapsed"
	case TimeIndex:
		return "index"
	default:
		return "datetime"
	}
}

// ParseTimeFormat 解析用户输入的时间列头格式。
func ParseTimeFormat(s string) (TimeFormat, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "datetime", "date":
		return TimeDateTime, nil
	case "clock", "time":
		return TimeClock, nil
	case "elapsed", "relative":
		return TimeElapsed, nil
	case "index", "stage", "t":
		return TimeIndex, nil
	}
	return 0, fmt.Errorf("未知时间格式 %q（可选：datetime / clock / elapsed / index）", s)
}

// Labels 把时间轴刻度渲染成列头。
//
// 同一份数据用不同格式渲染，得到的是同一张图的不同时间轴外观，
// 因此这里做成纯函数，方便在测试里逐一钉死。
func Labels(cols []time.Time, format TimeFormat, start time.Time) []string {
	out := make([]string, len(cols))
	for i, t := range cols {
		switch format {
		case TimeClock:
			out[i] = t.Format("15:04")
		case TimeElapsed:
			out[i] = elapsedLabel(t.Sub(start))
		case TimeIndex:
			out[i] = "T" + strconv.Itoa(i+1)
		default:
			out[i] = t.Format("2006-01-02 15:04")
		}
	}
	return out
}

// elapsedLabel 把时长渲染成 "+H:MM"。
//
// 用 H:MM 而不是 "HH:MM:SS"：连击的时间尺度通常以分钟计，
// 秒级精度只会让列头变长而不增加信息。
func elapsedLabel(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Minute) / time.Minute)
	return fmt.Sprintf("+%d:%02d", total/60, total%60)
}

// Row 是宽表里的一行。
type Row struct {
	Name   string
	Values []float64
}

// Table 是一张完整的宽表。
type Table struct {
	// Columns 是除名字列以外的时间列头。
	Columns []string
	Rows    []Row
	// Decimals 是数值保留的小数位；0 表示输出整数。
	Decimals int
}

// WriteCSV 把宽表写成 CSV。
//
// 首列列名固定为 "Name" —— Flourish 默认把第 0 列绑定为 label，
// 列名叫什么不影响绑定，但写成 Name 能让数据表在 Flourish 里自解释。
func WriteCSV(w io.Writer, t Table) error {
	cw := csv.NewWriter(w)

	header := make([]string, 0, len(t.Columns)+1)
	header = append(header, "Name")
	header = append(header, t.Columns...)
	if err := cw.Write(header); err != nil {
		return fmt.Errorf("写入 CSV 表头失败: %w", err)
	}

	record := make([]string, len(header))
	for _, row := range t.Rows {
		record[0] = row.Name
		for i := range t.Columns {
			var v float64
			if i < len(row.Values) {
				v = row.Values[i]
			}
			record[i+1] = strconv.FormatFloat(v, 'f', t.Decimals, 64)
		}
		if err := cw.Write(record); err != nil {
			return fmt.Errorf("写入 CSV 行 %q 失败: %w", row.Name, err)
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("写出 CSV 失败: %w", err)
	}
	return nil
}

// WriteFile 把宽表写入磁盘，必要时创建父目录。
func WriteFile(path string, t Table) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建文件 %s 失败: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if err := WriteCSV(f, t); err != nil {
		return err
	}
	return f.Close()
}
