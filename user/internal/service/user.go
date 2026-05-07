package service

import (
	"context"

	userv1 "user/api/user/v1"
	"user/internal/biz"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProviderSet is service providers.
var ProviderSet = wire.NewSet(NewUserService)

type UserService struct {
	userv1.UnimplementedUserServer
	uc     *biz.UserUsecase
	logger log.Logger
}

// NewUserService 创建用户 gRPC 服务
func NewUserService(uc *biz.UserUsecase, logger log.Logger) *UserService {
	return &UserService{uc: uc, logger: logger}
}

func (s *UserService) GetUser(ctx context.Context, req *userv1.GetUserReq) (*userv1.GetUserResp, error) {
	_log := log.NewHelper(s.logger)
	_log.Infow("msg", "GetUser called", "id", req.Id)

	user, err := s.uc.Get(ctx, req.Id)
	if err != nil {
		_log.Warnw("msg", "GetUser failed", "id", req.Id, "error", err)
		return nil, s.mapError(err)
	}
	return &userv1.GetUserResp{
		Id:    user.ID,
		Name:  user.Name,
		Email: user.Email,
	}, nil
}

func (s *UserService) CreateUser(ctx context.Context, req *userv1.CreateUserReq) (*userv1.CreateUserResp, error) {
	_log := log.NewHelper(s.logger)
	_log.Infow("msg", "CreateUser called", "name", req.Name, "email", req.Email)

	user, err := s.uc.Create(ctx, req.Name, req.Email)
	if err != nil {
		_log.Warnw("msg", "CreateUser failed", "name", req.Name, "error", err)
		return nil, s.mapError(err)
	}
	return &userv1.CreateUserResp{
		Id:      user.ID,
		Message: "created",
	}, nil
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
