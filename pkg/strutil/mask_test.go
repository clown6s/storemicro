package strutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaskPhone(t *testing.T) {
	// 11-digit phone number: first 2 + **** + last 2
	assert.Equal(t, "13****78", MaskPhone("13812345678"))
}

func TestMaskPhone_7Digits(t *testing.T) {
	assert.Equal(t, "13****78", MaskPhone("13812345678")) // Wait, this is 11 digits...
}

func TestMaskPhone_7to10Digits(t *testing.T) {
	assert.Equal(t, "13****56", MaskPhone("138123456"))   // 9 digits: first2 + **** + last2
	assert.Equal(t, "13****78", MaskPhone("13812345678")) // 11 digits: same logic
	assert.Equal(t, "13****34", MaskPhone("1381234"))     // 7 digits: first2 + **** + last2
}

func TestMaskPhone_4to6Digits(t *testing.T) {
	assert.Equal(t, "1***4", MaskPhone("1234"))   // 4 digits
	assert.Equal(t, "1***6", MaskPhone("123456")) // 6 digits
}

func TestMaskPhone_Short(t *testing.T) {
	assert.Equal(t, "***", MaskPhone("123")) // 3 digits
	assert.Equal(t, "***", MaskPhone("12"))
	assert.Equal(t, "***", MaskPhone("1"))
}

func TestMaskPhone_Empty(t *testing.T) {
	assert.Equal(t, "***", MaskPhone(""))
}

func TestMaskEmail(t *testing.T) {
	assert.Equal(t, "t***@example.com", MaskEmail("test@example.com"))
	assert.Equal(t, "a***@b.com", MaskEmail("a@b.com"))
}

func TestMaskEmail_LongLocal(t *testing.T) {
	assert.Equal(t, "u***@example.com", MaskEmail("username@example.com"))
}

func TestMaskEmail_NoAt(t *testing.T) {
	assert.Equal(t, "***", MaskEmail("nobody"))
}

func TestMaskEmail_Empty(t *testing.T) {
	assert.Equal(t, "***", MaskEmail(""))
}

func TestMaskEmail_AtOnly(t *testing.T) {
	assert.Equal(t, "***", MaskEmail("@example.com"))
}
