package torn

import (
	"context"
	"sync"
	"time"
)

// Limiter 是单窗口滑动限流器：「在 Window 内最多放行 Max 次请求」。
//
// 为什么用滑动窗口而不是令牌桶：Torn 的配额语义是「滚动 60 秒内 N 次」，
// 固定 refill 的令牌桶在窗口边界会允许双倍突发，正好踩中 429。
//
// MinGap 用来削平突发：即使配额还有富余，两次请求之间也强制留出间隔，
// 避免「1 秒打光一整分钟额度」后被服务端按行为特征限流。
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	minGap time.Duration

	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error

	// hist 按时间升序追加，因此清理时从头部截断即可。
	hist    []time.Time
	lastReq time.Time

	// total 是自创建以来成功放行的请求总数（单调递增，不随窗口滑出减少）。
	// 用于在收尾时报告「本次共消耗多少配额」。
	total int64
}

// NewLimiter 构造限流器。max 与 window 非正时表示不限制。
func NewLimiter(max int, window, minGap time.Duration) *Limiter {
	return &Limiter{
		max:    max,
		window: window,
		minGap: minGap,
		now:    time.Now,
		wait:   sleepCtx,
	}
}

// Wait 阻塞直到可以发起一次请求，成功后立即占位。
//
// 语义上等价于「预约一份配额」：拿到 nil 之后应当**马上**发请求，
// 不要在中间插入耗时操作，否则等于把配额空占着。
func (l *Limiter) Wait(ctx context.Context, _ string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		l.mu.Lock()
		now := l.now()
		l.pruneLocked(now)
		delay := l.delayLocked(now)
		if delay <= 0 {
			l.recordLocked(now)
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()

		// 等待必须在锁外进行，否则会把其他 goroutine 一起冻住，
		// 限流器就退化成了串行锁。
		if err := l.wait(ctx, delay); err != nil {
			return err
		}
	}
}

// RequestsTotal 返回自创建以来成功放行的请求总数。
func (l *Limiter) RequestsTotal() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// delayLocked 计算此刻发请求需要等多久；0 表示可以立刻发送。
func (l *Limiter) delayLocked(now time.Time) time.Duration {
	var delay time.Duration

	if l.max > 0 && l.window > 0 && len(l.hist) >= l.max {
		delay = maxDuration(delay, l.hist[0].Add(l.window).Sub(now))
	}
	if l.minGap > 0 && !l.lastReq.IsZero() {
		delay = maxDuration(delay, l.lastReq.Add(l.minGap).Sub(now))
	}
	return delay
}

// recordLocked 记下一次成功的占位。
func (l *Limiter) recordLocked(now time.Time) {
	if l.max > 0 && l.window > 0 {
		l.hist = append(l.hist, now)
	}
	l.lastReq = now
	l.total++
}

// pruneLocked 丢弃已经滑出窗口的历史。
func (l *Limiter) pruneLocked(now time.Time) {
	if l.max <= 0 || l.window <= 0 {
		return
	}
	cut := now.Add(-l.window)
	i := 0
	for i < len(l.hist) && !l.hist[i].After(cut) {
		i++
	}
	if i > 0 {
		l.hist = l.hist[i:]
	}
}

// sleepCtx 休眠 d，或在 ctx 取消时提前返回。
//
// 必须用 select + timer 而非 time.Sleep：否则 Ctrl+C 要等这一觉睡完才生效，
// 而限流等待动辄几十秒。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func maxDuration(a, b time.Duration) time.Duration {
	if b > a {
		return b
	}
	return a
}
