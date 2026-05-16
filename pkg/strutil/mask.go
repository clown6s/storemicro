package strutil

import "strings"

// MaskPhone 脱敏手机号（支持非11位号码）
// 11位：138****5678 | 7-10位：显示前2+后2 | 4-6位：显示首+尾 | 更短：***
func MaskPhone(phone string) string {
	runes := []rune(phone)
	n := len(runes)
	switch {
	case n >= 7:
		// 显示前2位 + **** + 后2位
		return string(runes[:2]) + "****" + string(runes[n-2:])
	case n >= 4:
		// 显示首位 + *** + 末位
		return string(runes[:1]) + "***" + string(runes[n-1:])
	default:
		return "***"
	}
}

// MaskEmail 脱敏邮箱地址：u***@example.com
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	local := email[:at]
	domain := email[at+1:]
	switch {
	case len(local) >= 1:
		return local[:1] + "***@" + domain
	default:
		return "***@" + domain
	}
}
