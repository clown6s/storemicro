package biz

import (
	"context"
	"errors"

	"user/internal/pkg/jwt"
	"user/internal/pkg/redis"

	"github.com/google/wire"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewUserUsecase)

// User 领域模型（对应数据库 user 表全部字段）
type User struct {
	ID         int64
	Password   string // bcrypt 加密后的密码
	RealName   string
	Avatar     string
	Gender     int32
	Age        int32
	Phone      string
	Email      string
	CreateTime string
	UpdateTime string
	Remark     string
	DeletedAt  string
}

// UserRepo 数据仓库接口（由 data 层实现）
type UserRepo interface {
	Create(ctx context.Context, user *User) (int64, error)
	Get(ctx context.Context, id int64) (*User, error)
	GetByPhone(ctx context.Context, phone string) (*User, error)
	List(ctx context.Context, page, pageSize int32, phone, name string) ([]*User, int64, error)
	Update(ctx context.Context, user *User) error
	Delete(ctx context.Context, id int64) error
	EmailExists(ctx context.Context, email string) (bool, error)
	PhoneExists(ctx context.Context, phone string) (bool, error)
}

// UserUsecase 用户用例
type UserUsecase struct {
	repo  UserRepo
	jwt   *jwt.JWT
	redis *redis.Redis
}

// NewUserUsecase 创建用户用例
func NewUserUsecase(repo UserRepo, j *jwt.JWT, r *redis.Redis) *UserUsecase {
	return &UserUsecase{repo: repo, jwt: j, redis: r}
}

// Register 用户注册
func (uc *UserUsecase) Register(ctx context.Context, user *User) (int64, error) {
	// 参数校验
	if user.Phone == "" {
		zap.L().Warn("validation failed", zap.String("method", "Register"), zap.String("reason", "phone is required"))
		return 0, errors.New("phone is required")
	}
	if user.Password == "" {
		zap.L().Warn("validation failed", zap.String("method", "Register"), zap.String("reason", "password is required"))
		return 0, errors.New("password is required")
	}

	// 手机号唯一性
	exists, err := uc.repo.PhoneExists(ctx, user.Phone)
	if err != nil {
		zap.L().Error("check phone failed", zap.String("phone", user.Phone), zap.Error(err))
		return 0, err
	}
	if exists {
		zap.L().Warn("phone already exists", zap.String("phone", user.Phone))
		return 0, errors.New("phone already exists")
	}

	// 邮箱唯一性（如果提供了邮箱）
	if user.Email != "" {
		exists, err = uc.repo.EmailExists(ctx, user.Email)
		if err != nil {
			zap.L().Error("check email failed", zap.String("email", user.Email), zap.Error(err))
			return 0, err
		}
		if exists {
			zap.L().Warn("email already exists", zap.String("email", user.Email))
			return 0, errors.New("email already exists")
		}
	}

	// bcrypt 加密密码
	hashedPwd, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		zap.L().Error("bcrypt hash failed", zap.Error(err))
		return 0, errors.New("password encryption failed")
	}
	user.Password = string(hashedPwd)

	// 写入数据库
	id, err := uc.repo.Create(ctx, user)
	if err != nil {
		zap.L().Error("create user failed", zap.Error(err))
		return 0, err
	}

	zap.L().Info("user registered", zap.Int64("id", id), zap.String("phone", user.Phone))
	return id, nil
}

// Login 用户登录（手机号+密码），返回 JWT token 和用户信息
func (uc *UserUsecase) Login(ctx context.Context, phone, password string) (string, *User, error) {
	if phone == "" {
		return "", nil, errors.New("phone is required")
	}
	if password == "" {
		return "", nil, errors.New("password is required")
	}

	// 查找用户
	user, err := uc.repo.GetByPhone(ctx, phone)
	if err != nil {
		zap.L().Warn("login failed: user not found", zap.String("phone", phone), zap.Error(err))
		return "", nil, errors.New("phone or password incorrect")
	}

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		zap.L().Warn("login failed: password incorrect", zap.String("phone", phone))
		return "", nil, errors.New("phone or password incorrect")
	}

	// 生成 JWT token
	token, err := uc.jwt.GenerateToken(user.ID, user.Phone)
	if err != nil {
		zap.L().Error("generate token failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", nil, errors.New("generate token failed")
	}

	// 将 token 存入 Redis（与 JWT 过期时间一致）
	expireHours := uc.jwt.ExpireHours()
	if err := uc.redis.SetToken(ctx, token, user.ID, expireHours); err != nil {
		zap.L().Error("store token to redis failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", nil, errors.New("store token failed")
	}

	zap.L().Info("user logged in", zap.Int64("id", user.ID), zap.String("phone", phone))
	return token, user, nil
}

// Logout 用户登出（根据 token 删除 Redis 中的 token 双映射）
func (uc *UserUsecase) Logout(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("token is required")
	}

	// 从 Redis 查 userID
	userID, err := uc.redis.GetUserIDByToken(ctx, token)
	if err != nil {
		zap.L().Warn("logout: token not found in redis", zap.Error(err))
		// token 已不存在，视为已登出
		return nil
	}

	// 删除 token 双映射
	if err := uc.redis.DeleteToken(ctx, token, userID); err != nil {
		zap.L().Error("logout: delete token failed", zap.Int64("userID", userID), zap.Error(err))
		return err
	}

	zap.L().Info("user logged out", zap.Int64("userID", userID))
	return nil
}

// ListUsers 用户列表（分页）
func (uc *UserUsecase) ListUsers(ctx context.Context, page, pageSize int32, phone, name string) ([]*User, int64, error) {
	// 创建自定义埋点 span
	ctx, span := otel.Tracer("user-service").Start(ctx, "ListUsers",
		trace.WithAttributes(
			attribute.Int("list.page", int(page)),
			attribute.Int("list.pageSize", int(pageSize)),
		),
	)
	defer span.End()

	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	users, total, err := uc.repo.List(ctx, page, pageSize, phone, name)

	// 记录结果到 span
	span.SetAttributes(
		attribute.Int64("list.total", total),
		attribute.Int("list.result_count", len(users)),
	)
	if err != nil {
		span.RecordError(err)
	}

	return users, total, err
}

// Get 获取用户
func (uc *UserUsecase) Get(ctx context.Context, id int64) (*User, error) {
	if id == 0 {
		zap.L().Warn("validation failed", zap.String("method", "Get"), zap.String("reason", "invalid user id"))
		return nil, errors.New("invalid user id")
	}
	return uc.repo.Get(ctx, id)
}

// Create 创建用户（旧接口保留兼容）
func (uc *UserUsecase) Create(ctx context.Context, phone, email string) (*User, error) {
	if phone == "" {
		return nil, errors.New("phone is required")
	}
	if email == "" {
		return nil, errors.New("email is required")
	}
	exists, err := uc.repo.EmailExists(ctx, email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New("email already exists")
	}
	user := &User{Phone: phone, Email: email}
	id, err := uc.repo.Create(ctx, user)
	if err != nil {
		return nil, err
	}
	user.ID = id
	return user, nil
}

// Update 更新用户
func (uc *UserUsecase) Update(ctx context.Context, user *User) error {
	if user.ID == 0 {
		return errors.New("invalid user id")
	}
	if user.RealName == "" && user.Email == "" && user.Avatar == "" {
		return errors.New("nothing to update")
	}
	err := uc.repo.Update(ctx, user)
	if err != nil {
		return err
	}
	zap.L().Info("user updated", zap.Int64("id", user.ID))
	return nil
}

// Delete 删除用户
func (uc *UserUsecase) Delete(ctx context.Context, id int64) error {
	if id == 0 {
		return nil
	}
	err := uc.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	zap.L().Info("user deleted", zap.Int64("id", id))
	return nil
}
