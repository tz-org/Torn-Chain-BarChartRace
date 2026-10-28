package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// Prompter 是交互式提问的最小实现。
//
// 刻意不引入 TUI 框架：本工具的交互只有五六个问题，一次问完就进长任务，
// 上重量级框架只会增加维护面而不改善体验。
type Prompter struct {
	In  io.Reader
	Out io.Writer

	reader *bufio.Reader
}

// NewPrompter 构造提问器。
func NewPrompter(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{In: in, Out: out, reader: bufio.NewReader(in)}
}

// Interactive 报告 stdin 是否是终端（决定能否做掩码输入与彩色提示）。
func (p *Prompter) Interactive() bool {
	f, ok := p.In.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// Ask 提一个有默认值的问题；用户直接回车即采用默认值。
//
// validate 非 nil 时会被反复调用直到通过 —— 让用户在输入阶段就改对，
// 而不是跑到长任务中途才因为参数非法失败。
func (p *Prompter) Ask(label, def string, validate func(string) error) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(p.Out, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(p.Out, "%s: ", label)
		}

		line, err := p.reader.ReadString('\n')
		if err != nil && line == "" {
			if err == io.EOF {
				return "", fmt.Errorf("输入中断")
			}
			return "", fmt.Errorf("读取输入失败: %w", err)
		}
		ans := strings.TrimSpace(line)
		if ans == "" {
			ans = def
		}
		if ans == "" {
			fmt.Fprintln(p.Out, "  ✗ 该项不能为空")
			continue
		}
		if validate != nil {
			if verr := validate(ans); verr != nil {
				fmt.Fprintf(p.Out, "  ✗ %v\n", verr)
				continue
			}
		}
		return ans, nil
	}
}

// AskSecret 提一个不回显的敏感问题（API key）。
//
// 终端下用 term.ReadPassword 关闭回显；非终端（管道、CI）下退回普通读取，
// 因为此时「回显」本来就不存在。
func (p *Prompter) AskSecret(label string) (string, error) {
	fmt.Fprintf(p.Out, "%s: ", label)

	f, ok := p.In.(*os.File)
	if ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.Out) // ReadPassword 不输出换行，补一个
		if err != nil {
			return "", fmt.Errorf("读取密钥失败: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}

	line, err := p.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("输入中断")
	}
	return strings.TrimSpace(line), nil
}

// Confirm 提一个是/否问题。
func (p *Prompter) Confirm(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(p.Out, "%s [%s]: ", label, hint)
		line, err := p.reader.ReadString('\n')
		if err != nil && line == "" {
			return def, nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def, nil
		case "y", "yes", "是":
			return true, nil
		case "n", "no", "否":
			return false, nil
		}
		fmt.Fprintln(p.Out, "  ✗ 请输入 y 或 n")
	}
}

// ParseChainID 解析 chain id 输入。
func ParseChainID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q 不是合法的 chain id（应为纯数字）", s)
	}
	if id <= 0 {
		return 0, fmt.Errorf("chain id 必须为正数，实际 %d", id)
	}
	return id, nil
}

// ParseInterval 解析统计间隔。
//
// 同时接受 Go 时长写法（"5m"、"90s"）与纯数字秒数（"300"），
// 因为「5 分钟」这个需求两种写法都很自然。
func ParseInterval(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("间隔不能为空")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("间隔必须为正数，实际 %d 秒", n)
		}
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("无法解析间隔 %q（示例：5m / 300 / 90s）", s)
	}
	sec := int(d.Seconds())
	if sec <= 0 {
		return 0, fmt.Errorf("间隔必须至少 1 秒，实际 %q", s)
	}
	return sec, nil
}
