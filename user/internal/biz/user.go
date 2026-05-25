package biz

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"user/internal/pkg/common"
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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// bcryptCost 使用公共常量，与 data 层保持一致
const bcryptCost = common.BCryptCost

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewUserUsecase)

// 统一错误变量，避免业务逻辑散落字符串字面量
var (
	ErrUserNotFound            = errors.New("user not found")
	ErrPhoneRequired           = errors.New("phone is required")
	ErrPasswordRequired        = errors.New("password is required")
	ErrPhoneExists             = errors.New("phone already exists")
	ErrEmailExists             = errors.New("email already exists")
	ErrInvalidUserID           = errors.New("invalid user id")
	ErrNothingToUpdate         = errors.New("nothing to update")
	ErrWrongCredentials        = errors.New("phone or password incorrect")
	ErrTokenRequired           = errors.New("token is required")
	ErrGenerateToken           = errors.New("generate token failed")
	ErrStoreToken              = errors.New("store token failed")
	ErrEncryptPassword         = errors.New("password encryption failed")
	ErrInvalidPhone            = errors.New("invalid phone format")
	ErrInvalidEmail            = errors.New("invalid email format")
	ErrPasswordTooShort        = errors.New("password too short (min 8)")
	ErrPasswordTooLong         = errors.New("password too long (max 72 bytes)")
	ErrPasswordNoUpper         = errors.New("password must contain at least one uppercase letter")
	ErrPasswordNoLower         = errors.New("password must contain at least one lowercase letter")
	ErrPasswordNoDigit         = errors.New("password must contain at least one digit")
	ErrPasswordNoSpecial       = errors.New("password must contain at least one special character")
	ErrNameTooShort            = errors.New("real name too short (min 2 characters)")
	ErrNameTooLong             = errors.New("real name too long (max 50)")
	ErrRemarkTooLong           = errors.New("remark too long (max 200)")
	ErrAvatarTooLong           = errors.New("avatar URL too long (max 500)")
	ErrInvalidAvatar           = errors.New("avatar must be an HTTP(S) URL")
	ErrEmailTooLong            = errors.New("email too long (max 254)")
	ErrInvalidGender           = errors.New("gender must be 0 (unknown), 1 (male), or 2 (female)")
	ErrInvalidAge              = errors.New("age must be between 0 and 150")
	ErrWrongOldPassword        = errors.New("old password is incorrect")
	ErrPasswordSameAsOld       = errors.New("new password must differ from old password")
	ErrInvalidRefreshToken     = errors.New("invalid or expired refresh token")
	ErrRegisterRateLimited     = errors.New("too many registration attempts, please try again later")
	ErrTokenInvalidationFailed = errors.New("password changed but token invalidation incomplete, please re-login")
)

// 手机号和邮箱格式正则
var (
	phoneRegex  = regexp.MustCompile(`^1[3-9]\d{9}$`)
	emailRegex  = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	avatarRegex = regexp.MustCompile(`^https?://\S+$`)
	hasUpper    = regexp.MustCompile(`[A-Z]`)
	hasLower    = regexp.MustCompile(`[a-z]`)
	hasDigit    = regexp.MustCompile(`\d`)
	hasSpecial  = regexp.MustCompile(`[^a-zA-Z0-9]`) // #16 fix: 匹配任意非字母数字字符（含反引号、空格等）
)

// [C3] 用于 timing-safe login 的 dummy bcrypt hash（cost=10, 随机 salt）
// #18: 使用 lazy init + 重试，避免 sync.Once 首次失败后永久失效
var dummyHash []byte
var dummyHashOnce sync.Once
var dummyHashErr error

// getDummyHash 延迟生成 dummy hash（仅在首次 Login 时调用），失败时重试最多 3 次
func getDummyHash() []byte {
	dummyHashOnce.Do(func() {
		for attempt := 1; attempt <= 3; attempt++ {
			dummyHash, dummyHashErr = bcrypt.GenerateFromPassword([]byte("dummy-timing-safe-hash"), 12)
			if dummyHashErr == nil {
				return
			}
			zap.L().Error("failed to generate dummy bcrypt hash, retrying",
				zap.Int("attempt", attempt), zap.Int("max", 3), zap.Error(dummyHashErr))
		}
		zap.L().Error("failed to generate dummy bcrypt hash after 3 attempts, timing-safe login disabled")
	})
	return dummyHash
}

// User 领域模型（对应数据库 user 表全部字段）
// UpdateFields 标记需要强制更新的字段（用于区分"未设置"和"设置为空值"），仅在 Update/UpdateTx 时有效
type User struct {
	ID           int64
	Password     string // bcrypt 加密后的密码，WARNING: 仅在 Login/Register 流程中赋值，返回前必须调用 ClearPassword()
	RealName     string
	Avatar       string
	Gender       int32
	Age          int32
	Phone        string
	Email        string
	CreateTime   *timestamppb.Timestamp
	UpdateTime   *timestamppb.Timestamp
	Remark       string
	DeletedAt    string
	UpdateFields map[string]bool // 需要强制更新为零值的字段名
}

// ClearPassword 清除密码 hash，防止在 biz 层之外传播。所有返回 User 的代码路径都必须调用。
func (u *User) ClearPassword() { u.Password = "" }

// UserRepo 数据仓库接口（由 data 层实现）
type UserRepo interface {
	Get(ctx context.Context, id int64) (*User, error)
	GetByPhone(ctx context.Context, phone string) (*User, error)
	List(ctx context.Context, page, pageSize int32, phone, name, sortBy, sortOrder string) ([]*User, int64, error)
	UpdateTx(ctx context.Context, user *User) error                                            // 事务更新（含唯一性检查）
	ChangePasswordTx(ctx context.Context, userID int64, oldPassword, newPassword string) error // 事务改密码（防 TOCTOU）
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

// redisCtx 为 Redis 操作创建带 3s 超时的 context（防止上游无 deadline 时阻塞）
func (uc *UserUsecase) redisCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 3*time.Second)
}

// validateUserFields 校验用户字段（Register 和 Update 共享），isRegister=true 时执行全量校验
func validateUserFields(user *User, isRegister bool) error {
	if user.Email != "" || (user.UpdateFields != nil && user.UpdateFields["email"]) {
		if len(user.Email) > 254 {
			return ErrEmailTooLong
		}
		if user.Email != "" && !emailRegex.MatchString(user.Email) {
			return ErrInvalidEmail
		}
	}
	if user.Gender < 0 || user.Gender > 2 {
		return ErrInvalidGender
	}
	if user.Age < 0 || user.Age > 150 {
		return ErrInvalidAge
	}
	if len(user.RealName) > 0 && len(user.RealName) < 2 {
		return ErrNameTooShort
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
	if user.Avatar != "" && !avatarRegex.MatchString(user.Avatar) {
		return ErrInvalidAvatar
	}
	return nil
}

// validatePassword 校验密码复杂度（Register 和 ChangePassword 共享）
func validatePassword(password string) error {
	if len(password) < 8 {
		return ErrPasswordTooShort
	}
	if len(password) > 72 {
		return ErrPasswordTooLong
	}
	if !hasUpper.MatchString(password) {
		return ErrPasswordNoUpper
	}
	if !hasLower.MatchString(password) {
		return ErrPasswordNoLower
	}
	if !hasDigit.MatchString(password) {
		return ErrPasswordNoDigit
	}
	if !hasSpecial.MatchString(password) {
		return ErrPasswordNoSpecial
	}
	return nil
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

	// 手机号注册频率限制（5分钟内同一手机号只能注册一次）
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if err := uc.redis.CheckRegisterPhoneRateLimit(rCtx, user.Phone); err != nil {
			zap.L().Warn("register rate limited", zap.String("phone", pkgstr.MaskPhone(user.Phone)))
			return ErrRegisterRateLimited
		}
	}

	// 密码复杂度校验
	if err := validatePassword(user.Password); err != nil {
		return err
	}

	// 共享字段校验（Register 使用全量校验）
	if err := validateUserFields(user, true); err != nil {
		return err
	}

	// bcrypt 加密密码
	hashedPwd, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcryptCost)
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

// Login 用户登录（手机号+密码），返回 access token、refresh token 和用户信息
func (uc *UserUsecase) Login(ctx context.Context, phone, password string) (string, string, *User, error) {
	if phone == "" {
		return "", "", nil, ErrPhoneRequired
	}
	if !phoneRegex.MatchString(phone) {
		return "", "", nil, ErrInvalidPhone
	}
	if password == "" {
		return "", "", nil, ErrPasswordRequired
	}

	// 查找用户
	user, err := uc.repo.GetByPhone(ctx, phone)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// [C3] timing-safe: 用户不存在时对 dummy hash 做 bcrypt 比较，防止通过响应时间差异枚举手机号
			// #15 fix: h == nil 时用 time.Sleep 模拟 bcrypt 耗时，保持 timing-safe
			if h := getDummyHash(); h != nil {
				_ = bcrypt.CompareHashAndPassword(h, []byte(password))
			} else {
				// dummy hash 生成失败（3次重试后），用 sleep 模拟 bcrypt.DefaultCost 耗时（约 100ms）
				time.Sleep(100 * time.Millisecond)
			}
			return "", "", nil, ErrWrongCredentials
		}
		// 数据库错误，包装为内部错误返回（避免泄露连接信息）
		zap.L().Error("login failed: db error", zap.String("phone", pkgstr.MaskPhone(phone)), zap.Error(err))
		return "", "", nil, fmt.Errorf("internal error: %w", err)
	}
	// #14 fix: GetByPhone 返回后立即 defer 清除密码 hash，确保所有错误路径都不会泄露
	defer user.ClearPassword()

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		zap.L().Warn("login failed: password incorrect", zap.String("phone", pkgstr.MaskPhone(phone)))
		return "", "", nil, ErrWrongCredentials
	}

	// 生成 access token
	token, err := uc.jwt.GenerateToken(user.ID)
	if err != nil {
		zap.L().Error("generate token failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", "", nil, ErrGenerateToken
	}

	// 生成 refresh token
	refreshToken, err := uc.jwt.GenerateRefreshToken(user.ID)
	if err != nil {
		zap.L().Error("generate refresh token failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", "", nil, ErrGenerateToken
	}

	// 原子存储 access token + refresh token（单次 Lua 调用，避免部分失败导致状态不一致）
	expireHours := uc.jwt.ExpireHours()
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if err := uc.redis.LoginStoreTokens(rCtx, token, refreshToken, user.ID, int64(expireHours/time.Hour), jwt.RefreshExpireHours); err != nil {
			zap.L().Error("store tokens to redis failed", zap.Int64("id", user.ID), zap.Error(err))
			return "", "", nil, ErrStoreToken
		}
	}

	zap.L().Info("user logged in", zap.Int64("id", user.ID), zap.String("phone", pkgstr.MaskPhone(phone)))
	user.ClearPassword() // 显式清除密码 hash，防止通过返回值泄露（defer 作为安全保障）
	return token, refreshToken, user, nil
}

// Logout 用户登出（删除 access token + refresh token 的 Redis 映射）
func (uc *UserUsecase) Logout(ctx context.Context, token string, userID int64) error {
	if token == "" {
		return ErrTokenRequired
	}

	// 删除 access token 双映射
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if err := uc.redis.DeleteToken(rCtx, token, userID); err != nil {
			zap.L().Error("logout: delete access token failed", zap.Int64("userID", userID), zap.Error(err))
			return err
		}
	}

	// 删除 refresh token（通过反向映射，单设备模式只保留一个）
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if err := uc.redis.InvalidateUserRefreshTokens(rCtx, userID); err != nil {
			zap.L().Warn("logout: delete refresh token failed", zap.Int64("userID", userID), zap.Error(err))
			// refresh token 删除失败不影响登出成功（access token 已删除）
		}
	}

	zap.L().Info("user logged out", zap.Int64("userID", userID))
	return nil
}

// GetUserIDByToken 通过 token 获取 userID（供 service 层鉴权使用）
func (uc *UserUsecase) GetUserIDByToken(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, ErrTokenRequired
	}
	rCtx, rCancel := uc.redisCtx(ctx)
	defer rCancel()
	return uc.redis.GetUserIDByToken(rCtx, token)
}

// ListUsers sortBy 白名单（service 层防御性校验，data 层有独立校验做兜底）
var allowedSortBy = map[string]bool{
	"id": true, "createTime": true, "updateTime": true,
}

// ListUsers 用户列表（分页）
func (uc *UserUsecase) ListUsers(ctx context.Context, page, pageSize int32, phone, name, sortBy, sortOrder string) ([]*User, int64, error) {
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
	if page > 10000 {
		page = 10000
	}
	if phone != "" {
		// 支持模糊查询：长度 3-11 位，只包含数字，以 1 开头
		if len(phone) < 3 || len(phone) > 11 || !strings.HasPrefix(phone, "1") {
			return nil, 0, ErrInvalidPhone
		}
		for _, c := range phone {
			if c < '0' || c > '9' {
				return nil, 0, ErrInvalidPhone
			}
		}
	}
	if len(name) > 50 {
		return nil, 0, ErrNameTooLong
	}
	if name != "" && len(name) < 2 {
		return nil, 0, ErrNameTooShort
	}
	if sortBy != "" && !allowedSortBy[sortBy] {
		sortBy = "id" // 非法值回退默认
	}
	if sortOrder != "" && !strings.EqualFold(sortOrder, "asc") && !strings.EqualFold(sortOrder, "desc") {
		sortOrder = "" // 非法值清空，让 data 层用默认值
	}

	users, total, err := uc.repo.List(ctx, page, pageSize, phone, name, sortBy, sortOrder)
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

	// 共享字段校验（与 Register 一致，含 avatar URL 格式正则 + real_name 最短 2 字符）
	if err := validateUserFields(user, false); err != nil {
		return err
	}

	// 邮箱唯一性检查 — 用事务更新
	err := uc.repo.UpdateTx(ctx, user)
	if err != nil {
		return err
	}
	zap.L().Info("user updated", zap.Int64("id", user.ID))
	return nil
}

// Delete 删除用户（软删除 + 清除 Redis 活跃 token）
func (uc *UserUsecase) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrInvalidUserID
	}
	err := uc.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	// 清除该用户的所有活跃 token，防止已删除用户继续使用旧 token
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if invalidateErr := uc.redis.InvalidateUserTokens(rCtx, id); invalidateErr != nil {
			zap.L().Error("delete: failed to invalidate user tokens",
				zap.Int64("id", id), zap.Error(invalidateErr))
			// token 清理失败不阻塞删除操作（token 会在过期后自动失效）
		}
	}
	zap.L().Info("user deleted", zap.Int64("id", id))
	return nil
}

// ChangePassword 修改密码（需验证旧密码，使用事务保证原子性）
func (uc *UserUsecase) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	if oldPassword == "" || newPassword == "" {
		return ErrPasswordRequired
	}

	// 检查新旧密码不能相同
	if oldPassword == newPassword {
		return ErrPasswordSameAsOld
	}

	// 密码复杂度校验
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	// 事务内完成：读取当前密码 → 校验旧密码 → 更新新密码（防 TOCTOU 竞态）
	if err := uc.repo.ChangePasswordTx(ctx, userID, oldPassword, newPassword); err != nil {
		return err
	}

	// 修改密码后使该用户所有 token 失效（access + refresh）
	hasErr := false
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if invalidateErr := uc.redis.InvalidateUserTokens(rCtx, userID); invalidateErr != nil {
			zap.L().Error("change password: failed to invalidate access tokens",
				zap.Int64("id", userID), zap.Error(invalidateErr))
			hasErr = true
		}
	}
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if invalidateErr := uc.redis.InvalidateUserRefreshTokens(rCtx, userID); invalidateErr != nil {
			zap.L().Error("change password: failed to invalidate refresh tokens",
				zap.Int64("id", userID), zap.Error(invalidateErr))
			hasErr = true
		}
	}
	if hasErr {
		return ErrTokenInvalidationFailed
	}

	zap.L().Info("password changed", zap.Int64("id", userID))
	return nil
}

// RefreshToken 使用 refresh token 刷新 access token（含 JWT 签名校验 + 旋转策略）
func (uc *UserUsecase) RefreshToken(ctx context.Context, refreshToken string) (string, string, *User, error) {
	if refreshToken == "" {
		return "", "", nil, ErrTokenRequired
	}

	// 1. 先解析并校验 JWT 签名、audience、过期时间
	claims, err := uc.jwt.ParseRefreshToken(refreshToken)
	if err != nil {
		zap.L().Warn("refresh: invalid refresh token jwt", zap.Error(err))
		return "", "", nil, ErrInvalidRefreshToken
	}
	userID := claims.UserID

	// 2. 从 Redis 验证 refresh token 是否有效（未被撤销）
	var redisUserID int64
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		var redisErr error
		redisUserID, redisErr = uc.redis.GetRefreshTokenUserID(rCtx, refreshToken)
		if redisErr != nil {
			zap.L().Warn("refresh: token not found in redis", zap.Int64("userID", userID), zap.Error(redisErr))
			return "", "", nil, ErrInvalidRefreshToken
		}
	}

	// 3. 校验 JWT claims 中的 userID 与 Redis 一致
	if redisUserID != userID {
		zap.L().Warn("refresh: userID mismatch",
			zap.Int64("jwt_user_id", userID),
			zap.Int64("redis_user_id", redisUserID))
		return "", "", nil, ErrInvalidRefreshToken
	}

	// 4. 获取用户信息
	user, err := uc.repo.Get(ctx, userID)
	if err != nil {
		return "", "", nil, err
	}

	// 5. 生成新的 refresh token（旋转策略：旧 token 失效，新 token 生效）
	newRefreshToken, err := uc.jwt.GenerateRefreshToken(user.ID)
	if err != nil {
		zap.L().Error("refresh: generate refresh token failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", "", nil, ErrGenerateToken
	}

	// 6. 原子旋转 refresh token（CAS 校验 + 删旧 + 写新，单次 Lua 调用）
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if err := uc.redis.RotateRefreshToken(rCtx, refreshToken, newRefreshToken, user.ID, jwt.RefreshExpireHours); err != nil {
			zap.L().Error("refresh: rotate refresh token failed", zap.Int64("id", user.ID), zap.Error(err))
			return "", "", nil, ErrStoreToken
		}
	}

	// 7. 生成新的 access token（在 refresh token 旋转成功之后，确保原子性）
	token, err := uc.jwt.GenerateToken(user.ID)
	if err != nil {
		zap.L().Error("refresh: generate token failed", zap.Int64("id", user.ID), zap.Error(err))
		return "", "", nil, ErrGenerateToken
	}

	// 8. 存储 access token 到 Redis
	expireHours := uc.jwt.ExpireHours()
	{
		rCtx, rCancel := uc.redisCtx(ctx)
		defer rCancel()
		if err := uc.redis.SetToken(rCtx, token, user.ID, expireHours); err != nil {
			zap.L().Error("refresh: store token failed", zap.Int64("id", user.ID), zap.Error(err))
			return "", "", nil, ErrStoreToken
		}
	}

	zap.L().Info("token refreshed", zap.Int64("id", user.ID))
	return token, newRefreshToken, user, nil
}
