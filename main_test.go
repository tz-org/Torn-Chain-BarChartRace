package main

import (
	"flag"
	"reflect"
	"testing"
)

// newTestFlagSet 构造一个与 main 注册项等价的 FlagSet，用于测试参数重排。
func newTestFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("chainrace", flag.ContinueOnError)
	var (
		s  string
		n  int64
		b1 bool
		b2 bool
		b3 bool
	)
	fs.StringVar(&s, "api", "", "")
	fs.Int64Var(&n, "chain", 0, "")
	fs.StringVar(&s, "interval", "", "")
	fs.BoolVar(&b1, "offline", false, "")
	fs.BoolVar(&b2, "refresh", false, "")
	fs.BoolVar(&b3, "yes", false, "")
	return fs
}

func TestReorderArgs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "位置参数在最后，原样保留",
			in:   []string{"-offline", "1234567"},
			want: []string{"-offline", "1234567"},
		},
		{
			name: "位置参数在前，布尔 flag 前移",
			in:   []string{"1234567", "-offline"},
			want: []string{"-offline", "1234567"},
		},
		{
			name: "分离式取值必须跟随它的 flag",
			in:   []string{"1234567", "-interval", "10m", "-offline"},
			want: []string{"-interval", "10m", "-offline", "1234567"},
		},
		{
			name: "等号式取值整体视为一个 flag",
			in:   []string{"1234567", "-interval=10m", "-metric=respect"},
			want: []string{"-interval=10m", "-metric=respect", "1234567"},
		},
		{
			name: "双横线写法",
			in:   []string{"1234567", "--offline", "--chain", "42"},
			want: []string{"--offline", "--chain", "42", "1234567"},
		},
		{
			name: "显式 -- 之后全部按位置参数处理",
			in:   []string{"-offline", "--", "1234567", "-x"},
			want: []string{"-offline", "1234567", "-x"},
		},
		{
			name: "单独的 - 是位置参数而非 flag",
			in:   []string{"-", "-offline"},
			want: []string{"-offline", "-"},
		},
		{
			name: "空参数",
			in:   nil,
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderArgs(newTestFlagSet(), tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("reorderArgs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestReorderArgsFlagsActuallyParse 验证重排之后 flag 真的能取到值 ——
// 这才是重排的目的，仅比对切片相等不足以证明。
func TestReorderArgsFlagsActuallyParse(t *testing.T) {
	fs := flag.NewFlagSet("chainrace", flag.ContinueOnError)
	var (
		interval string
		offline  bool
	)
	fs.StringVar(&interval, "interval", "", "")
	fs.BoolVar(&offline, "offline", false, "")

	if err := fs.Parse(reorderArgs(fs, []string{"1234567", "-interval", "10m", "-offline"})); err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if interval != "10m" {
		t.Errorf("interval = %q, want 10m", interval)
	}
	if !offline {
		t.Error("offline = false, want true")
	}
	if fs.NArg() != 1 || fs.Arg(0) != "1234567" {
		t.Errorf("位置参数 = %v, want [1234567]", fs.Args())
	}
}
