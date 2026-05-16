package service

import (
	"context"
	"errors"

	userv1 "user/api/user/v1"
	"user/internal/biz"

	"github.com/google/wire"
	pkgstr "github.com/storemicro/pkg/strutil"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProviderSet is service providers.
var ProviderSet = wire.NewSet(NewUserService)

type UserService struct {
	userv1.UnimplementedUserServer
	uc *biz.UserUsecase
}

// NewUserService 创建用户 gRPC 服务
func NewUserService(uc *biz.UserUsecase) *UserService {
	return &UserService{uc: uc}
}

func (s *UserService) GetUser(ctx context.Context, req *userv1.GetUserReq) (*userv1.GetUserResp, error) {
	zap.L().Debug("GetUser called", zap.Int64("id", req.Id))

	if req.Id <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid user id: %d", req.Id)
	}

	user, err := s.uc.Get(ctx, req.Id)
	if err != nil {
		zap.L().Warn("GetUser failed", zap.Int64("id", req.Id), zap.Error(err))
		return nil, s.mapError(err)
	}
	return &userv1.GetUserResp{
		Code:    200,
		Message: "success",
		Data:    s.toUserInfo(user),
	}, nil
}

// Register 用户注册
func (s *UserService) Register(ctx context.Context, req *userv1.RegisterReq) (*userv1.RegisterResp, error) {
	zap.L().Debug("Register called", zap.String("phone", pkgstr.MaskPhone(req.Phone)))

	user := &biz.User{
		Phone:    req.Phone,
		Password: req.Password,
		Email:    req.Email,
		RealName: req.RealName,
		Avatar:   req.Avatar,
		Gender:   req.Gender,
		Age:      req.Age,
		Remark:   req.Remark,
	}

	err := s.uc.Register(ctx, user)
	if err != nil {
		zap.L().Warn("Register failed", zap.String("phone", pkgstr.MaskPhone(req.Phone)), zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.RegisterResp{
		Code:    200,
		Message: "registered",
	}, nil
}

// Login 用户登录
func (s *UserService) Login(ctx context.Context, req *userv1.LoginReq) (*userv1.LoginResp, error) {
	zap.L().Debug("Login called", zap.String("phone", pkgstr.MaskPhone(req.Phone)))

	token, user, err := s.uc.Login(ctx, req.Phone, req.Password)
	if err != nil {
		zap.L().Warn("Login failed", zap.String("phone", pkgstr.MaskPhone(req.Phone)), zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.LoginResp{
		Code:    200,
		Message: "success",
		Token:   token,
		Data:    s.toUserInfo(user),
	}, nil
}

// Logout 用户登出
func (s *UserService) Logout(ctx context.Context, req *userv1.LogoutReq) (*userv1.LogoutResp, error) {
	zap.L().Debug("Logout called")

	if err := s.uc.Logout(ctx, req.Token); err != nil {
		zap.L().Warn("Logout failed", zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.LogoutResp{
		Code:    200,
		Message: "logged out",
	}, nil
}

// ListUsers 用户列表（分页）
func (s *UserService) ListUsers(ctx context.Context, req *userv1.ListUsersReq) (*userv1.ListUsersResp, error) {
	zap.L().Debug("ListUsers called",
		zap.Int32("page", req.Page),
		zap.Int32("pageSize", req.PageSize),
		zap.String("phone", pkgstr.MaskPhone(req.Phone)),
		zap.String("name", req.Name),
	)

	users, total, err := s.uc.ListUsers(ctx, req.Page, req.PageSize, req.Phone, req.Name)
	if err != nil {
		zap.L().Warn("ListUsers failed", zap.Error(err))
		return nil, s.mapError(err)
	}

	list := make([]*userv1.UserInfo, 0, len(users))
	for _, u := range users {
		list = append(list, s.toUserInfo(u))
	}

	return &userv1.ListUsersResp{
		Code:     200,
		Message:  "success",
		Data:     list,
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
	}, nil
}

// ---- 辅助方法 ----

// toUserInfo 将 biz.User 转换为 proto UserInfo（不含密码，手机号脱敏）
func (s *UserService) toUserInfo(u *biz.User) *userv1.UserInfo {
	return &userv1.UserInfo{
		Id:         u.ID,
		Email:      u.Email,
		Phone:      pkgstr.MaskPhone(u.Phone),
		RealName:   u.RealName,
		Avatar:     u.Avatar,
		Gender:     u.Gender,
		Age:        u.Age,
		CreateTime: u.CreateTime,
		UpdateTime: u.UpdateTime,
		Remark:     u.Remark,
	}
}

func (s *UserService) mapError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, biz.ErrUserNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, biz.ErrPhoneExists), errors.Is(err, biz.ErrEmailExists):
		return status.Error(codes.AlreadyExists, "user already exists")
	case errors.Is(err, biz.ErrInvalidUserID),
		errors.Is(err, biz.ErrPhoneRequired),
		errors.Is(err, biz.ErrPasswordRequired),
		errors.Is(err, biz.ErrNothingToUpdate),
		errors.Is(err, biz.ErrWrongCredentials),
		errors.Is(err, biz.ErrTokenRequired),
		errors.Is(err, biz.ErrInvalidPhone),
		errors.Is(err, biz.ErrInvalidEmail),
		errors.Is(err, biz.ErrPasswordTooShort),
		errors.Is(err, biz.ErrPasswordTooLong),
		errors.Is(err, biz.ErrPasswordNoUpper),
		errors.Is(err, biz.ErrPasswordNoLower),
		errors.Is(err, biz.ErrPasswordNoDigit),
		errors.Is(err, biz.ErrNameTooLong),
		errors.Is(err, biz.ErrRemarkTooLong),
		errors.Is(err, biz.ErrAvatarTooLong),
		errors.Is(err, biz.ErrEmailTooLong),
		errors.Is(err, biz.ErrInvalidGender),
		errors.Is(err, biz.ErrInvalidAge):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		zap.L().Error("unexpected user error", zap.Error(err))
		return status.Error(codes.Internal, "internal error")
	}
}
