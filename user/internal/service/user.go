package service

import (
	"context"
	"strconv"

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
	zap.L().Info("GetUser called", zap.String("id", req.Id))

	id, err := strconv.ParseInt(req.Id, 10, 64)
	if err != nil {
		zap.L().Warn("invalid user id", zap.String("id", req.Id), zap.Error(err))
		return nil, status.Errorf(codes.InvalidArgument, "invalid user id: %s", req.Id)
	}

	user, err := s.uc.Get(ctx, id)
	if err != nil {
		zap.L().Warn("GetUser failed", zap.Int64("id", id), zap.Error(err))
		return nil, s.mapError(err)
	}
	return &userv1.GetUserResp{
		Id:    user.ID,
		Name:  user.Name,
		Email: user.Email,
	}, nil
}

func (s *UserService) CreateUser(ctx context.Context, req *userv1.CreateUserReq) (*userv1.CreateUserResp, error) {
	zap.L().Info("CreateUser called", zap.String("name", req.Name), zap.String("email", req.Email))

	user, err := s.uc.Create(ctx, req.Name, req.Email)
	if err != nil {
		zap.L().Warn("CreateUser failed", zap.String("name", req.Name), zap.Error(err))
		return nil, s.mapError(err)
	}
	return &userv1.CreateUserResp{
		Id:      user.ID,
		Message: "created",
	}, nil
}

func (s *UserService) Ping(ctx context.Context, req *userv1.PingReq) (*userv1.PingResp, error) {
	return &userv1.PingResp{Message: "pong"}, nil
}

func (s *UserService) mapError(err error) error {
	if err == nil {
		return nil
	}
	switch err.Error() {
	case "user not found":
		return status.Error(codes.NotFound, err.Error())
	case "email already exists":
		return status.Error(codes.AlreadyExists, err.Error())
	case "invalid user id", "name is required", "email is required", "nothing to update":
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
