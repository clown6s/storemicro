package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"user/internal/biz"
	"user/internal/pkg/snowflake"

	pkgstr "github.com/storemicro/pkg/strutil"
	"go.uber.org/zap"
)

type userRepo struct {
	data *Data
}

// NewUserRepo 创建 UserRepo 实现
func NewUserRepo(data *Data) biz.UserRepo {
	return &userRepo{data: data}
}

func (r *userRepo) Get(ctx context.Context, id int64) (*biz.User, error) {
	zap.L().Debug("userRepo.Get", zap.Int64("id", id))

	row := r.data.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,''), COALESCE(deleted_at,'')
		 FROM user WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')`, id,
	)
	user := &biz.User{}
	err := row.Scan(&user.ID, &user.RealName, &user.Avatar, &user.Gender, &user.Age,
		&user.Phone, &user.Email, &user.CreateTime, &user.UpdateTime, &user.Remark, &user.DeletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			zap.L().Warn("userRepo.Get not found", zap.Int64("id", id))
			return nil, biz.ErrUserNotFound
		}
		zap.L().Error("userRepo.Get failed", zap.Int64("id", id), zap.Error(err))
		return nil, fmt.Errorf("query user failed: %w", err)
	}
	return user, nil
}

func (r *userRepo) GetByPhone(ctx context.Context, phone string) (*biz.User, error) {
	zap.L().Debug("userRepo.GetByPhone", zap.String("phone", pkgstr.MaskPhone(phone)))

	row := r.data.db.QueryRowContext(ctx,
		`SELECT id, password, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,''), COALESCE(deleted_at,'')
		 FROM user WHERE phone = ? AND (deleted_at IS NULL OR deleted_at = '')`, phone,
	)
	user := &biz.User{}
	err := row.Scan(&user.ID, &user.Password, &user.RealName, &user.Avatar, &user.Gender, &user.Age,
		&user.Phone, &user.Email, &user.CreateTime, &user.UpdateTime, &user.Remark, &user.DeletedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			zap.L().Warn("userRepo.GetByPhone not found", zap.String("phone", pkgstr.MaskPhone(phone)))
			return nil, biz.ErrUserNotFound
		}
		zap.L().Error("userRepo.GetByPhone failed", zap.String("phone", pkgstr.MaskPhone(phone)), zap.Error(err))
		return nil, fmt.Errorf("query user by phone failed: %w", err)
	}
	return user, nil
}

func (r *userRepo) List(ctx context.Context, page, pageSize int32, phone, name string) ([]*biz.User, int64, error) {
	zap.L().Debug("userRepo.List",
		zap.Int32("page", page), zap.Int32("pageSize", pageSize),
		zap.String("phone", pkgstr.MaskPhone(phone)), zap.String("name", name),
	)

	// 防御性校验
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	// 动态拼接 WHERE 条件
	var conditions []string
	var args []any

	conditions = append(conditions, "(deleted_at IS NULL OR deleted_at = '')")

	if phone != "" {
		conditions = append(conditions, "phone LIKE ? ESCAPE '\\\\'")
		args = append(args, "%"+escapeLike(phone)+"%")
	}
	if name != "" {
		conditions = append(conditions, "real_name LIKE ? ESCAPE '\\\\'")
		args = append(args, "%"+escapeLike(name)+"%")
	}

	where := strings.Join(conditions, " AND ")

	// 查总数
	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM user WHERE %s", where)
	err := r.data.db.QueryRowContext(ctx, countSQL, args...).Scan(&total)
	if err != nil {
		zap.L().Error("userRepo.List count failed", zap.Error(err))
		// [M8] 统一错误包装
		return nil, 0, fmt.Errorf("count users failed: %w", err)
	}

	// 分页查询（int64 防止溢出）
	offset := int64(page-1) * int64(pageSize)
	querySQL := fmt.Sprintf(
		`SELECT id, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,'')
		 FROM user WHERE %s
		 ORDER BY id DESC LIMIT ? OFFSET ?`, where,
	)
	queryArgs := append(args, pageSize, offset)

	rows, err := r.data.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		zap.L().Error("userRepo.List query failed", zap.Error(err))
		// [M8] 统一错误包装
		return nil, 0, fmt.Errorf("query users failed: %w", err)
	}
	defer rows.Close()

	users := make([]*biz.User, 0)
	for rows.Next() {
		user := &biz.User{}
		if err := rows.Scan(&user.ID, &user.RealName, &user.Avatar, &user.Gender, &user.Age,
			&user.Phone, &user.Email, &user.CreateTime, &user.UpdateTime, &user.Remark); err != nil {
			zap.L().Error("userRepo.List scan failed", zap.Error(err))
			// [M8] 统一错误包装
			return nil, 0, fmt.Errorf("scan user row failed: %w", err)
		}
		users = append(users, user)
	}

	return users, total, rows.Err()
}

// Update 已废弃：请使用 UpdateTx（包含邮箱唯一性检查的事务更新）
// 保留此方法以保持接口兼容，内部直接调用 UpdateTx
func (r *userRepo) Update(ctx context.Context, user *biz.User) error {
	return r.UpdateTx(ctx, user)
}

// UpdateTx 事务更新：动态 SET + 邮箱唯一性检查原子操作 [H7]
func (r *userRepo) UpdateTx(ctx context.Context, user *biz.User) error {
	tx, err := r.data.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx failed: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	// [H7] 邮箱唯一性检查（如果提供了新邮箱）
	if user.Email != "" {
		var emailCount int
		err = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM user WHERE email = ? AND id != ? AND (deleted_at IS NULL OR deleted_at = '')`,
			user.Email, user.ID,
		).Scan(&emailCount)
		if err != nil {
			return fmt.Errorf("check email uniqueness failed: %w", err)
		}
		if emailCount > 0 {
			return biz.ErrEmailExists
		}
	}

	// 动态构建 SET 子句
	var setClauses []string
	var args []any

	if user.RealName != "" {
		setClauses = append(setClauses, "real_name = ?")
		args = append(args, user.RealName)
	}
	if user.Avatar != "" {
		setClauses = append(setClauses, "avatar = ?")
		args = append(args, user.Avatar)
	}
	if user.Gender >= 0 {
		setClauses = append(setClauses, "gender = ?")
		args = append(args, user.Gender)
	}
	if user.Age >= 0 {
		setClauses = append(setClauses, "age = ?")
		args = append(args, user.Age)
	}
	if user.Email != "" {
		setClauses = append(setClauses, "email = ?")
		args = append(args, user.Email)
	}
	if user.Remark != "" {
		setClauses = append(setClauses, "remark = ?")
		args = append(args, user.Remark)
	}

	if len(setClauses) == 0 {
		zap.L().Debug("userRepo.UpdateTx: no fields to update", zap.Int64("id", user.ID))
		return nil
	}

	args = append(args, user.ID)
	query := fmt.Sprintf("UPDATE user SET %s WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')", strings.Join(setClauses, ", "))

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update user failed: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return biz.ErrUserNotFound
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit tx failed: %w", err)
	}
	committed = true

	return nil
}

func (r *userRepo) Delete(ctx context.Context, id int64) error {
	zap.L().Debug("userRepo.Delete", zap.Int64("id", id))

	result, err := r.data.db.ExecContext(ctx,
		`UPDATE user SET deleted_at = ? WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')`,
		time.Now().UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		zap.L().Error("userRepo.Delete failed", zap.Int64("id", id), zap.Error(err))
		// [M8] 统一错误包装
		return fmt.Errorf("delete user failed: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return biz.ErrUserNotFound
	}

	zap.L().Info("userRepo.Delete success", zap.Int64("id", id))
	return nil
}

// escapeLike 转义 SQL LIKE 通配符（% _ \）
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// RegisterTx 事务注册：唯一性检查 + 创建用户原子操作，回填 user.ID
func (r *userRepo) RegisterTx(ctx context.Context, user *biz.User) error {
	tx, err := r.data.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx failed: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	// 手机号唯一性检查（SELECT FOR UPDATE 锁行，防止并发）
	var phoneCount int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user WHERE phone = ? AND (deleted_at IS NULL OR deleted_at = '') FOR UPDATE`, user.Phone,
	).Scan(&phoneCount)
	if err != nil {
		return fmt.Errorf("check phone failed: %w", err)
	}
	if phoneCount > 0 {
		return biz.ErrPhoneExists
	}

	// 邮箱唯一性检查（如果提供了邮箱）
	if user.Email != "" {
		var emailCount int
		err = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM user WHERE email = ? AND (deleted_at IS NULL OR deleted_at = '') FOR UPDATE`, user.Email,
		).Scan(&emailCount)
		if err != nil {
			return fmt.Errorf("check email failed: %w", err)
		}
		if emailCount > 0 {
			return biz.ErrEmailExists
		}
	}

	// 生成 ID 并写入
	id, err := snowflake.GenID()
	if err != nil {
		return fmt.Errorf("generate snowflake id failed: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO user (id, password, real_name, avatar, gender, age, phone, email, remark)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, user.Password, user.RealName, user.Avatar, user.Gender, user.Age,
		user.Phone, user.Email, user.Remark,
	)
	if err != nil {
		return fmt.Errorf("insert user failed: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit tx failed: %w", err)
	}
	committed = true

	// 回填 ID
	user.ID = id
	return nil
}
