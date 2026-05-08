package biz

import (
	"context"
	"errors"
	"regexp"

	"user/internal/pkg/jwt"
	"user/internal/pkg/redis"

	"github.com/google/wire"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewUserUsecase)

// 统一错误变量，避免业务逻辑散落字符串字面量
var (
	ErrUserNotFound     = errors.New("user not found")
	ErrPhoneRequired    = errors.New("phone is required")
	ErrPasswordRequired = errors.New("password is required")
	ErrPhoneExists      = errors.New("phone already exists")
	ErrEmailExists      = errors.New("email already exists")
	ErrInvalidUserID    = errors.New("invalid user id")
	ErrNothingToUpdate  = errors.New("nothing to update")
	ErrWrongCredentials = errors.New("phone or password incorrect")
	ErrTokenRequired    = errors.New("token is required")
	ErrGenerateToken    = errors.New("generate token failed")
	ErrStoreToken       = errors.New("store token failed")
	ErrEncryptPassword  = errors.New("password encryption failed")
	ErrInvalidPhone     = errors.New("invalid phone format")
	ErrInvalidEmail     = errors.New("invalid email format")
	ErrPasswordTooShort = errors.New("password too short (min 6)")
	ErrPasswordTooLong  = errors.New("password too long (max 72 bytes)")
)

// 手机号和邮箱格式正则
var (
	phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)
	emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

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
	Get(ctx context.Context, id int64) (*User, error)
	GetByPhone(ctx context.Context, phone string) (*User, error)
	List(ctx context.Context, page, pageSize int32, phone, name string) ([]*User, int64, error)
	Update(ctx context.Context, user *User) error
	Delete(ctx context.Context, id int64) error
	RegisterTx(ctx context.Context, user *User) error // 事务注册（含唯一性检查）
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

// Register 用户注册（使用事务保证原子性）
func (uc *UserUsecase) Register(ctx context.Context, user *User) error {
	// 参数校验
	if user.Phone == "" {
		zap.L().Warn("validation failed", zap.String("method", "Register"), zap.String("reason", "phone is required"))
		return ErrPhoneRequired
	}
	if !phoneRegex.MatchString(user.Phone) {
		return ErrInvalidPhone
	}
	if user.Password == "" {
		zap.L().Warn("validation failed", zap.String("method", "Register"), zap.String("reason", "password is required"))
		return ErrPasswordRequired
	}
	if len(user.Password) < 6 {
		return ErrPasswordTooShort
	}
	if len(user.Password) > 72 {
		return ErrPasswordTooLong
	}
	if user.Email != "" && !emailRegex.MatchString(user.Email) {
		return ErrInvalidEmail
	}
	if len(user.Phone) > 20 {
		return ErrInvalidPhone
	}

	// bcrypt 加密密码
	hashedPwd, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		zap.L().Error("bcrypt hash failed", zap.Error(err))
		return ErrEncryptPassword
	}
	user.Password = string(hashedPwd)

	// 事务注册（唯一性检查 + 写入原子操作）
	err = uc.repo.RegisterTx(ctx, user)
	if err != nil {
		zap.L().Error("register failed", zap.Error(err))
		return err
	}

	zap.L().Info("user registered", zap.Int64("id", user.ID), zap.String("phone", user.Phone))
	return nil
}

// Login 用户登录（手机号+密码），返回 JWT token 和用户信息
func (uc *UserUsecase) Login(ctx context.Context, phone, password string) (string, *User, error) {
	if phone == "" {
		return "", nil, ErrPhoneRequired
	}
	if password == "" {
		return "", nil, ErrPasswordRequired
	}

	// 查找用户
	user, err := uc.repo.GetByPhone(ctx, phone)
	if err != nil {
		zap.L().Warn("login failed: user not found", zap.String("phone", phone), zap.Error(err))
		return "", nil, ErrWrongCredentials
	}

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		zap.L().Warn("login failed: password incorrect", zap.String("phone", phone))
		return "", nil, ErrWrongCredentials
	}

	// 生成 JWT token
	token, err := uc.jwt.GenerateToken(user.ID, user.Phone)
	if err != nil {
		zap.L().Error("generate token failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", nil, ErrGenerateToken
	}

	// 将 token 存入 Redis（与 JWT 过期时间一致）
	expireHours := uc.jwt.ExpireHours()
	if err := uc.redis.SetToken(ctx, token, user.ID, expireHours); err != nil {
		zap.L().Error("store token to redis failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", nil, ErrStoreToken
	}

	zap.L().Info("user logged in", zap.Int64("id", user.ID), zap.String("phone", phone))
	return token, user, nil
}

// Logout 用户登出（根据 token 删除 Redis 中的 token 双映射）
func (uc *UserUsecase) Logout(ctx context.Context, token string) error {
	if token == "" {
		return ErrTokenRequired
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
		span.SetStatus(codes.Error, err.Error())
		return nil, 0, err
	}

	return users, total, nil
}

// Get 获取用户
func (uc *UserUsecase) Get(ctx context.Context, id int64) (*User, error) {
	if id <= 0 {
		zap.L().Warn("validation failed", zap.String("method", "Get"), zap.String("reason", "invalid user id"))
		return nil, ErrInvalidUserID
	}
	return uc.repo.Get(ctx, id)
}

// Update 更新用户
func (uc *UserUsecase) Update(ctx context.Context, user *User) error {
	if user.ID <= 0 {
		return ErrInvalidUserID
	}
	if user.RealName == "" && user.Email == "" && user.Avatar == "" &&
		user.Gender == 0 && user.Age == 0 && user.Remark == "" {
		return ErrNothingToUpdate
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
	if id <= 0 {
		return ErrInvalidUserID
	}
	err := uc.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	zap.L().Info("user deleted", zap.Int64("id", id))
	return nil
}
