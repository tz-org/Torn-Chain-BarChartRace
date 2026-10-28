package torn

import "fmt"

// Torn API 业务错误码。
//
// 全部取自官方 openapi.json 中各个 Error* schema 的 code enum。
// 只保留本项目实际会遇到的码 + 几个用于给出准确指引的码，
// 其余码会以数字形式原样透出（ErrorCodeName 返回 unknown）。
const (
	ErrUnknown           = 0  // 未知错误
	ErrKeyEmpty          = 1  // 未提供 key
	ErrIncorrectKey      = 2  // key 不正确
	ErrWrongType         = 3  // 参数类型错误
	ErrWrongFields       = 4  // 字段缺失或非法
	ErrTooManyRequests   = 5  // 超出频率限制
	ErrIncorrectID       = 6  // ID 不正确
	ErrIPBlocked         = 8  // IP 被封禁
	ErrAPIDisabled       = 9  // API 被禁用
	ErrKeyReadError      = 12 // key 读取失败
	ErrDailyReadLimit    = 14 // 达到每日读取上限
	ErrAccessLevelTooLow = 16 // key 权限不足
	ErrBackendError      = 17 // Torn 后端错误
	ErrAPIKeyPaused      = 18 // key 被暂停
	ErrIncorrectLogID    = 28 // log id 不正确
)

// errorCodeNames 把错误码映射成官方名称，便于在报错里给出可检索的关键字。
var errorCodeNames = map[int]string{
	ErrUnknown:           "Unknown",
	ErrKeyEmpty:          "KeyEmpty",
	ErrIncorrectKey:      "IncorrectKey",
	ErrWrongType:         "WrongType",
	ErrWrongFields:       "WrongFields",
	ErrTooManyRequests:   "TooManyRequests",
	ErrIncorrectID:       "IncorrectId",
	ErrIPBlocked:         "IpBlocked",
	ErrAPIDisabled:       "ApiDisabled",
	ErrKeyReadError:      "KeyReadError",
	ErrDailyReadLimit:    "DailyReadLimitReached",
	ErrAccessLevelTooLow: "AccessLevelTooLow",
	ErrBackendError:      "BackendError",
	ErrAPIKeyPaused:      "ApiKeyPaused",
	ErrIncorrectLogID:    "IncorrectLogId",
}

// ErrorCodeName 返回错误码的官方名称；未知码返回 unknown。
func ErrorCodeName(code int) string {
	if n, ok := errorCodeNames[code]; ok {
		return n
	}
	return "unknown"
}

// APIError 是 Torn 返回的业务错误。
//
// ⚠️ 接 Torn API 最容易踩的坑：**失败时 HTTP 状态码通常仍是 200**，
// 错误放在响应体的 {"error":{"code":N,"error":"..."}} 里。
// 因此绝不能只看 HTTP 状态码判断成败，必须解析响应体。
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"error"`
}

// Error 实现 error 接口。
func (e *APIError) Error() string {
	return fmt.Sprintf("Torn API 错误 %d (%s): %s", e.Code, ErrorCodeName(e.Code), e.Message)
}

// Retryable 报告「等一会儿再试可能成功」——只有这类错误才值得消耗重试次数。
func (e *APIError) Retryable() bool {
	switch e.Code {
	case ErrTooManyRequests, ErrIPBlocked, ErrBackendError, ErrUnknown:
		return true
	}
	return false
}

// Fatal 报告「必须改配置或换 key 才能解决」。
//
// 这类错误重试多少次都不会变好，只会白烧配额，必须立刻中断并给出可执行的指引。
func (e *APIError) Fatal() bool {
	switch e.Code {
	case ErrKeyEmpty, ErrIncorrectKey, ErrAPIDisabled, ErrKeyReadError,
		ErrAccessLevelTooLow, ErrAPIKeyPaused:
		return true
	}
	return false
}

// Hint 返回针对该错误码的可执行建议；没有专门建议时返回空串。
func (e *APIError) Hint() string {
	switch e.Code {
	case ErrIncorrectKey:
		return "API key 不正确或已失效，请到 Torn 的 Preferences → API 重新生成"
	case ErrAccessLevelTooLow:
		return "key 权限不足：需要至少 Limited Access，并勾选 Faction → Attacks / Chain report"
	case ErrKeyEmpty:
		return "请求没有携带 API key"
	case ErrTooManyRequests:
		return "触发频率限制（100 次/分钟），工具会自动等待后重试；也可用 -rps 调低速率"
	case ErrIPBlocked:
		return "IP 被 Torn 临时封禁，请等待数分钟后重试"
	case ErrDailyReadLimit:
		return "已达到该 key 的每日读取上限，请换 key 或等到次日"
	case ErrIncorrectID:
		return "chain id 或 faction id 不存在"
	}
	return ""
}

// IsRateLimited 报告是否为频率超限（错误码 5）。
func (e *APIError) IsRateLimited() bool { return e.Code == ErrTooManyRequests }

// IsIPBlocked 报告是否为 IP 封禁（错误码 8）。
func (e *APIError) IsIPBlocked() bool { return e.Code == ErrIPBlocked }
