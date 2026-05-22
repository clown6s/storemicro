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

	"github.com/go-sql-driver/mysql"
	pkgstr "github.com/storemicro/pkg/strutil"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type userRepo struct {
	data *Data
}

// parseTimestamp 将 RFC3339 时间字符串转为 *timestamppb.Timestamp
func parseTimestamp(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
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
	var createTimeStr, updateTimeStr string
	err := row.Scan(&user.ID, &user.RealName, &user.Avatar, &user.Gender, &user.Age,
		&user.Phone, &user.Email, &createTimeStr, &updateTimeStr, &user.Remark, &user.DeletedAt)
	if err == nil {
		user.CreateTime = parseTimestamp(createTimeStr)
		user.UpdateTime = parseTimestamp(updateTimeStr)
	}
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
	var createTimeStr, updateTimeStr string
	err := row.Scan(&user.ID, &user.Password, &user.RealName, &user.Avatar, &user.Gender, &user.Age,
		&user.Phone, &user.Email, &createTimeStr, &updateTimeStr, &user.Remark, &user.DeletedAt)
	if err == nil {
		user.CreateTime = parseTimestamp(createTimeStr)
		user.UpdateTime = parseTimestamp(updateTimeStr)
	}
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

// #21: 排序字段白名单（包级变量，避免每次请求重建）
var allowedSortBy = map[string]string{
	"id":         "id",
	"createTime": "create_time",
	"updateTime": "update_time",
}

func (r *userRepo) List(ctx context.Context, page, pageSize int32, phone, name, sortBy, sortOrder string) ([]*biz.User, int64, error) {
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
	if page > 10000 {
		page = 10000
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

	// #18 fix: 使用只读事务确保 COUNT 和 SELECT 快照一致性，避免并发写入导致 total 与结果不匹配
	tx, err := r.data.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, fmt.Errorf("begin read-only tx failed: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			zap.L().Error("userRepo.List rollback failed", zap.Error(err))
		}
	}()

	// 查总数（事务内执行，确保快照一致）
	var total int64
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM user WHERE %s", where)
	err = tx.QueryRowContext(ctx, countSQL, args...).Scan(&total)
	if err != nil {
		zap.L().Error("userRepo.List count failed", zap.Error(err))
		// [M8] 统一错误包装
		return nil, 0, fmt.Errorf("count users failed: %w", err)
	}

	// 排序参数白名单校验（防止 SQL 注入，使用包级 allowedSortBy）
	sortCol, ok := allowedSortBy[sortBy]
	if !ok {
		sortCol = "id" // 默认按 id 排序
	}
	sortDir := "DESC"
	if strings.EqualFold(sortOrder, "asc") {
		sortDir = "ASC"
	}
	orderBy := sortCol + " " + sortDir

	// 分页查询（int64 防止溢出，事务内执行）
	offset := int64(page-1) * int64(pageSize)
	querySQL := fmt.Sprintf(
		`SELECT id, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,'')
		 FROM user WHERE %s
		 ORDER BY %s LIMIT ? OFFSET ?`, where, orderBy,
	)
	queryArgs := append(args, pageSize, offset)

	rows, err := tx.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		zap.L().Error("userRepo.List query failed", zap.Error(err))
		// [M8] 统一错误包装
		return nil, 0, fmt.Errorf("query users failed: %w", err)
	}
	defer rows.Close()

	users := make([]*biz.User, 0)
	for rows.Next() {
		user := &biz.User{}
		var createTimeStr, updateTimeStr string
		if err := rows.Scan(&user.ID, &user.RealName, &user.Avatar, &user.Gender, &user.Age,
			&user.Phone, &user.Email, &createTimeStr, &updateTimeStr, &user.Remark); err != nil {
			zap.L().Error("userRepo.List scan failed", zap.Error(err))
			// [M8] 统一错误包装
			return nil, 0, fmt.Errorf("scan user row failed: %w", err)
		}
		user.CreateTime = parseTimestamp(createTimeStr)
		user.UpdateTime = parseTimestamp(updateTimeStr)
		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		zap.L().Error("userRepo.List rows error", zap.Error(err))
		return nil, 0, fmt.Errorf("list users rows error: %w", err)
	}

	// 提交只读事务（释放快照资源）
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit read-only tx failed: %w", err)
	}

	return users, total, nil
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
			if err := tx.Rollback(); err != nil {
				zap.L().Error("UpdateTx rollback failed", zap.Error(err))
			}
		}
	}()

	// [H7] 邮箱唯一性检查（如果提供了新邮箱）
	if user.Email != "" {
		var emailCount int
		err = tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM user WHERE email = ? AND id != ? AND (deleted_at IS NULL OR deleted_at = '') FOR UPDATE`,
			user.Email, user.ID,
		).Scan(&emailCount)
		if err != nil {
			return fmt.Errorf("check email uniqueness failed: %w", err)
		}
		if emailCount > 0 {
			return biz.ErrEmailExists
		}
	}

	// 动态构建 SET 子句（支持将字段重置为零值：通过 UpdateFields 标记）
	var setClauses []string
	var args []any

	// forceSet 白名单：只允许客户端强制清空以下字段，防止未来新增字段时遗漏
	allowedForceFields := map[string]bool{
		"realName": true, "avatar": true, "gender": true,
		"age": true, "email": true, "remark": true,
	}
	forceSet := func(field string) bool {
		return user.UpdateFields != nil && user.UpdateFields[field] && allowedForceFields[field]
	}

	if user.RealName != "" || forceSet("realName") {
		setClauses = append(setClauses, "real_name = ?")
		args = append(args, user.RealName)
	}
	if user.Avatar != "" || forceSet("avatar") {
		setClauses = append(setClauses, "avatar = ?")
		args = append(args, user.Avatar)
	}
	if forceSet("gender") {
		setClauses = append(setClauses, "gender = ?")
		args = append(args, user.Gender)
	}
	if forceSet("age") {
		setClauses = append(setClauses, "age = ?")
		args = append(args, user.Age)
	}
	if user.Email != "" || forceSet("email") {
		if user.Email == "" {
			// 清空 email 时写 NULL，避免空字符串触发 unique index 冲突
			setClauses = append(setClauses, "email = NULL")
		} else {
			setClauses = append(setClauses, "email = ?")
			args = append(args, user.Email)
		}
	}
	if user.Remark != "" || forceSet("remark") {
		setClauses = append(setClauses, "remark = ?")
		args = append(args, user.Remark)
	}

	if len(setClauses) == 0 {
		zap.L().Debug("userRepo.UpdateTx: no fields to update", zap.Int64("id", user.ID))
		return biz.ErrNothingToUpdate
	}

	args = append(args, user.ID)
	query := fmt.Sprintf("UPDATE user SET %s WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')", strings.Join(setClauses, ", "))

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		// 捕获唯一索引冲突（errno 1062），映射为业务错误
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			zap.L().Warn("update: duplicate entry",
				zap.Int64("id", user.ID),
				zap.String("message", mysqlErr.Message),
			)
			if strings.Contains(mysqlErr.Message, "email") {
				return biz.ErrEmailExists
			}
			if strings.Contains(mysqlErr.Message, "phone") {
				return biz.ErrPhoneExists
			}
		}
		return fmt.Errorf("update user failed: %w", err)
	}

	affected, raErr := result.RowsAffected()
	if raErr != nil {
		return fmt.Errorf("get rows affected failed: %w", raErr)
	}
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

	affected, raErr := result.RowsAffected()
	if raErr != nil {
		return fmt.Errorf("get rows affected failed: %w", raErr)
	}
	if affected == 0 {
		// 软删除幂等：用户可能已被删除，不再报 ErrUserNotFound（防止用户 ID 枚举）
		zap.L().Debug("userRepo.Delete: no rows affected (already deleted)", zap.Int64("id", id))
		return nil
	}
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
			if err := tx.Rollback(); err != nil {
				zap.L().Error("RegisterTx rollback failed", zap.Error(err))
			}
		}
	}()

	// [Fix] 清理同手机号/邮箱的软删除记录，避免唯一键冲突
	// 注意：此处使用硬删除（DELETE），前提是系统中无外键引用 user.id 的关联表。
	// 如果未来添加了订单表等引用 user.id 的表，应改为 UPDATE 将 phone/email 置空。
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user WHERE phone = ? AND deleted_at IS NOT NULL AND deleted_at != ''`, user.Phone,
	); err != nil {
		return fmt.Errorf("cleanup soft-deleted phone records failed: %w", err)
	}
	if user.Email != "" {
		// 清理所有软删除记录中同邮箱的记录（不限定手机号，防止跨手机号邮箱重用导致唯一键冲突）
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM user WHERE email = ? AND deleted_at IS NOT NULL AND deleted_at != ''`, user.Email,
		); err != nil {
			return fmt.Errorf("cleanup soft-deleted email records failed: %w", err)
		}
	}

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

	// #17: 未提供 email 时写入 NULL，避免空字符串触发唯一索引冲突
	// #19: 显式设置 create_time/update_time，不依赖数据库 DEFAULT 值
	now := time.Now().UTC().Format(time.RFC3339)
	var insertSQL string
	var insertArgs []any
	if user.Email == "" {
		insertSQL = `INSERT INTO user (id, password, real_name, avatar, gender, age, phone, remark, create_time, update_time)
		             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		insertArgs = []any{id, user.Password, user.RealName, user.Avatar, user.Gender, user.Age,
			user.Phone, user.Remark, now, now}
	} else {
		insertSQL = `INSERT INTO user (id, password, real_name, avatar, gender, age, phone, email, remark, create_time, update_time)
		             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		insertArgs = []any{id, user.Password, user.RealName, user.Avatar, user.Gender, user.Age,
			user.Phone, user.Email, user.Remark, now, now}
	}
	_, err = tx.ExecContext(ctx, insertSQL, insertArgs...)
	if err != nil {
		// 捕获唯一索引冲突（errno 1062），映射为业务错误
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			zap.L().Warn("register: duplicate entry on insert",
				zap.Int64("id", id),
				zap.String("phone", pkgstr.MaskPhone(user.Phone)),
				zap.Uint16("errno", mysqlErr.Number),
				zap.String("message", mysqlErr.Message),
			)
			// 根据错误消息判断是手机号还是邮箱冲突
			if strings.Contains(mysqlErr.Message, "phone") {
				return biz.ErrPhoneExists
			}
			if strings.Contains(mysqlErr.Message, "email") {
				return biz.ErrEmailExists
			}
			return biz.ErrPhoneExists // 默认返回手机号冲突
		}
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

func (r *userRepo) UpdatePassword(ctx context.Context, id int64, hashedPassword string) error {
	result, err := r.data.db.ExecContext(ctx,
		`UPDATE user SET password = ?, update_time = ? WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')`,
		hashedPassword, time.Now().UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		zap.L().Error("userRepo.UpdatePassword failed", zap.Int64("id", id), zap.Error(err))
		return fmt.Errorf("update password failed: %w", err)
	}
	affected, raErr := result.RowsAffected()
	if raErr != nil {
		return fmt.Errorf("get rows affected failed: %w", raErr)
	}
	if affected == 0 {
		return biz.ErrUserNotFound
	}
	return nil
}
