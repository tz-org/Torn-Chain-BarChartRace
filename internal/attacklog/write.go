// Package attacklog 把「本次参与统计的攻击」逐条导出成 CSV，作为图表之外的原始凭据。
//
// 它不是 Flourish 的数据源（那是 internal/flourish 的宽表），用途是排查与留档：
// 汇总数字只能证明「总量看起来对」，逐条记录才能回答「某一次攻击到底算没算进去」。
//
// 因此这里的筛选口径必须与聚合输入**完全一致** —— 与 chain 窗口区间相交、
// 且属于本帮派 —— 让导出结果与汇总数字可以互相印证：
//
//	导出行数        == 汇总里的「我方发起攻击」总数（含打输、僵持等未计入的）
//	Counted 为 true == 汇总里的「成功攻击」数
//	Counted 的 respect 合计 == 汇总里的成功攻击 respect
package attacklog

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"Torn-Chain-BarChartRace/internal/chain"
	"Torn-Chain-BarChartRace/internal/torn"
)

// header 是导出文件的列名。
//
// Counted 而不是「Successful」：它表达的是「本次统计是否计入」，
// 已经把窗口与帮派条件一并算进去，因此可以直接与汇总数字对账。
var header = []string{
	"AttackID", "Code", "AttackerID", "Name",
	"Started", "Ended", "Result", "RespectGain", "Counted",
}

// Options 控制导出的筛选与呈现。
type Options struct {
	// Start / End 是 chain 的闭区间。只有与它**区间相交**的攻击才会导出。
	//
	// 判据与聚合完全一致（chain.Event.Overlaps），不是「时刻落在窗口内」——
	// 首击的 started 会早于 chain.start，末击的 ended 会晚于 chain.end，
	// 用时刻判定会刚好丢掉这两条（见 README 7.3）。
	Start, End time.Time

	// FactionID 是 chain 所属帮派。其他帮派的攻击不导出：
	// 它们不会进入图表，混进来只会让「行数 = 攻击总数」这条对账关系失效。
	FactionID int64

	// Names 是 attacker id → 显示名。查不到时该列留空 ——
	// 在这份以核对为目的的文件里，编造 "User<id>" 比留空更容易被误读成真实名字。
	Names map[int64]string
}

// Stats 是一次导出的统计结果，用于在终端上交叉核对。
type Stats struct {
	// Rows 是写出的行数（去重后、且已按窗口与帮派筛选）。
	Rows int
	// Duplicates 是被剔除的重复记录数（同一攻击 id 出现多次）。
	Duplicates int
	// Counted 是其中计入统计的行数，CountedRespect 是它们的 respect 合计。
	Counted        int
	CountedRespect float64
}

// WriteFile 把攻击记录写入磁盘，必要时创建父目录。
func WriteFile(path string, attacks []torn.Attack, opt Options) (Stats, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Stats{}, fmt.Errorf("创建输出目录失败: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return Stats{}, fmt.Errorf("创建文件 %s 失败: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	st, err := WriteCSV(f, attacks, opt)
	if err != nil {
		return Stats{}, err
	}
	return st, f.Close()
}

// WriteCSV 把筛选后的攻击记录写成 CSV。
func WriteCSV(w io.Writer, attacks []torn.Attack, opt Options) (Stats, error) {
	rows, dup := selectAttacks(attacks, opt)
	st := Stats{Rows: len(rows), Duplicates: dup}

	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return Stats{}, fmt.Errorf("写入 CSV 表头失败: %w", err)
	}

	record := make([]string, len(header))
	for _, a := range rows {
		counted := a.AttackerID != 0 && chain.IsSuccessful(chain.Event{Result: a.Result})
		if counted {
			st.Counted++
			st.CountedRespect += a.RespectGain
		}

		record[0] = strconv.FormatInt(a.ID, 10)
		record[1] = a.Code
		record[2] = strconv.FormatInt(a.AttackerID, 10)
		record[3] = opt.Names[a.AttackerID]
		record[4] = formatTime(a.Started)
		record[5] = formatTime(a.Ended)
		record[6] = a.Result
		// 用 -1 精度原样写出，不做四舍五入：这是原始凭据，任何舍入都会让
		// 「逐条相加 == 汇总值」在末位上对不上。
		record[7] = strconv.FormatFloat(a.RespectGain, 'f', -1, 64)
		record[8] = strconv.FormatBool(counted)

		if err := cw.Write(record); err != nil {
			return Stats{}, fmt.Errorf("写入攻击 %d 失败: %w", a.ID, err)
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		return Stats{}, fmt.Errorf("写出 CSV 失败: %w", err)
	}
	return st, nil
}

// selectAttacks 按「筛选 → 去重 → 排序」处理。
//
// 去重按攻击 id 判等，与输入顺序无关；排序用 (started, id) 双键，
// 同一秒内的多条攻击也有确定顺序，因此同一份缓存任意次运行输出逐字节相同。
func selectAttacks(attacks []torn.Attack, opt Options) ([]torn.Attack, int) {
	out := make([]torn.Attack, 0, len(attacks))
	seen := make(map[int64]struct{}, len(attacks))
	dup := 0

	for _, a := range attacks {
		if !inWindow(a, opt.Start, opt.End) || !sameFaction(a, opt.FactionID) {
			continue
		}
		// id 为 0 说明响应缺字段，无法判定是否重复 —— 保留，宁可多写也不静默丢弃。
		if a.ID != 0 {
			if _, ok := seen[a.ID]; ok {
				dup++
				continue
			}
			seen[a.ID] = struct{}{}
		}
		out = append(out, a)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.Before(out[j].Started)
		}
		return out[i].ID < out[j].ID
	})
	return out, dup
}

// inWindow 报告攻击区间是否与 chain 窗口相交。窗口未确定时一律不导出。
func inWindow(a torn.Attack, start, end time.Time) bool {
	if start.IsZero() || end.IsZero() {
		return false
	}
	return chain.Event{Started: a.Started, Ended: a.Ended}.Overlaps(start, end)
}

// sameFaction 与 cli.toEvents 保持同一判据：帮派 id 缺失（0）时不据此排除。
func sameFaction(a torn.Attack, factionID int64) bool {
	return a.AttackerFactionID == 0 || factionID == 0 || a.AttackerFactionID == factionID
}

// formatTime 用带时区偏移的 RFC3339 输出，避免跨时区核对时产生歧义。
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
