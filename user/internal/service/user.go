package service

import (
	"context"

	userv1 "user/api/user/v1"
	"user/internal/biz"

	"github.com/google/wire"
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
	zap.L().Info("GetUser called", zap.Int64("id", req.Id))

	if req.Id <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "invalid user id: %d", req.Id)
	}

	user, err := s.uc.Get(ctx, req.Id)
	if err != nil {
		zap.L().Warn("GetUser failed", zap.Int64("id", req.Id), zap.Error(err))
		return nil, s.mapError(err)
	}
	return &userv1.GetUserResp{
		User: s.toUserInfo(user),
	}, nil
}

// Register 用户注册
func (s *UserService) Register(ctx context.Context, req *userv1.RegisterReq) (*userv1.RegisterResp, error) {
	zap.L().Info("Register called", zap.String("phone", req.Phone))

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

	id, err := s.uc.Register(ctx, user)
	if err != nil {
		zap.L().Warn("Register failed", zap.String("phone", req.Phone), zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.RegisterResp{
		Id:      id,
		Message: "registered",
	}, nil
}

// Login 用户登录
func (s *UserService) Login(ctx context.Context, req *userv1.LoginReq) (*userv1.LoginResp, error) {
	zap.L().Info("Login called", zap.String("phone", req.Phone))

	token, user, err := s.uc.Login(ctx, req.Phone, req.Password)
	if err != nil {
		zap.L().Warn("Login failed", zap.String("phone", req.Phone), zap.Error(err))
		return nil, s.mapError(err)
	}

	return &userv1.LoginResp{
		Token: token,
		User:  s.toUserInfo(user),
	}, nil
}

// ListUsers 用户列表（分页）
func (s *UserService) ListUsers(ctx context.Context, req *userv1.ListUsersReq) (*userv1.ListUsersResp, error) {
	zap.L().Info("ListUsers called",
		zap.Int32("page", req.Page),
		zap.Int32("pageSize", req.PageSize),
		zap.String("phone", req.Phone),
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
		Users:    list,
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
	}, nil
}

// ---- 辅助方法 ----

// toUserInfo 将 biz.User 转换为 proto UserInfo（不含密码）
func (s *UserService) toUserInfo(u *biz.User) *userv1.UserInfo {
	return &userv1.UserInfo{
		Id:         u.ID,
		Email:      u.Email,
		Phone:      u.Phone,
		RealName:   u.RealName,
		Avatar:     u.Avatar,
		Gender:     u.Gender,
		Age:        u.Age,
		CreateTime: u.CreateTime,
		UpdateTime: u.UpdateTime,
		Remark:     u.Remark,
	}
}

// mapError 将业务错误映射为 gRPC 状态码
func (s *UserService) mapError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch msg {
	case "user not found":
		return status.Error(codes.NotFound, msg)
	case "email already exists", "phone already exists":
		return status.Error(codes.AlreadyExists, msg)
	case "invalid user id", "phone is required", "password is required",
		"email is required", "nothing to update",
		"phone or password incorrect":
		return status.Error(codes.InvalidArgument, msg)
	default:
		return status.Error(codes.Internal, msg)
	}
}
