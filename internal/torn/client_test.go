package torn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// newCommentServer 起一个只回显请求 query 的假 API，用于断言参数真的发出去了。
func newCommentServer(t *testing.T) (*httptest.Server, *url.Values) {
	t.Helper()
	got := &url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.URL.Query()
		if r.Header.Get("Authorization") == "" {
			t.Errorf("请求缺少 Authorization 头：密钥必须走头部而不是 URL")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(Options{APIKey: "test-key", BaseURL: baseURL})
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}
	return c
}

// TestGetRawSendsComment 每个普通请求都必须带 comment，便于在 Torn 的 API 日志里
// 把这批调用与其他工具区分开。
func TestGetRawSendsComment(t *testing.T) {
	srv, got := newCommentServer(t)
	c := newTestClient(t, srv.URL)

	if _, err := c.GetRaw(context.Background(), PathKeyInfo, nil); err != nil {
		t.Fatalf("GetRaw 失败: %v", err)
	}
	if v := got.Get("comment"); v != DefaultComment {
		t.Fatalf("comment = %q, want %q", v, DefaultComment)
	}
	// 密钥只能出现在头部，绝不能落到 query 里（会进服务器访问日志）。
	if got.Has("key") {
		t.Fatal("query 里出现了 key 参数")
	}
}

// TestGetRawKeepsExistingQuery 追加 comment 不能覆盖调用方自己的参数，
// 也不能修改调用方传入的 url.Values（避免同一份参数被复用时被悄悄改写）。
func TestGetRawKeepsExistingQuery(t *testing.T) {
	srv, got := newCommentServer(t)
	c := newTestClient(t, srv.URL)

	q := url.Values{}
	q.Set("limit", "1000")
	q.Set("filters", "outgoing")
	if _, err := c.GetRaw(context.Background(), PathFactionAttacksFull, q); err != nil {
		t.Fatalf("GetRaw 失败: %v", err)
	}
	if got.Get("limit") != "1000" || got.Get("filters") != "outgoing" {
		t.Fatalf("原有参数丢失: %v", *got)
	}
	if got.Has("comment") == false {
		t.Fatal("comment 未追加")
	}
	if q.Has("comment") {
		t.Fatal("调用方传入的 url.Values 被修改了")
	}
}

// TestGetFullURLRawSendsComment 翻页用的 prev 链接来自服务端响应，
// 里面没有 comment —— 跟随前必须补上，否则翻页请求在日志里认不出来。
func TestGetFullURLRawSendsComment(t *testing.T) {
	srv, got := newCommentServer(t)
	c := newTestClient(t, srv.URL)

	full := srv.URL + PathFactionAttacksFull + "?limit=1000&filters=outgoing&sort=desc&from=1&to=2"
	if _, err := c.GetFullURLRaw(context.Background(), full); err != nil {
		t.Fatalf("GetFullURLRaw 失败: %v", err)
	}
	if v := got.Get("comment"); v != DefaultComment {
		t.Fatalf("comment = %q, want %q", v, DefaultComment)
	}
	if got.Get("to") != "2" || got.Get("filters") != "outgoing" {
		t.Fatalf("游标参数被破坏: %v", *got)
	}
}

// TestGetFullURLRawRejectsOtherOrigin 同源校验不能因为追加 comment 而被绕过。
func TestGetFullURLRawRejectsOtherOrigin(t *testing.T) {
	c := newTestClient(t, "https://api.torn.com/v2")
	if _, err := c.GetFullURLRaw(context.Background(), "https://evil.example.com/v2/faction/attacksfull"); err == nil {
		t.Fatal("跨源地址应被拒绝")
	}
}
