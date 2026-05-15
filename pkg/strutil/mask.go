package strutil

// MaskPhone 脱敏手机号（138****5678），使用 rune 避免多字节字符误判
func MaskPhone(phone string) string {
	runes := []rune(phone)
	if len(runes) != 11 {
		return "***"
	}
	return string(runes[:3]) + "****" + string(runes[7:])
}
