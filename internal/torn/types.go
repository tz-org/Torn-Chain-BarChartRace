package torn

import (
	"encoding/json"
	"fmt"
	"time"
)

// 本文件是 Torn v2 响应的 wire 类型与解码层。
//
// 分工：wire 类型只负责「原样接住 JSON」，解码函数把它收敛成领域类型。
// 领域类型只保留本工具真正用得到的字段 —— 多余字段留在原始字节里，
// 以后需要时离线重算即可，不必重抓。

// ───────────────────────────── chainreport ─────────────────────────────

// chainReportEnvelope 是 GET /faction/{chainId}/chainreport 的响应体。
type chainReportEnvelope struct {
	ChainReport chainReportWire `json:"chainreport"`
}

type chainReportWire struct {
	ID           int64                  `json:"id"`
	FactionID    int64                  `json:"faction_id"`
	Start        int64                  `json:"start"`
	End          int64                  `json:"end"`
	Details      chainReportDetailsWire `json:"details"`
	Attackers    []chainAttackerWire    `json:"attackers"`
	NonAttackers []int64                `json:"non_attackers"`
	Bonuses      []chainReportBonusWire `json:"bonuses"`
}

type chainReportDetailsWire struct {
	Chain   int     `json:"chain"`
	Respect float64 `json:"respect"`
	Members int     `json:"members"`
	Targets int     `json:"targets"`
	Best    float64 `json:"best"`
}

type chainAttackerWire struct {
	ID      int64 `json:"id"`
	Respect struct {
		Total   float64 `json:"total"`
		Average float64 `json:"average"`
		Best    float64 `json:"best"`
	} `json:"respect"`
	Attacks struct {
		Total   int `json:"total"`
		Bonuses int `json:"bonuses"`
		Assists int `json:"assists"`
		Losses  int `json:"losses"`
		Escapes int `json:"escapes"`
		Draws   int `json:"draws"`
	} `json:"attacks"`
}

type chainReportBonusWire struct {
	AttackerID int64   `json:"attacker_id"`
	DefenderID int64   `json:"defender_id"`
	Chain      int     `json:"chain"`
	Respect    float64 `json:"respect"`
}

// ChainAttacker 是 chain 报告里的单个参与者汇总（官方口径）。
type ChainAttacker struct {
	UserID  int64
	Respect float64
	Best    float64
	Hits    int
	Bonuses int
	Assists int
	Losses  int
	Escapes int
	Draws   int
}

// ChainReport 是 chain 报告的领域表示。
type ChainReport struct {
	ID        int64
	FactionID int64
	Start     time.Time
	End       time.Time

	// Ongoing 表示 chain 尚未结束（end 为 0 或晚于当前时间）。
	Ongoing bool

	ChainHits int     // 官方口径的最终连击数
	Respect   float64 // 官方口径的最终 respect
	Members   int
	Targets   int
	Best      float64

	Attackers    []ChainAttacker
	NonAttackers []int64
	BonusCount   int
}

// AttackerIDs 返回报告里出现过的所有攻击者 id。
func (r *ChainReport) AttackerIDs() []int64 {
	out := make([]int64, 0, len(r.Attackers))
	for _, a := range r.Attackers {
		out = append(out, a.UserID)
	}
	return out
}

// Duration 返回 chain 的持续时长；未结束时为 0。
func (r *ChainReport) Duration() time.Duration {
	if r.Ongoing || r.End.IsZero() || r.End.Before(r.Start) {
		return 0
	}
	return r.End.Sub(r.Start)
}

// DecodeChainReport 解析 chainreport 响应体。
//
// now 用于判定「进行中」：官方对进行中的 chain 会把 end 置 0，
// 但不同版本行为不完全一致，因此同时用 end<=0 和 end>now 两个判据。
func DecodeChainReport(raw []byte, now time.Time) (*ChainReport, error) {
	var env chainReportEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 chainreport 响应失败: %w", err)
	}
	w := env.ChainReport
	if w.ID == 0 {
		return nil, fmt.Errorf("chainreport 响应缺少 chainreport.id，响应结构与预期不符")
	}

	r := &ChainReport{
		ID:           w.ID,
		FactionID:    w.FactionID,
		ChainHits:    w.Details.Chain,
		Respect:      w.Details.Respect,
		Members:      w.Details.Members,
		Targets:      w.Details.Targets,
		Best:         w.Details.Best,
		NonAttackers: w.NonAttackers,
		BonusCount:   len(w.Bonuses),
	}
	if w.Start > 0 {
		r.Start = time.Unix(w.Start, 0)
	}
	if w.End > 0 {
		r.End = time.Unix(w.End, 0)
	}
	r.Ongoing = w.End <= 0 || time.Unix(w.End, 0).After(now)

	for _, a := range w.Attackers {
		r.Attackers = append(r.Attackers, ChainAttacker{
			UserID:  a.ID,
			Respect: a.Respect.Total,
			Best:    a.Respect.Best,
			Hits:    a.Attacks.Total,
			Bonuses: a.Attacks.Bonuses,
			Assists: a.Attacks.Assists,
			Losses:  a.Attacks.Losses,
			Escapes: a.Attacks.Escapes,
			Draws:   a.Attacks.Draws,
		})
	}
	return r, nil
}

// ───────────────────────────── attacksfull ─────────────────────────────

// attacksEnvelope 是 GET /faction/attacksfull 的响应体。
//
// 结构：{"attacks":[...], "_metadata":{"links":{"prev":…,"next":…}}}
// `attacks` 是**数组**（不是以 id 为键的 map）。
type attacksEnvelope struct {
	Attacks  []attackWire `json:"attacks"`
	Metadata struct {
		Links struct {
			Prev *string `json:"prev"`
			Next *string `json:"next"`
		} `json:"links"`
	} `json:"_metadata"`
}

// attackWire 是精简版攻击记录。
//
// 精简版（attacksfull）的 attacker / defender 是扁平 {id, faction_id}，
// **没有** name / modifiers / is_ranked_war。本工具只需要 id 与时间，
// 因此精简版完全够用，且 limit 上限是完整版的 10 倍。
type attackWire struct {
	ID          int64      `json:"id"`
	Code        string     `json:"code"`
	Started     int64      `json:"started"`
	Ended       int64      `json:"ended"`
	Result      string     `json:"result"`
	RespectGain float64    `json:"respect_gain"`
	RespectLoss float64    `json:"respect_loss"`
	Attacker    *actorWire `json:"attacker"`
	Defender    actorWire  `json:"defender"`
}

// actorWire 同时容纳精简版（faction_id 平铺）与完整版（faction 嵌套）两种形状，
// 谁给了就用谁 —— 这样同一份解码路径能吃两种响应，也便于读旧缓存。
type actorWire struct {
	ID      int64   `json:"id"`
	Name    *string `json:"name"`
	Faction *struct {
		ID int64 `json:"id"`
	} `json:"faction"`
	FactionID int64 `json:"faction_id"`
}

// resolvedFactionID 优先取完整版的嵌套对象，回退到精简版的平铺字段。
func (a actorWire) resolvedFactionID() int64 {
	if a.Faction != nil {
		return a.Faction.ID
	}
	return a.FactionID
}

// Attack 是攻击记录的领域表示。
type Attack struct {
	ID          int64
	Code        string
	Started     time.Time
	Ended       time.Time
	Result      string
	RespectGain float64
	AttackerID  int64
	// AttackerFactionID 是攻击发起时所属帮派（精简版提供）。
	// 0 表示响应没给，调用方不应据此过滤。
	AttackerFactionID int64
}

// AttacksPage 是一页攻击列表 + 分页信息。
//
// 把分页信息与数据一起返回，是为了让调用方**不可能忘记翻页** ——
// 少翻一页不会报错，只会静默少算，这类 bug 最难发现。
type AttacksPage struct {
	Attacks []Attack
	// PrevLink 是 Torn 返回的下一页 URL（按时间戳降序，prev 指回更早的数据）。
	// 空串表示没有更早的页了。
	PrevLink string
	HasPrev  bool
	// NextLink 与 PrevLink 对称：按时间戳**升序**翻页时用它指向更晚的数据。
	NextLink string
	HasNext  bool
}

// DecodeAttacksPage 解析一页攻击列表。
func DecodeAttacksPage(raw []byte) (*AttacksPage, error) {
	var env attacksEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 attacksfull 响应失败: %w", err)
	}

	page := &AttacksPage{
		Attacks: make([]Attack, 0, len(env.Attacks)),
	}
	if env.Metadata.Links.Prev != nil && *env.Metadata.Links.Prev != "" {
		page.PrevLink = *env.Metadata.Links.Prev
		page.HasPrev = true
	}
	if env.Metadata.Links.Next != nil && *env.Metadata.Links.Next != "" {
		page.NextLink = *env.Metadata.Links.Next
		page.HasNext = true
	}

	for _, a := range env.Attacks {
		atk := Attack{
			ID:          a.ID,
			Code:        a.Code,
			Result:      a.Result,
			RespectGain: a.RespectGain,
		}
		if a.Started > 0 {
			atk.Started = time.Unix(a.Started, 0)
		}
		if a.Ended > 0 {
			atk.Ended = time.Unix(a.Ended, 0)
		}
		// attacker 为 null 表示隐身攻击（只可能出现在 incoming 方向）。
		// 本工具只取 outgoing，遇到 null 直接跳过。
		if a.Attacker != nil {
			atk.AttackerID = a.Attacker.ID
			atk.AttackerFactionID = a.Attacker.resolvedFactionID()
		}
		page.Attacks = append(page.Attacks, atk)
	}
	return page, nil
}

// ───────────────────────────── members ─────────────────────────────

// membersEnvelope 是 GET /faction/{id}/members 的响应体。
type membersEnvelope struct {
	Members []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"members"`
}

// DecodeMembers 解析成员列表为 id → name 映射。
func DecodeMembers(raw []byte) (map[int64]string, error) {
	var env membersEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 faction/members 响应失败: %w", err)
	}
	out := make(map[int64]string, len(env.Members))
	for _, m := range env.Members {
		if m.ID != 0 && m.Name != "" {
			out[m.ID] = m.Name
		}
	}
	return out, nil
}

// ───────────────────────────── key/info ─────────────────────────────

// keyInfoEnvelope 是 GET /key/info 的响应体。
type keyInfoEnvelope struct {
	Info struct {
		User struct {
			ID        int64  `json:"id"`
			FactionID *int64 `json:"faction_id"`
		} `json:"user"`
		Access struct {
			Level   int    `json:"level"`
			Type    string `json:"type"`
			Faction bool   `json:"faction"`
		} `json:"access"`
		Selections struct {
			Faction []string `json:"faction"`
		} `json:"selections"`
	} `json:"info"`
}

// KeyInfo 是 API key 的能力快照，用于在抓取前把「缺哪个权限」提前暴露出来。
type KeyInfo struct {
	OwnerID           int64
	OwnerFactionID    int64
	AccessLevel       int
	AccessType        string
	FactionAccess     bool
	FactionSelections []string
}

// HasFactionSelection 报告 key 是否具备某个 faction 选择项（如 "attacksfull"）。
func (k *KeyInfo) HasFactionSelection(name string) bool {
	for _, s := range k.FactionSelections {
		if s == name {
			return true
		}
	}
	return false
}

// DecodeKeyInfo 解析 /key/info 响应体。
func DecodeKeyInfo(raw []byte) (*KeyInfo, error) {
	var env keyInfoEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 key/info 响应失败: %w", err)
	}
	if env.Info.User.ID == 0 {
		return nil, fmt.Errorf("key/info 响应缺少 info.user.id，响应结构与预期不符")
	}
	k := &KeyInfo{
		OwnerID:           env.Info.User.ID,
		AccessLevel:       env.Info.Access.Level,
		AccessType:        env.Info.Access.Type,
		FactionAccess:     env.Info.Access.Faction,
		FactionSelections: env.Info.Selections.Faction,
	}
	if env.Info.User.FactionID != nil {
		k.OwnerFactionID = *env.Info.User.FactionID
	}
	return k, nil
}

// ───────────────────────────── user/basic ─────────────────────────────

// userBasicEnvelope 是 GET /user/{id}/basic 的响应体。
type userBasicEnvelope struct {
	Profile struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"profile"`
}

// DecodeUserBasicName 解析用户显示名，并校验返回的 id 与请求一致。
//
// 校验是必需的：若服务端返回的是别人的资料，照着写会得到一个**错的名字**，
// 那比没有名字糟得多。
func DecodeUserBasicName(raw []byte, wantID int64) (string, error) {
	var env userBasicEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("解析 user/basic 响应失败: %w", err)
	}
	if env.Profile.ID != wantID {
		return "", fmt.Errorf("user/%d/basic 返回的 profile.id 是 %d，与请求不符", wantID, env.Profile.ID)
	}
	if env.Profile.Name == "" {
		return "", fmt.Errorf("user/%d/basic 响应缺少 profile.name", wantID)
	}
	return env.Profile.Name, nil
}
