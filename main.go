// Command chainrace 从 Torn 的 chain 数据生成 Flourish「Bar chart race」可用的 CSV。
//
// 用法速览：
//
//	chainrace                         # 全交互：逐步询问 key、chain id、间隔、指标
//	chainrace -chain 1234567         # 只问缺的部分
//	chainrace -api KEY -chain 1234567 -interval 5m
//	chainrace -chain 1234567 -offline # 不联网，用本地缓存重算
//
// 详见 README.md。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"Torn-Chain-BarChartRace/internal/cli"
)

// version 由构建时通过 -ldflags "-X main.version=..." 覆盖。
var version = "dev"

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\n已取消。")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "\n✗ %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		apiKey       string
		chainID      int64
		intervalRaw  string
		metric       string
		outDir       string
		timeFormat   string
		cacheDir     string
		refresh      bool
		offline      bool
		resolveNames bool
		rateLimit    int
		noSave       bool
		yes          bool
		showVersion  bool
	)

	flag.StringVar(&apiKey, "api", "", "Torn API key（也可用环境变量 TORN_API_KEY）")
	flag.Int64Var(&chainID, "chain", 0, "chain id（省略则交互询问）")
	flag.StringVar(&intervalRaw, "interval", "", "统计间隔，如 5m / 300 / 90s（默认 5m）")
	flag.StringVar(&metric, "metric", "", "指标：respect / hits / both（默认 both）")
	flag.StringVar(&outDir, "out", "", "输出目录（默认 out）")
	flag.StringVar(&timeFormat, "time-format", "", "时间列头格式：datetime / clock / elapsed / index（默认 datetime）")
	flag.StringVar(&cacheDir, "cache", "", "缓存目录（默认 .chainrace-cache）")
	flag.BoolVar(&refresh, "refresh", false, "忽略缓存，强制重新抓取")
	flag.BoolVar(&offline, "offline", false, "只用本地缓存，不发起任何请求")
	flag.BoolVar(&resolveNames, "resolve-names", true, "成员名单里查不到时逐个查询用户名")
	flag.IntVar(&rateLimit, "rate", 0, "每分钟请求上限（默认 90，Torn 官方上限 100）")
	flag.BoolVar(&noSave, "no-save", false, "不把 API key 写入配置文件")
	flag.BoolVar(&yes, "yes", false, "不做任何交互（缺参数则直接报错）")
	flag.BoolVar(&showVersion, "version", false, "打印版本号后退出")

	flag.Usage = usage
	// Go 的 flag 包遇到第一个非 flag 参数就停止解析，
	// 于是 `chainrace 1234567 -offline` 会静默忽略 -offline —— 而 chain id 前置
	// 正是 README 推荐写法。先重排再解析，两种顺序都能生效。
	_ = flag.CommandLine.Parse(reorderArgs(flag.CommandLine, os.Args[1:]))

	if showVersion {
		fmt.Printf("chainrace %s\n", version)
		return nil
	}

	// 允许把 chain id 作为位置参数传入，`chainrace 1234567` 比 `-chain` 顺手。
	if chainID <= 0 && flag.NArg() > 0 {
		id, err := cli.ParseChainID(flag.Arg(0))
		if err != nil {
			return err
		}
		chainID = id
	}

	var interval time.Duration
	if intervalRaw != "" {
		sec, err := cli.ParseInterval(intervalRaw)
		if err != nil {
			return err
		}
		interval = time.Duration(sec) * time.Second
	}

	// Ctrl+C 要能立刻停下正在进行的翻页，而不是等当前请求超时。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opt := cli.Options{
		APIKey:       apiKey,
		ChainID:      chainID,
		Interval:     interval,
		Metric:       metric,
		OutDir:       outDir,
		TimeFormat:   timeFormat,
		CacheDir:     cacheDir,
		Refresh:      refresh,
		Offline:      offline,
		ResolveNames: resolveNames,
		RateLimit:    rateLimit,
		NoSave:       noSave,
		AssumeYes:    yes,
	}
	return cli.Run(ctx, opt, os.Stdin, os.Stdout)
}

// reorderArgs 把 argv 重排成「所有 flag 在前、位置参数在后」。
//
// 动机：flag 包在遇到第一个非 flag 参数时停止解析，所以
// `chainrace 1234567 -offline` 会把 -offline 当成位置参数静默丢弃 ——
// 既不报错也不生效，属于最难察觉的一类缺陷。
func reorderArgs(fs *flag.FlagSet, argv []string) []string {
	flags := make([]string, 0, len(argv))
	positional := make([]string, 0, 1)

	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			// 显式分隔符之后的一律是位置参数。
			positional = append(positional, argv[i+1:]...)
			break
		}
		// 只有 "-x" / "--x" 才算 flag；单独的 "-" 是位置参数。
		if len(a) < 2 || a[0] != '-' {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)

		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") || isBoolFlag(fs, name) {
			continue
		}
		// 分离式取值（-api KEY）：下一项是它的值，必须一并前移，
		// 否则重排后会变成两个互不相干的位置参数。
		if i+1 < len(argv) {
			i++
			flags = append(flags, argv[i])
		}
	}
	return append(flags, positional...)
}

// isBoolFlag 判断某个已注册的 flag 是否为布尔型 —— 布尔型不消费下一个参数。
func isBoolFlag(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

func usage() {
	fmt.Fprintf(os.Stderr, `chainrace %s —— 把 Torn chain 数据导出成 Flourish 柱状图竞赛（bar chart race）CSV

用法：
  chainrace [选项] [chain-id]

示例：
  chainrace                                 交互式：依次询问 key、chain id、间隔、指标
  chainrace 1234567                        只补问缺失项
  chainrace -chain 1234567 -interval 10m   10 分钟一桶
  chainrace -chain 1234567 -metric respect 只出 respect 口径
  chainrace -chain 1234567 -offline        用本地缓存重算，零配额

选项：
`, version)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
权限要求：
  Torn API key 需至少 Limited Access，并勾选 Faction → Attacks、Chain report、Members。

输出：
  out/chain-<id>-respect.csv  累计 respect（宽表：首列 Name，其余每列一个时间阶段）
  out/chain-<id>-hits.csv     累计连击命中数
`)
}
