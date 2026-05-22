package biz

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- validatePassword tests ---

func TestValidatePassword_Valid(t *testing.T) {
	assert.NoError(t, validatePassword("Abc123!@#"))
	assert.NoError(t, validatePassword("Admin123!"))
	assert.NoError(t, validatePassword(strings.Repeat("aB1!x", 5))) // 20 chars, all requirements met
}

func TestValidatePassword_TooShort(t *testing.T) {
	err := validatePassword("Ab1!")
	assert.ErrorIs(t, err, ErrPasswordTooShort)
}

func TestValidatePassword_TooLong(t *testing.T) {
	longPwd := strings.Repeat("Abcdef12!x", 8) // 10 * 8 = 80 chars > 72
	assert.Greater(t, len(longPwd), 72)
	err := validatePassword(longPwd)
	assert.ErrorIs(t, err, ErrPasswordTooLong)
}

func TestValidatePassword_NoUpper(t *testing.T) {
	err := validatePassword("abc123!@#")
	assert.ErrorIs(t, err, ErrPasswordNoUpper)
}

func TestValidatePassword_NoLower(t *testing.T) {
	err := validatePassword("ABC123!@#")
	assert.ErrorIs(t, err, ErrPasswordNoLower)
}

func TestValidatePassword_NoDigit(t *testing.T) {
	err := validatePassword("Abcdef!@#")
	assert.ErrorIs(t, err, ErrPasswordNoDigit)
}

func TestValidatePassword_NoSpecial(t *testing.T) {
	err := validatePassword("Abcdef12")
	assert.ErrorIs(t, err, ErrPasswordNoSpecial)
}

func TestValidatePassword_Empty(t *testing.T) {
	err := validatePassword("")
	assert.ErrorIs(t, err, ErrPasswordTooShort)
}

// --- phone regex tests ---

func TestValidatePhone_Valid(t *testing.T) {
	validPhones := []string{"13812345678", "13900001111", "15012345678", "18600001234", "19999999999"}
	for _, phone := range validPhones {
		assert.True(t, phoneRegex.MatchString(phone), "phone %q should match", phone)
	}
}

func TestValidatePhone_Invalid(t *testing.T) {
	invalidPhones := []string{
		"12345678901",  // starts with 1 but second digit is 2
		"11111111111",  // all 1s
		"23812345678",  // starts with 2
		"1381234567",   // 10 digits
		"138123456789", // 12 digits
		"abcdefghijk",  // letters
		"",             // empty
	}
	for _, phone := range invalidPhones {
		assert.False(t, phoneRegex.MatchString(phone), "phone %q should not match", phone)
	}
}

// --- email regex tests ---

func TestValidateEmail_Valid(t *testing.T) {
	validEmails := []string{
		"test@example.com",
		"user.name@domain.org",
		"a@b.co",
		"long.email+tag@gmail.com",
	}
	for _, email := range validEmails {
		assert.True(t, emailRegex.MatchString(email), "email %q should match", email)
	}
}

func TestValidateEmail_Invalid(t *testing.T) {
	invalidEmails := []string{
		"not-an-email",
		"@domain.com",
		"user@",
		"user@.com",
		"",
		"just-text",
	}
	for _, email := range invalidEmails {
		assert.False(t, emailRegex.MatchString(email), "email %q should not match", email)
	}
}

// --- error variable tests ---

func TestErrorVariables(t *testing.T) {
	// Ensure all error variables are defined and non-nil
	errors := []error{
		ErrUserNotFound,
		ErrPhoneRequired,
		ErrPasswordRequired,
		ErrPhoneExists,
		ErrEmailExists,
		ErrInvalidUserID,
		ErrNothingToUpdate,
		ErrWrongCredentials,
		ErrTokenRequired,
		ErrGenerateToken,
		ErrStoreToken,
		ErrEncryptPassword,
		ErrInvalidPhone,
		ErrInvalidEmail,
		ErrPasswordTooShort,
		ErrPasswordTooLong,
		ErrPasswordNoUpper,
		ErrPasswordNoLower,
		ErrPasswordNoDigit,
		ErrPasswordNoSpecial,
		ErrNameTooShort,
		ErrNameTooLong,
		ErrRemarkTooLong,
		ErrAvatarTooLong,
		ErrInvalidAvatar,
		ErrEmailTooLong,
		ErrInvalidGender,
		ErrInvalidAge,
		ErrWrongOldPassword,
		ErrPasswordSameAsOld,
		ErrInvalidRefreshToken,
		ErrRegisterRateLimited,
	}
	for _, err := range errors {
		assert.NotNil(t, err, "error variable should not be nil")
		assert.NotEmpty(t, err.Error(), "error message should not be empty")
	}
}
