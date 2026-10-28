package torn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultUserAgent 是默认 UA。
//
// Torn 前置了 Cloudflare，默认 Go UA（Go-http-client/1.1）会被拦，
// 因此必须带上可识别的自有标识。
const DefaultUserAgent = "Torn-Chain-BarChartRace/1.0 (+https://github.com/tz-org/Torn-Chain-BarChartRace)"

// 退避与重试参数。
const (
	// DefaultMaxAttempts 是单次请求的总尝试次数（含首次）。
	DefaultMaxAttempts = 4
	// defaultBaseBackoff 是指数退避的基数。
	defaultBaseBackoff = 600 * time.Millisecond
	// defaultMaxBackoff 是单次退避的上限。
	defaultMaxBackoff = 8 * time.Second
	// rateLimitBackoff 是收到错误码 5 后的固定等待（等一个完整窗口滑过）。
	rateLimitBackoff = 61 * time.Second
	// ipBlockBackoff 是收到错误码 8 后的固定等待。
	ipBlockBackoff = 5 * time.Minute
	// maxResponseBytes 限制单次响应体大小，防止异常响应把内存打爆。
	maxResponseBytes = 32 << 20 // 32 MiB
)

// Options 是构造客户端所需的参数。
type Options struct {
	BaseURL   string
	UserAgent string
	Timeout   time.Duration

	// APIKey 是 Torn API key。走 Authorization 头，不出现在 URL 里。
	APIKey string

	// Limiter 是限流器。nil 表示不限制（仅供测试）。
	Limiter *Limiter

	// MaxAttempts 覆盖默认重试次数；0 表示使用 DefaultMaxAttempts。
	MaxAttempts int
}

// Client 是 Torn API v2 的 HTTP 客户端。
//
// 职责单一：过限流 → 发请求 → 判业务错误 → 按需退避重试。
// 解析与缓存都不在这里，调用方拿到原始字节后自行处理（raw-first）。
type Client struct {
	http        *http.Client
	baseURL     string
	userAgent   string
	apiKey      string
	limiter     *Limiter
	maxAttempts int

	clock func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// Response 是一次成功请求的结果。
//
// 保留原始字节是为了 raw-first：调用方可以把 Body 原样落进缓存，
// 这样以后改口径或解析逻辑只需离线重算，不必再消耗一次 API 配额。
type Response struct {
	StatusCode int
	Body       []byte
	FetchedAt  time.Time
	Attempts   int
	Cached     bool
}

// StatusError 表示 HTTP 状态码不是 200。
//
// Torn 把正常结果和业务错误都放在 200 里返回，所以出现非 200 基本意味着
// 网关、CDN 或网络层出了问题，而不是参数写错了。
type StatusError struct {
	Code   int
	Status string
	Body   string
}

// Error 实现 error 接口。
func (e *StatusError) Error() string {
	return fmt.Sprintf("Torn HTTP %d %s: %s", e.Code, e.Status, truncate(e.Body, 200))
}

// NewClient 构造客户端并校验必要参数。
func NewClient(opt Options) (*Client, error) {
	if strings.TrimSpace(opt.APIKey) == "" {
		return nil, errors.New("torn: Options.APIKey 不能为空")
	}

	base := strings.TrimRight(strings.TrimSpace(opt.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	ua := strings.TrimSpace(opt.UserAgent)
	if ua == "" {
		ua = DefaultUserAgent
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	attempts := opt.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}

	return &Client{
		http:        &http.Client{Timeout: timeout},
		baseURL:     base,
		userAgent:   ua,
		apiKey:      strings.TrimSpace(opt.APIKey),
		limiter:     opt.Limiter,
		maxAttempts: attempts,
		clock:       time.Now,
		sleep:       sleepCtx,
	}, nil
}

// GetRaw 发起一次 GET 请求并返回原始响应体。
func (c *Client) GetRaw(ctx context.Context, path string, query url.Values) (*Response, error) {
	endpoint, err := c.buildURL(path, query)
	if err != nil {
		return nil, err
	}
	return c.doEndpoint(ctx, endpoint)
}

// GetFullURLRaw 请求一个完整 URL（Torn 返回的 prev / next 链接场景）。
//
// ⚠️ 强制同源：prev 链接是**服务端给的字符串**，理论上可以指向任意地址。
// 不加校验就等于把「下一个请求打给谁」的决定权交给了响应体。
// 本工具只允许访问官方 API，跨源一律拒绝。
func (c *Client) GetFullURLRaw(ctx context.Context, fullURL string) (*Response, error) {
	if err := c.checkSameOrigin(fullURL); err != nil {
		return nil, err
	}
	return c.doEndpoint(ctx, fullURL)
}

// checkSameOrigin 校验 rawURL 与 baseURL 同源（scheme + host 都相同）。
func (c *Client) checkSameOrigin(rawURL string) error {
	ref, err := url.Parse(c.baseURL)
	if err != nil {
		return fmt.Errorf("torn: baseURL %q 无法解析: %w", c.baseURL, err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("torn: 待跟随的 URL 无法解析: %w", err)
	}
	if !strings.EqualFold(u.Scheme, ref.Scheme) || !strings.EqualFold(u.Host, ref.Host) {
		return fmt.Errorf(
			"torn: 拒绝请求非同源地址 %s://%s（本工具只允许访问官方 API %s://%s）",
			u.Scheme, u.Host, ref.Scheme, ref.Host)
	}
	return nil
}

// doEndpoint 是「限流 + 重试」的共享实现。
func (c *Client) doEndpoint(ctx context.Context, endpoint string) (*Response, error) {
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if c.limiter != nil {
			if err := c.limiter.Wait(ctx, c.apiKey); err != nil {
				return nil, err
			}
		}

		resp, err := c.attempt(ctx, endpoint)
		if err == nil {
			resp.Attempts = attempt
			return resp, nil
		}
		lastErr = err

		delay, retry := c.retryDelay(ctx, err, attempt)
		if !retry || attempt == c.maxAttempts {
			return nil, err
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// attempt 执行单次请求尝试（不含重试）。
func (c *Client) attempt(ctx context.Context, endpoint string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	// 密钥走 Authorization 头而非 query 参数：明文密钥不出现在 URL 中，
	// 也就不会进 Torn 的访问日志、代理日志和错误堆栈。
	// 官方格式见 openapi securitySchemes: "Authorization: ApiKey <key>"。
	req.Header.Set("Authorization", "ApiKey "+c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	httpResp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", redact(endpoint), err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(httpResp.Body, 1<<16))
		_ = httpResp.Body.Close()
	}()

	// 多读 1 字节用于判断是否超限，避免「刚好等于上限」被误判为超限。
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 %s 响应失败: %w", redact(endpoint), err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("响应体超过 %d 字节上限，已丢弃", maxResponseBytes)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: httpResp.StatusCode, Status: httpResp.Status, Body: string(body)}
	}

	if apiErr := parseAPIError(body); apiErr != nil {
		return nil, apiErr
	}

	return &Response{
		StatusCode: httpResp.StatusCode,
		Body:       body,
		FetchedAt:  c.clock(),
	}, nil
}

// retryDelay 判断该错误是否值得重试，以及应当退避多久。
func (c *Client) retryDelay(ctx context.Context, err error, attempt int) (time.Duration, bool) {
	// 调用方主动取消或整体超时：立刻收手，不要在这里悄悄重试。
	if ctx.Err() != nil {
		return 0, false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.IsRateLimited():
			return rateLimitBackoff, true
		case apiErr.IsIPBlocked():
			return ipBlockBackoff, true
		case apiErr.Retryable():
			return c.backoff(attempt), true
		default:
			// Fatal 及其余业务错误不重试 —— 重试多少次都不会变好，只会白烧配额。
			return 0, false
		}
	}

	// HTTP 5xx 是瞬态故障；4xx 说明请求本身有问题，重试无意义。
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return c.backoff(attempt), statusErr.Code >= 500
	}

	// 其余归为网络 / IO 类错误，值得退避重试。
	return c.backoff(attempt), true
}

// backoff 返回带抖动的指数退避时长：0.6s → 1.2s → 2.4s（上限 8s）。
//
// 抖动是必需的：多个请求同时被限流后，若按同样节奏重试会一直撞在一起，
// 形成周期性冲突。
func (c *Client) backoff(attempt int) time.Duration {
	d := defaultBaseBackoff << max(attempt-1, 0)
	if d > defaultMaxBackoff {
		d = defaultMaxBackoff
	}
	span := int64(d / 2)
	if span <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(span)) - time.Duration(span/2)
}

// buildURL 拼接完整请求地址。
func (c *Client) buildURL(path string, query url.Values) (string, error) {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return "", fmt.Errorf("拼接请求地址失败: %w", err)
	}
	if len(query) > 0 {
		// url.Values.Encode 按 key 排序输出，保证同一组参数得到同一个 URL。
		u.RawQuery = query.Encode()
	}
	return u.String(), nil
}

// parseAPIError 从响应体里提取 Torn 的业务错误；没有错误时返回 nil。
//
// 先用一次廉价的子串预筛再决定要不要真正解析：攻击列表响应可能几 MB，
// 每次都完整解析一遍只为找一个 error 字段并不划算。
func parseAPIError(body []byte) *APIError {
	if !bytes.Contains(body, []byte(`"error"`)) {
		return nil
	}
	var env struct {
		Error *APIError `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		// 体里有 "error" 字样但不是预期结构，交给调用方按普通响应处理。
		return nil
	}
	// Torn 在成功时也可能返回 {"error":{"code":0,"error":""}}，code==0 不算错误。
	if env.Error == nil || env.Error.Code == 0 {
		return nil
	}
	return env.Error
}

// redact 在把 URL 写进错误信息前抹掉可能存在的密钥参数（第二道保险）。
func redact(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<非法 URL>"
	}
	q := u.Query()
	if q.Has("key") {
		q.Set("key", "***")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// truncate 把字符串截断到 n 个字节，便于把短摘要塞进错误信息。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
