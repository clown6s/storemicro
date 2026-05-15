package strutil

// MaskPhone 脱敏手机号（138****5678）
func MaskPhone(phone string) string {
	if len(phone) != 11 {
		return "***"
	}
	return phone[:3] + "****" + phone[7:]
}
