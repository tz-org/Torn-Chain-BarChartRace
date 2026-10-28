package torn

import "strconv"

// API 基础地址与端点路径。
//
// 路径**不含** /v2 前缀 —— 该前缀属于 base URL（https://api.torn.com/v2，
// 见 openapi.json 的 servers 字段）。拼 URL 时不要重复加。
const (
	// DefaultBaseURL 是官方 base URL（openapi servers[0].url）。
	DefaultBaseURL = "https://api.torn.com/v2"

	PathKeyInfo            = "/key/info"
	PathFactionChainReport = "/faction/chainreport"
	PathFactionAttacksFull = "/faction/attacksfull"
)

// 端点分页上限，取自 openapi 中 limit 参数的 maximum。超限会被 Torn 直接报错，
// 因此必须在构造请求时夹紧，而不是把错误留给调用方。
const (
	// MaxAttacksFullLimit 是 /faction/attacksfull 的 limit 上限。
	//
	// 选精简版（attacksfull）而不是完整版（attacks）的理由：本工具只需要
	// attacker.id / started / result / respect_gain 四个字段做时间分桶，
	// 精简版 limit=1000（完整版只有 100），配额消耗相差一个数量级；
	// 而完整版多出来的 modifiers / is_ranked_war 对「按时间累计」毫无用处。
	MaxAttacksFullLimit = 1000
)

// ChainReportPath 构造指定 chain 的报告路径。
//
// ⚠️ 路径参数是 **chainId**，不是 faction id —— 官方 operationId 为 getChainReport，
// 参数名就叫 chainId。已结束的 chain 任何帮派的报告都读得到（public key）；
// 进行中的 chain 只有自己帮派的报告可读。
func ChainReportPath(chainID int64) string {
	return "/faction/" + strconv.FormatInt(chainID, 10) + "/chainreport"
}

// FactionMembersPath 构造指定帮派的成员列表路径。
func FactionMembersPath(factionID int64) string {
	return "/faction/" + strconv.FormatInt(factionID, 10) + "/members"
}

// UserBasicPath 构造指定用户的基础资料路径（仅用于兜底取名）。
func UserBasicPath(userID int64) string {
	return "/user/" + strconv.FormatInt(userID, 10) + "/basic"
}
