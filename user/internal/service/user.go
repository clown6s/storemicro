package service

import (
	"context"
	"errors"
	"strings"

	userv1 "user/api/user/v1"
	"user/internal/biz"

	"github.com/google/wire"
	pkgstr "github.com/storemicro/pkg/strutil"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
		Message: "success",
		Data:    s.toUserInfo(user),
	}, nil
}

// Register 用户注册
func (s *UserService) Register(ctx context.Context, req *userv1.RegisterReq) (*userv1.RegisterResp, error) {
	zap.L().Debug("Register called",
		zap.String("phone", pkgstr.MaskPhone(req.Phone)),
		zap.String("email", pkgstr.MaskEmail(req.Email)),
		zap.String("real_name", req.RealName),
	)

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
		Message: "registered",
	}, nil
}

// Login 用户登录
func (s *UserService) Login(ctx context.Context, req *userv1.LoginReq) (*userv1.LoginResp, error) {
	traceID := trace.SpanFromContext(ctx).SpanContext().TraceID().String()
	zap.L().Debug("Login called",
		zap.String("phone", pkgstr.MaskPhone(req.Phone)),
		zap.String("trace_id", traceID),
	)

	token, user, err := s.uc.Login(ctx, req.Phone, req.Password)
	if err != nil {
		zap.L().Warn("Login failed", zap.String("phone", pkgstr.MaskPhone(req.Phone)), zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.LoginResp{
		Message: "success",
		Token:   token,
		Data:    s.toUserInfo(user),
	}, nil
}

// Logout 用户登出（token 从 gRPC metadata Authorization header 获取）
func (s *UserService) Logout(ctx context.Context, req *userv1.LogoutReq) (*userv1.LogoutResp, error) {
	zap.L().Debug("Logout called")

	// 从 gRPC metadata 读取 Authorization header（gateway 转发）
	token := extractTokenFromMetadata(ctx)
	if token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing authorization token")
	}

	if err := s.uc.Logout(ctx, token); err != nil {
		zap.L().Warn("Logout failed", zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.LogoutResp{
		Message: "logged out",
	}, nil
}

// extractTokenFromMetadata 从 gRPC metadata 提取 Bearer token
func extractTokenFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	// grpc-gateway 转发 HTTP header 时 key 会转小写
	values := md.Get("authorization")
	if len(values) == 0 {
		return ""
	}
	authHeader := values[0]
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return authHeader
}

// ListUsers 用户列表（分页）
func (s *UserService) ListUsers(ctx context.Context, req *userv1.ListUsersReq) (*userv1.ListUsersResp, error) {
	zap.L().Debug("ListUsers called",
		zap.Int32("page", req.Page),
		zap.Int32("pageSize", req.PageSize),
		zap.String("phone", pkgstr.MaskPhone(req.Phone)),
		zap.String("name", req.Name),
	)

	users, total, err := s.uc.ListUsers(ctx, req.Page, req.PageSize, req.Phone, req.Name, req.SortBy, req.SortOrder)
	if err != nil {
		zap.L().Warn("ListUsers failed", zap.Error(err))
		return nil, s.mapError(err)
	}

	list := make([]*userv1.UserInfo, 0, len(users))
	for _, u := range users {
		list = append(list, s.toUserInfo(u))
	}

	return &userv1.ListUsersResp{
		Message:  "success",
		Data:     list,
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
	}, nil
}

// UpdateUser 更新用户资料
func (s *UserService) UpdateUser(ctx context.Context, req *userv1.UpdateUserReq) (*userv1.UpdateUserResp, error) {
	zap.L().Debug("UpdateUser called", zap.Int64("id", req.Id))

	user := &biz.User{
		ID:       req.Id,
		Email:    req.Email,
		RealName: req.RealName,
		Avatar:   req.Avatar,
		Remark:   req.Remark,
	}
	// 1. 先处理 ForceClearFields（表示要清零的字段）
	if len(req.ForceClearFields) > 0 {
		allowed := map[string]bool{"email": true, "remark": true, "avatar": true, "real_name": true, "gender": true, "age": true}
		user.UpdateFields = make(map[string]bool, len(req.ForceClearFields))
		for _, f := range req.ForceClearFields {
			if !allowed[f] {
				return nil, status.Errorf(codes.InvalidArgument, "invalid force clear field: %s", f)
			}
			user.UpdateFields[f] = true
		}
	}
	// 2. 再处理 proto3 optional 字段（指针非 nil 表示客户端显式设置了该字段，优先级高于 ForceClearFields）
	if req.Gender != nil {
		user.Gender = *req.Gender
		if user.UpdateFields == nil {
			user.UpdateFields = make(map[string]bool)
		}
		user.UpdateFields["gender"] = true
	}
	if req.Age != nil {
		user.Age = *req.Age
		if user.UpdateFields == nil {
			user.UpdateFields = make(map[string]bool)
		}
		user.UpdateFields["age"] = true
	}

	if err := s.uc.Update(ctx, user); err != nil {
		zap.L().Warn("UpdateUser failed", zap.Int64("id", req.Id), zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.UpdateUserResp{
		Message: "updated",
	}, nil
}

// DeleteUser 删除用户（软删除）
func (s *UserService) DeleteUser(ctx context.Context, req *userv1.DeleteUserReq) (*userv1.DeleteUserResp, error) {
	zap.L().Debug("DeleteUser called", zap.Int64("id", req.Id))

	if req.Id <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid user id: %d", req.Id)
	}

	if err := s.uc.Delete(ctx, req.Id); err != nil {
		zap.L().Warn("DeleteUser failed", zap.Int64("id", req.Id), zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.DeleteUserResp{
		Message: "deleted",
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
		errors.Is(err, biz.ErrNameTooShort),
		errors.Is(err, biz.ErrRemarkTooLong),
		errors.Is(err, biz.ErrAvatarTooLong),
		errors.Is(err, biz.ErrInvalidAvatar),
		errors.Is(err, biz.ErrEmailTooLong),
		errors.Is(err, biz.ErrPasswordNoSpecial),
		errors.Is(err, biz.ErrInvalidGender),
		errors.Is(err, biz.ErrInvalidAge):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		zap.L().Error("unexpected user error", zap.Error(err))
		return status.Error(codes.Internal, "internal error")
	}
}
