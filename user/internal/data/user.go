package data

import (
	"context"
	"database/sql"
	"errors"

	"user/internal/biz"
	"user/internal/pkg/snowflake"

	"go.uber.org/zap"
)

type userRepo struct {
	data *Data
}

// NewUserRepo 创建 UserRepo 实现
func NewUserRepo(data *Data) biz.UserRepo {
	return &userRepo{data: data}
}

func (r *userRepo) Create(ctx context.Context, user *biz.User) (int64, error) {
	zap.L().Debug("userRepo.Create",
		zap.String("phone", user.Phone),
		zap.String("email", user.Email),
	)

	// 使用雪花算法生成 ID
	id := snowflake.GenID()

	_, err := r.data.db.ExecContext(ctx,
		`INSERT INTO user (id, password, real_name, avatar, gender, age, phone, email, remark)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, user.Password, user.RealName, user.Avatar, user.Gender, user.Age,
		user.Phone, user.Email, user.Remark,
	)
	if err != nil {
		zap.L().Error("userRepo.Create failed", zap.Error(err))
		return 0, err
	}

	zap.L().Info("userRepo.Create success", zap.Int64("id", id))
	return id, nil
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
		} else {
			zap.L().Error("userRepo.Get failed", zap.Int64("id", id), zap.Error(err))
		}
		return nil, errors.New("user not found")
	}
	return user, nil
}

func (r *userRepo) GetByPhone(ctx context.Context, phone string) (*biz.User, error) {
	zap.L().Debug("userRepo.GetByPhone", zap.String("phone", phone))

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
			zap.L().Warn("userRepo.GetByPhone not found", zap.String("phone", phone))
		} else {
			zap.L().Error("userRepo.GetByPhone failed", zap.String("phone", phone), zap.Error(err))
		}
		return nil, errors.New("user not found")
	}
	return user, nil
}

func (r *userRepo) List(ctx context.Context, page, pageSize int32) ([]*biz.User, int64, error) {
	zap.L().Debug("userRepo.List", zap.Int32("page", page), zap.Int32("pageSize", pageSize))

	// 查总数
	var total int64
	err := r.data.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user WHERE deleted_at IS NULL OR deleted_at = ''`,
	).Scan(&total)
	if err != nil {
		zap.L().Error("userRepo.List count failed", zap.Error(err))
		return nil, 0, err
	}

	// 分页查询
	offset := (page - 1) * pageSize
	rows, err := r.data.db.QueryContext(ctx,
		`SELECT id, COALESCE(real_name,''), COALESCE(avatar,''), gender, COALESCE(age,0),
		        phone, COALESCE(email,''), create_time, update_time, COALESCE(remark,'')
		 FROM user WHERE deleted_at IS NULL OR deleted_at = ''
		 ORDER BY id DESC LIMIT ? OFFSET ?`, pageSize, offset,
	)
	if err != nil {
		zap.L().Error("userRepo.List query failed", zap.Error(err))
		return nil, 0, err
	}
	defer rows.Close()

	users := make([]*biz.User, 0)
	for rows.Next() {
		user := &biz.User{}
		err := rows.Scan(&user.ID, &user.RealName, &user.Avatar, &user.Gender, &user.Age,
			&user.Phone, &user.Email, &user.CreateTime, &user.UpdateTime, &user.Remark)
		if err != nil {
			zap.L().Error("userRepo.List scan failed", zap.Error(err))
			continue
		}
		users = append(users, user)
	}

	return users, total, nil
}

func (r *userRepo) Update(ctx context.Context, user *biz.User) error {
	zap.L().Debug("userRepo.Update", zap.Int64("id", user.ID))

	result, err := r.data.db.ExecContext(ctx,
		`UPDATE user SET
		   real_name = COALESCE(NULLIF(?, ''), real_name),
		   avatar    = COALESCE(NULLIF(?, ''), avatar),
		   gender    = COALESCE(NULLIF(?, 0), gender),
		   age       = COALESCE(NULLIF(?, 0), age),
		   email     = COALESCE(NULLIF(?, ''), email),
		   remark    = COALESCE(NULLIF(?, ''), remark)
		 WHERE id = ?`,
		user.RealName, user.Avatar, user.Gender, user.Age, user.Email, user.Remark, user.ID,
	)
	if err != nil {
		zap.L().Error("userRepo.Update failed", zap.Int64("id", user.ID), zap.Error(err))
		return err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		zap.L().Warn("userRepo.Update no rows affected", zap.Int64("id", user.ID))
	}
	return nil
}

func (r *userRepo) Delete(ctx context.Context, id int64) error {
	zap.L().Debug("userRepo.Delete", zap.Int64("id", id))

	// 逻辑删除
	_, err := r.data.db.ExecContext(ctx,
		`UPDATE user SET deleted_at = '1' WHERE id = ?`, id,
	)
	if err != nil {
		zap.L().Error("userRepo.Delete failed", zap.Int64("id", id), zap.Error(err))
		return err
	}

	zap.L().Info("userRepo.Delete success", zap.Int64("id", id))
	return nil
}

func (r *userRepo) EmailExists(ctx context.Context, email string) (bool, error) {
	var count int
	err := r.data.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user WHERE email = ? AND (deleted_at IS NULL OR deleted_at = '')`, email,
	).Scan(&count)
	if err != nil {
		zap.L().Error("userRepo.EmailExists failed", zap.String("email", email), zap.Error(err))
		return false, err
	}
	return count > 0, nil
}

func (r *userRepo) PhoneExists(ctx context.Context, phone string) (bool, error) {
	var count int
	err := r.data.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user WHERE phone = ? AND (deleted_at IS NULL OR deleted_at = '')`, phone,
	).Scan(&count)
	if err != nil {
		zap.L().Error("userRepo.PhoneExists failed", zap.String("phone", phone), zap.Error(err))
		return false, err
	}
	return count > 0, nil
}
