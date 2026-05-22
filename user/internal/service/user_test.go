package service

import (
	"testing"

	"user/internal/biz"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMapError_Nil(t *testing.T) {
	s := &UserService{}
	assert.Nil(t, s.mapError(nil))
}

func TestMapError_UserNotFound(t *testing.T) {
	s := &UserService{}
	err := s.mapError(biz.ErrUserNotFound)
	assert.NotNil(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestMapError_PhoneExists(t *testing.T) {
	s := &UserService{}
	err := s.mapError(biz.ErrPhoneExists)
	assert.NotNil(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.AlreadyExists, st.Code())
}

func TestMapError_EmailExists(t *testing.T) {
	s := &UserService{}
	err := s.mapError(biz.ErrEmailExists)
	assert.NotNil(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.AlreadyExists, st.Code())
}

func TestMapError_InvalidArgument(t *testing.T) {
	s := &UserService{}
	invalidArgs := []error{
		biz.ErrInvalidUserID,
		biz.ErrPhoneRequired,
		biz.ErrPasswordRequired,
		biz.ErrNothingToUpdate,
		biz.ErrWrongCredentials,
		biz.ErrTokenRequired,
		biz.ErrInvalidPhone,
		biz.ErrInvalidEmail,
		biz.ErrPasswordTooShort,
		biz.ErrPasswordTooLong,
		biz.ErrPasswordNoUpper,
		biz.ErrPasswordNoLower,
		biz.ErrPasswordNoDigit,
		biz.ErrPasswordNoSpecial,
		biz.ErrNameTooShort,
		biz.ErrNameTooLong,
		biz.ErrInvalidGender,
		biz.ErrInvalidAge,
		biz.ErrWrongOldPassword,
		biz.ErrPasswordSameAsOld,
		biz.ErrInvalidRefreshToken,
	}
	for _, e := range invalidArgs {
		err := s.mapError(e)
		assert.NotNil(t, err, "expected error for %v", e)
		st, ok := status.FromError(err)
		assert.True(t, ok, "expected gRPC status for %v", e)
		assert.Equal(t, codes.InvalidArgument, st.Code(), "wrong code for %v", e)
	}
}

func TestMapError_Internal(t *testing.T) {
	s := &UserService{}
	internalErrs := []error{
		biz.ErrGenerateToken,
		biz.ErrStoreToken,
		biz.ErrEncryptPassword,
	}
	for _, e := range internalErrs {
		err := s.mapError(e)
		assert.NotNil(t, err, "expected error for %v", e)
		st, ok := status.FromError(err)
		assert.True(t, ok, "expected gRPC status for %v", e)
		assert.Equal(t, codes.Internal, st.Code(), "wrong code for %v", e)
	}
}

func TestMapError_ResourceExhausted(t *testing.T) {
	s := &UserService{}
	err := s.mapError(biz.ErrRegisterRateLimited)
	assert.NotNil(t, err)
	st, ok := status.FromError(err)
	assert.True(t, ok)
	assert.Equal(t, codes.ResourceExhausted, st.Code())
}
