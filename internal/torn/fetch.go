package torn

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AttacksQuery 是 /faction/attacksfull 的查询条件。
//
// 时间戳语义：From / To 是**闭区间**（官方说明 to 为 "up to and including"），
// 直接传 chain 的 start / end 即可覆盖整场连击。
type AttacksQuery struct {
	// Filters 例如 ["outgoing"]。留空表示不收窄（incoming + outgoing）。
	Filters []string
	Limit   int
	Sort    string
	From    int64
	To      int64
}

// Values 把查询条件转换成 URL query。
func (q AttacksQuery) Values() url.Values {
	v := url.Values{}
	if len(q.Filters) > 0 {
		// openapi 中 filters 为 style=form, explode=false → 逗号连接。
		v.Set("filters", strings.Join(q.Filters, ","))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	if q.From > 0 {
		v.Set("from", strconv.FormatInt(q.From, 10))
	}
	if q.To > 0 {
		v.Set("to", strconv.FormatInt(q.To, 10))
	}
	return v
}

// KeyInfo 抓取 key 的能力快照。
//
// 该端点 public key 即可访问，所以用户刚填完密钥就能立刻调用，
// 把「缺哪个权限」提前暴露出来，而不是跑到抓取中途才失败。
func (c *Client) KeyInfo(ctx context.Context) (*KeyInfo, []byte, error) {
	resp, err := c.GetRaw(ctx, PathKeyInfo, nil)
	if err != nil {
		return nil, nil, err
	}
	ki, err := DecodeKeyInfo(resp.Body)
	if err != nil {
		return nil, resp.Body, err
	}
	return ki, resp.Body, nil
}

// ChainReport 抓取指定 chain 的官方报告。
func (c *Client) ChainReport(ctx context.Context, chainID int64) (*ChainReport, []byte, error) {
	if chainID <= 0 {
		return nil, nil, fmt.Errorf("torn: chain id 必须为正数，实际 %d", chainID)
	}
	resp, err := c.GetRaw(ctx, ChainReportPath(chainID), nil)
	if err != nil {
		return nil, nil, err
	}
	r, err := DecodeChainReport(resp.Body, time.Now())
	if err != nil {
		return nil, resp.Body, err
	}
	return r, resp.Body, nil
}

// AttacksFirstPage 抓取攻击列表的第一页（按 q 构造请求）。
func (c *Client) AttacksFirstPage(ctx context.Context, q AttacksQuery) (*AttacksPage, []byte, error) {
	if q.Limit > MaxAttacksFullLimit {
		q.Limit = MaxAttacksFullLimit
	}
	resp, err := c.GetRaw(ctx, PathFactionAttacksFull, q.Values())
	if err != nil {
		return nil, nil, err
	}
	page, err := DecodeAttacksPage(resp.Body)
	if err != nil {
		return nil, resp.Body, err
	}
	return page, resp.Body, nil
}

// AttacksPageByURL 请求 Torn 返回的 prev / next 完整链接。
//
// prev 链接里已经带了正确的 limit / sort / from / to，
// 直接请求它比手动推算时间戳可靠得多。
func (c *Client) AttacksPageByURL(ctx context.Context, fullURL string) (*AttacksPage, []byte, error) {
	resp, err := c.GetFullURLRaw(ctx, fullURL)
	if err != nil {
		return nil, nil, err
	}
	page, err := DecodeAttacksPage(resp.Body)
	if err != nil {
		return nil, resp.Body, err
	}
	return page, resp.Body, nil
}

// Members 抓取指定帮派的成员列表，返回 id → name 映射。
func (c *Client) Members(ctx context.Context, factionID int64) (map[int64]string, []byte, error) {
	if factionID <= 0 {
		return nil, nil, fmt.Errorf("torn: faction id 必须为正数，实际 %d", factionID)
	}
	resp, err := c.GetRaw(ctx, FactionMembersPath(factionID), nil)
	if err != nil {
		return nil, nil, err
	}
	m, err := DecodeMembers(resp.Body)
	if err != nil {
		return nil, resp.Body, err
	}
	return m, resp.Body, nil
}

// UserBasicName 取单个用户的显示名（仅用于成员列表里查不到时的兜底）。
func (c *Client) UserBasicName(ctx context.Context, userID int64) (string, error) {
	if userID <= 0 {
		return "", fmt.Errorf("torn: user id 必须为正数，实际 %d", userID)
	}
	resp, err := c.GetRaw(ctx, UserBasicPath(userID), nil)
	if err != nil {
		return "", err
	}
	return DecodeUserBasicName(resp.Body, userID)
}
