package biz

import (
	"context"
	"errors"

	"github.com/google/wire"
	"go.uber.org/zap"
)

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewUserUsecase)

// User 领域模型
type User struct {
	ID    int64
	Name  string
	Email string
}

// UserRepo 数据仓库接口（由 data 层实现）
type UserRepo interface {
	Create(ctx context.Context, user *User) (int64, error)
	Get(ctx context.Context, id int64) (*User, error)
	Update(ctx context.Context, user *User) error
	Delete(ctx context.Context, id int64) error
	EmailExists(ctx context.Context, email string) (bool, error)
}

// UserUsecase 用户用例
type UserUsecase struct {
	repo UserRepo
}

// NewUserUsecase 创建用户用例
func NewUserUsecase(repo UserRepo) *UserUsecase {
	return &UserUsecase{repo: repo}
}

// Create 创建用户
func (uc *UserUsecase) Create(ctx context.Context, name, email string) (*User, error) {
	if name == "" {
		zap.L().Warn("validation failed", zap.String("method", "Create"), zap.String("reason", "name is required"))
		return nil, errors.New("name is required")
	}
	if email == "" {
		zap.L().Warn("validation failed", zap.String("method", "Create"), zap.String("reason", "email is required"))
		return nil, errors.New("email is required")
	}

	exists, err := uc.repo.EmailExists(ctx, email)
	if err != nil {
		zap.L().Error("check email failed", zap.String("email", email), zap.Error(err))
		return nil, err
	}
	if exists {
		zap.L().Warn("email already exists", zap.String("email", email))
		return nil, errors.New("email already exists")
	}

	user := &User{Name: name, Email: email}
	id, err := uc.repo.Create(ctx, user)
	if err != nil {
		zap.L().Error("create user failed", zap.Error(err))
		return nil, err
	}

	user.ID = id
	zap.L().Info("user created", zap.Int64("id", id), zap.String("name", name), zap.String("email", email))
	return user, nil
}

// Get 获取用户
func (uc *UserUsecase) Get(ctx context.Context, id int64) (*User, error) {
	if id <= 0 {
		zap.L().Warn("validation failed", zap.String("method", "Get"), zap.String("reason", "invalid user id"))
		return nil, errors.New("invalid user id")
	}
	return uc.repo.Get(ctx, id)
}

// Update 更新用户
func (uc *UserUsecase) Update(ctx context.Context, user *User) error {
	if user.ID <= 0 {
		zap.L().Warn("validation failed", zap.String("method", "Update"), zap.String("reason", "invalid user id"))
		return errors.New("invalid user id")
	}
	if user.Name == "" && user.Email == "" {
		zap.L().Warn("validation failed", zap.String("method", "Update"), zap.String("reason", "nothing to update"))
		return errors.New("nothing to update")
	}

	err := uc.repo.Update(ctx, user)
	if err != nil {
		zap.L().Error("update user failed", zap.Int64("id", user.ID), zap.Error(err))
		return err
	}

	zap.L().Info("user updated", zap.Int64("id", user.ID))
	return nil
}

// Delete 删除用户
func (uc *UserUsecase) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		zap.L().Warn("validation failed", zap.String("method", "Delete"), zap.String("reason", "invalid user id"))
		return nil
	}

	err := uc.repo.Delete(ctx, id)
	if err != nil {
		zap.L().Error("delete user failed", zap.Int64("id", id), zap.Error(err))
		return err
	}

	zap.L().Info("user deleted", zap.Int64("id", id))
	return nil
}
