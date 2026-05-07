package biz

import (
	"context"
	"errors"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
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
	repo   UserRepo
	logger log.Logger
}

// NewUserUsecase 创建用户用例
func NewUserUsecase(repo UserRepo, logger log.Logger) *UserUsecase {
	return &UserUsecase{repo: repo, logger: logger}
}

// Create 创建用户
func (uc *UserUsecase) Create(ctx context.Context, name, email string) (*User, error) {
	_log := log.NewHelper(uc.logger)

	if name == "" {
		_log.Warnw("msg", "validation failed", "method", "Create", "reason", "name is required")
		return nil, errors.New("name is required")
	}
	if email == "" {
		_log.Warnw("msg", "validation failed", "method", "Create", "reason", "email is required")
		return nil, errors.New("email is required")
	}

	exists, err := uc.repo.EmailExists(ctx, email)
	if err != nil {
		_log.Errorw("msg", "check email failed", "email", email, "error", err)
		return nil, err
	}
	if exists {
		_log.Warnw("msg", "email already exists", "email", email)
		return nil, errors.New("email already exists")
	}

	user := &User{Name: name, Email: email}
	id, err := uc.repo.Create(ctx, user)
	if err != nil {
		_log.Errorw("msg", "create user failed", "error", err)
		return nil, err
	}

	user.ID = id
	_log.Infow("msg", "user created", "id", id, "name", name, "email", email)
	return user, nil
}

// Get 获取用户
func (uc *UserUsecase) Get(ctx context.Context, id int64) (*User, error) {
	_log := log.NewHelper(uc.logger)

	if id <= 0 {
		_log.Warnw("msg", "validation failed", "method", "Get", "reason", "invalid user id")
		return nil, errors.New("invalid user id")
	}
	return uc.repo.Get(ctx, id)
}

// Update 更新用户
func (uc *UserUsecase) Update(ctx context.Context, user *User) error {
	_log := log.NewHelper(uc.logger)

	if user.ID <= 0 {
		_log.Warnw("msg", "validation failed", "method", "Update", "reason", "invalid user id")
		return errors.New("invalid user id")
	}
	if user.Name == "" && user.Email == "" {
		_log.Warnw("msg", "validation failed", "method", "Update", "reason", "nothing to update")
		return errors.New("nothing to update")
	}

	err := uc.repo.Update(ctx, user)
	if err != nil {
		_log.Errorw("msg", "update user failed", "id", user.ID, "error", err)
		return err
	}

	_log.Infow("msg", "user updated", "id", user.ID)
	return nil
}

// Delete 删除用户
func (uc *UserUsecase) Delete(ctx context.Context, id int64) error {
	_log := log.NewHelper(uc.logger)

	if id <= 0 {
		_log.Warnw("msg", "validation failed", "method", "Delete", "reason", "invalid user id")
		return errors.New("invalid user id")
	}

	err := uc.repo.Delete(ctx, id)
	if err != nil {
		_log.Errorw("msg", "delete user failed", "id", id, "error", err)
		return err
	}

	_log.Infow("msg", "user deleted", "id", id)
	return nil
}
