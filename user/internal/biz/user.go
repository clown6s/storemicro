package biz

import (
	"context"
	"errors"
	"regexp"

	"user/internal/pkg/jwt"
	"user/internal/pkg/redis"

	"github.com/google/wire"
	pkgstr "github.com/storemicro/pkg/strutil"
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
	ErrUserNotFound      = errors.New("user not found")
	ErrPhoneRequired     = errors.New("phone is required")
	ErrPasswordRequired  = errors.New("password is required")
	ErrPhoneExists       = errors.New("phone already exists")
	ErrEmailExists       = errors.New("email already exists")
	ErrInvalidUserID     = errors.New("invalid user id")
	ErrNothingToUpdate   = errors.New("nothing to update")
	ErrWrongCredentials  = errors.New("phone or password incorrect")
	ErrTokenRequired     = errors.New("token is required")
	ErrGenerateToken     = errors.New("generate token failed")
	ErrStoreToken        = errors.New("store token failed")
	ErrEncryptPassword   = errors.New("password encryption failed")
	ErrInvalidPhone      = errors.New("invalid phone format")
	ErrInvalidEmail      = errors.New("invalid email format")
	ErrPasswordTooShort  = errors.New("password too short (min 8)")
	ErrPasswordTooLong   = errors.New("password too long (max 72 bytes)")
	ErrPasswordNoUpper   = errors.New("password must contain at least one uppercase letter")
	ErrPasswordNoLower   = errors.New("password must contain at least one lowercase letter")
	ErrPasswordNoDigit   = errors.New("password must contain at least one digit")
	ErrPasswordNoSpecial = errors.New("password must contain at least one special character")
	ErrNameTooLong       = errors.New("real name too long (max 50)")
	ErrRemarkTooLong     = errors.New("remark too long (max 200)")
	ErrAvatarTooLong     = errors.New("avatar URL too long (max 500)")
	ErrEmailTooLong      = errors.New("email too long (max 254)")
	ErrInvalidGender     = errors.New("gender must be 0 (unknown), 1 (male), or 2 (female)")
	ErrInvalidAge        = errors.New("age must be between 0 and 150")
)

// 手机号和邮箱格式正则
var (
	phoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)
	emailRegex = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	hasUpper   = regexp.MustCompile(`[A-Z]`)
	hasLower   = regexp.MustCompile(`[a-z]`)
	hasDigit   = regexp.MustCompile(`\d`)
	hasSpecial = regexp.MustCompile(`[!@#$%^&*()_+\-=\[\]{};':"\\|,.<>/?~]`)
)

// [C3] 用于 timing-safe login 的 dummy bcrypt hash（cost=10, 随机 salt）
// 每次启动时预计算，登录失败时用这个 hash 做 CompareHashAndPassword 均衡耗时
var dummyHash []byte

func init() {
	var err error
	dummyHash, err = bcrypt.GenerateFromPassword([]byte("dummy-timing-safe-hash"), bcrypt.DefaultCost)
	if err != nil {
		panic("failed to generate dummy bcrypt hash for timing-safe login: " + err.Error())
	}
}

// User 领域模型（对应数据库 user 表全部字段）
// UpdateFields 标记需要强制更新的字段（用于区分"未设置"和"设置为空值"），仅在 Update/UpdateTx 时有效
type User struct {
	ID           int64
	Password     string // bcrypt 加密后的密码
	RealName     string
	Avatar       string
	Gender       int32
	Age          int32
	Phone        string
	Email        string
	CreateTime   string
	UpdateTime   string
	Remark       string
	DeletedAt    string
	UpdateFields map[string]bool // 需要强制更新为零值的字段名
}

// UserRepo 数据仓库接口（由 data 层实现）
type UserRepo interface {
	Get(ctx context.Context, id int64) (*User, error)
	GetByPhone(ctx context.Context, phone string) (*User, error)
	List(ctx context.Context, page, pageSize int32, phone, name string) ([]*User, int64, error)
	Update(ctx context.Context, user *User) error
	UpdateTx(ctx context.Context, user *User) error // 事务更新（含唯一性检查）
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
	if len(user.Password) < 8 {
		return ErrPasswordTooShort
	}
	if len(user.Password) > 72 {
		return ErrPasswordTooLong
	}
	if !hasUpper.MatchString(user.Password) {
		return ErrPasswordNoUpper
	}
	if !hasLower.MatchString(user.Password) {
		return ErrPasswordNoLower
	}
	if !hasDigit.MatchString(user.Password) {
		return ErrPasswordNoDigit
	}
	if !hasSpecial.MatchString(user.Password) {
		return ErrPasswordNoSpecial
	}
	if user.Email != "" {
		if len(user.Email) > 254 {
			return ErrEmailTooLong
		}
		if !emailRegex.MatchString(user.Email) {
			return ErrInvalidEmail
		}
	}
	// [M10] Gender 校验：限定 0/1/2
	if user.Gender < 0 || user.Gender > 2 {
		return ErrInvalidGender
	}
	// [M10] Age 校验：限定 0-150
	if user.Age < 0 || user.Age > 150 {
		return ErrInvalidAge
	}
	if len(user.RealName) > 50 {
		return ErrNameTooLong
	}
	if len(user.Remark) > 200 {
		return ErrRemarkTooLong
	}
	if len(user.Avatar) > 500 {
		return ErrAvatarTooLong
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

	zap.L().Info("user registered", zap.Int64("id", user.ID), zap.String("phone", pkgstr.MaskPhone(user.Phone)))
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
		if errors.Is(err, ErrUserNotFound) {
			// [C3] timing-safe: 用户不存在时对 dummy hash 做 bcrypt 比较，防止通过响应时间差异枚举手机号
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
			return "", nil, ErrWrongCredentials
		}
		// 数据库错误，直接返回，不做 dummy 比较（避免在 DB 故障时也产生 bcrypt 延迟）
		zap.L().Error("login failed: db error", zap.String("phone", pkgstr.MaskPhone(phone)), zap.Error(err))
		return "", nil, err
	}

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		zap.L().Warn("login failed: password incorrect", zap.String("phone", pkgstr.MaskPhone(phone)))
		return "", nil, ErrWrongCredentials
	}

	// 生成 JWT token（只包含 user_id，不含 phone 等 PII）
	token, err := uc.jwt.GenerateToken(user.ID)
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

	zap.L().Info("user logged in", zap.Int64("id", user.ID), zap.String("phone", pkgstr.MaskPhone(phone)))
	user.Password = "" // 防御性清除：不在 biz 层之外传播密码 hash
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
	if phone != "" && !phoneRegex.MatchString(phone) {
		return nil, 0, ErrInvalidPhone
	}
	if len(name) > 50 {
		return nil, 0, ErrNameTooLong
	}

	users, total, err := uc.repo.List(ctx, page, pageSize, phone, name)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, 0, err
	}

	// 记录结果到 span（仅在成功时）
	span.SetAttributes(
		attribute.Int64("list.total", total),
		attribute.Int("list.result_count", len(users)),
	)

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

// Update 更新用户（string 空值不更新，int32 < 0 不更新，int32 0 是有效值）
func (uc *UserUsecase) Update(ctx context.Context, user *User) error {
	if user.ID <= 0 {
		return ErrInvalidUserID
	}
	// [M10] Gender/Age 校验
	if user.Gender >= 0 && user.Gender > 2 {
		return ErrInvalidGender
	}
	if user.Age >= 0 && user.Age > 150 {
		return ErrInvalidAge
	}
	// [H7] 邮箱唯一性检查 — 用事务更新
	err := uc.repo.UpdateTx(ctx, user)
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
