package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"Torn-Chain-BarChartRace/internal/torn"
)

func TestSlicesForWindow(t *testing.T) {
	base := int64(1_000_000)
	tests := []struct {
		name string
		from int64
		to   int64
		want int
	}{
		{"零窗口", 0, 0, 1},
		{"半小时", base, base + 1800, 1},
		{"一小时五十九分", base, base + 7140, 1},
		{"两小时", base, base + 7200, 2},
		{"七小时", base, base + 7*3600, 2},
		{"八小时", base, base + 8*3600, 4},
		{"两天", base, base + 48*3600, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slicesForWindow(tt.from, tt.to); got != tt.want {
				t.Fatalf("slicesForWindow(%d,%d) = %d, want %d", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

// TestSliceSeeds 钉住分片的边界性质：严格降序、首项等于窗口右端、末项仍高于左端。
// 只要少一条边界，就会在分片之间留下一个永不取数的缝。
func TestSliceSeeds(t *testing.T) {
	from, to := int64(1000), int64(1000+24*3600)
	for _, n := range []int{1, 2, 4} {
		seeds := sliceSeeds(from, to, n)
		if len(seeds) != n {
			t.Fatalf("n=%d 时种子数 = %d", n, len(seeds))
		}
		if seeds[0] != to {
			t.Fatalf("n=%d 首项 = %d, want %d", n, seeds[0], to)
		}
		for i := 1; i < len(seeds); i++ {
			if seeds[i] >= seeds[i-1] {
				t.Fatalf("n=%d 种子非严格降序: %v", n, seeds)
			}
		}
		if seeds[len(seeds)-1] <= from {
			t.Fatalf("n=%d 末项 %d 未高于窗口左端 %d", n, seeds[len(seeds)-1], from)
		}
	}
}

// ───────────────────────── 伪 API：复刻 attacksfull 的游标语义 ─────────────────────────

type fakeAttack struct {
	ID         int64   `json:"id"`
	Code       string  `json:"code"`
	Started    int64   `json:"started"`
	Ended      int64   `json:"ended"`
	Result     string  `json:"result"`
	Respect    float64 `json:"respect_gain"`
	AttackerID int64   `json:"-"`
}

func (a fakeAttack) marshal() map[string]any {
	return map[string]any{
		"id": a.ID, "code": a.Code, "started": a.Started, "ended": a.Ended,
		"result": a.Result, "respect_gain": a.Respect,
		"attacker":     map[string]any{"id": a.AttackerID, "faction_id": 777},
		"defender":     map[string]any{"id": 1, "faction_id": 888},
		"respect_loss": 0,
	}
}

type fakeAPI struct {
	mu       sync.Mutex
	all      []fakeAttack // 按 ended 降序，与服务端顺序一致
	served   int          // 累计返回的记录数（含边界重复）
	requests int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, _ := strconv.ParseInt(q.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(q.Get("to"), 10, 64)
	limit := 1000
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = v
	}

	var matched []fakeAttack
	for _, a := range f.all {
		if a.Ended >= from && a.Ended <= to {
			matched = append(matched, a)
		}
	}
	truncated := len(matched) > limit
	if truncated {
		matched = matched[:limit]
	}

	var prev *string
	if truncated && len(matched) > 0 {
		// 与真实服务端一致：prev 的 to 取当页最小 ended（闭区间，会重复边界那条）。
		link := fmt.Sprintf("http://%s/v2/faction/attacksfull?from=%d&to=%d&limit=%d&sort=desc&filters=outgoing",
			r.Host, from, matched[len(matched)-1].Ended, limit)
		prev = &link
	}

	f.mu.Lock()
	f.served += len(matched)
	f.requests++
	f.mu.Unlock()

	attacks := make([]map[string]any, 0, len(matched))
	for _, a := range matched {
		attacks = append(attacks, a.marshal())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"attacks":   attacks,
		"_metadata": map[string]any{"links": map[string]any{"prev": prev, "next": nil}},
	})
}

func newFakeAPI(n int, start time.Time, span time.Duration) (*fakeAPI, []fakeAttack) {
	api := &fakeAPI{}
	// 按时间升序生成，再反转成降序（服务端默认顺序）。
	step := span / time.Duration(n)
	for i := 0; i < n; i++ {
		ended := start.Add(time.Duration(i) * step)
		api.all = append(api.all, fakeAttack{
			ID:         int64(10_000 + i),
			Code:       fmt.Sprintf("c%d", i),
			Started:    ended.Add(-3 * time.Second).Unix(),
			Ended:      ended.Unix(),
			Result:     "Attacked",
			Respect:    1.5,
			AttackerID: int64(100 + i%7),
		})
	}
	for i, j := 0, len(api.all)-1; i < j; i, j = i+1, j-1 {
		api.all[i], api.all[j] = api.all[j], api.all[i]
	}
	return api, api.all
}

func newCollector(t *testing.T, api *fakeAPI, root string) *Collector {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	c, err := torn.NewClient(torn.Options{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	return &Collector{
		Client: c,
		Cache:  &Cache{Root: root},
		Log:    func(string, ...any) {}, // 进度日志在并发下会刷屏，测试里静音
	}
}

// TestSlicedFetchCoversEverything 是本功能的核心断言：
// 分片并发取回的数据必须与「全量」逐 id 一致，不允许有任何缺口。
func TestSlicedFetchCoversEverything(t *testing.T) {
	const n = 9000
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	api, all := newFakeAPI(n, start, 24*time.Hour)
	root := t.TempDir()
	col := newCollector(t, api, root)

	from := start.Add(-10 * time.Minute).Unix()
	to := start.Add(24*time.Hour + 10*time.Minute).Unix()
	res, err := col.Attacks(context.Background(), AttacksOptions{
		ChainID: 42,
		Query: torn.AttacksQuery{
			Filters: []string{"outgoing"}, Limit: torn.MaxAttacksFullLimit,
			Sort: "DESC", From: from, To: to,
		},
		StopWhenPageOlderThan: start.Unix(),
	})
	if err != nil {
		t.Fatalf("Attacks 失败: %v", err)
	}

	if res.Partial {
		t.Fatalf("不应标记不完整：%v", res.Warnings)
	}
	if len(res.Attacks) != n {
		t.Fatalf("取到 %d 条，want %d（缺口 %d）", len(res.Attacks), n, n-len(res.Attacks))
	}
	got := make(map[int64]bool, len(res.Attacks))
	for _, a := range res.Attacks {
		if got[a.ID] {
			t.Fatalf("攻击 %d 重复出现，去重失败", a.ID)
		}
		got[a.ID] = true
	}
	for _, want := range all {
		if !got[want.ID] {
			t.Fatalf("缺少攻击 %d（ended=%d）", want.ID, want.Ended)
		}
	}

	// 4 个分片、每个约 3 页，总请求数应与「每片独立取满」相符，而不是 1 条游标链。
	if res.Pages < 9 {
		t.Fatalf("总页数 %d 偏少，分片可能没有真正并发展开", res.Pages)
	}
	if api.served <= n {
		t.Fatalf("边界重叠没有发生（返回 %d 条），测试未覆盖去重路径", api.served)
	}

	// 清单必须记下布局与各片页数，否则离线重放会读错文件。
	var man attacksManifest
	raw, ok := col.Cache.Read(col.Cache.attacksManifestPath(42))
	if !ok {
		t.Fatal("缓存清单未写出")
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("解析清单失败: %v", err)
	}
	if man.Layout != layoutSliced || man.Slices != 4 || len(man.SlicePages) != 4 || !man.Complete {
		t.Fatalf("清单内容异常: %+v", man)
	}

	// 离线重放必须得到同一结果，且不再发请求。
	before := api.requests
	offline := newCollector(t, api, root)
	offline.Offline = true
	res2, err := offline.Attacks(context.Background(), AttacksOptions{
		ChainID: 42,
		Query: torn.AttacksQuery{
			Filters: []string{"outgoing"}, Limit: torn.MaxAttacksFullLimit,
			Sort: "DESC", From: from, To: to,
		},
	})
	if err != nil {
		t.Fatalf("离线重放失败: %v", err)
	}
	if len(res2.Attacks) != n {
		t.Fatalf("离线重放取到 %d 条，want %d", len(res2.Attacks), n)
	}
	if api.requests != before {
		t.Fatalf("离线模式仍发出了 %d 次请求", api.requests-before)
	}
}

// TestSerialFetchWritesLegacyLayout 短窗口必须继续走旧布局，
// 否则老缓存（page-0001.json）会在一夜之间失效。
func TestSerialFetchWritesLegacyLayout(t *testing.T) {
	const n = 500
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	api, _ := newFakeAPI(n, start, 15*time.Minute) // 15 分钟窗口，远小于分片阈值
	root := t.TempDir()
	col := newCollector(t, api, root)

	from := start.Add(-time.Minute).Unix()
	to := start.Add(20 * time.Minute).Unix()
	res, err := col.Attacks(context.Background(), AttacksOptions{
		ChainID: 7,
		Query:   torn.AttacksQuery{Filters: []string{"outgoing"}, Limit: 1000, Sort: "DESC", From: from, To: to},
	})
	if err != nil {
		t.Fatalf("Attacks 失败: %v", err)
	}
	if len(res.Attacks) == 0 {
		t.Fatal("未取到数据")
	}

	var man attacksManifest
	raw, _ := col.Cache.Read(col.Cache.attacksManifestPath(7))
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("解析清单失败: %v", err)
	}
	if man.Layout != layoutSerial {
		t.Fatalf("短窗口应写旧布局，实际 layout=%q", man.Layout)
	}
	if _, ok := col.Cache.Read(col.Cache.attacksPagePath(7, 1)); !ok {
		t.Fatal("旧布局的 page-0001.json 未写出")
	}

	offline := newCollector(t, api, root)
	offline.Offline = true
	res2, err := offline.Attacks(context.Background(), AttacksOptions{
		ChainID: 7,
		Query:   torn.AttacksQuery{Filters: []string{"outgoing"}, Limit: 1000, Sort: "DESC", From: from, To: to},
	})
	if err != nil {
		t.Fatalf("离线重放失败: %v", err)
	}
	if len(res2.Attacks) != len(res.Attacks) {
		t.Fatalf("离线重放 %d 条，在线 %d 条", len(res2.Attacks), len(res.Attacks))
	}
}
