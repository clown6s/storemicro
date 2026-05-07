package data

import (
	"context"
	"database/sql"
	"errors"

	"user/internal/biz"

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
	zap.L().Debug("userRepo.Create", zap.String("name", user.Name), zap.String("email", user.Email))

	result, err := r.data.db.ExecContext(ctx,
		"INSERT INTO users (name, email) VALUES (?, ?)",
		user.Name, user.Email,
	)
	if err != nil {
		zap.L().Error("userRepo.Create failed", zap.Error(err))
		return 0, err
	}

	id, _ := result.LastInsertId()
	zap.L().Info("userRepo.Create success", zap.Int64("id", id))
	return id, nil
}

func (r *userRepo) Get(ctx context.Context, id int64) (*biz.User, error) {
	zap.L().Debug("userRepo.Get", zap.Int64("id", id))

	row := r.data.db.QueryRowContext(ctx,
		"SELECT id, name, email FROM users WHERE id = ?", id,
	)
	user := &biz.User{}
	err := row.Scan(&user.ID, &user.Name, &user.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			zap.L().Warn("userRepo.Get not found", zap.Int64("id", id))
		} else {
			zap.L().Error("userRepo.Get failed", zap.Int64("id", id), zap.Error(err))
		}
		return nil, errors.New("user not found")
	}
	return user, nil
}

func (r *userRepo) Update(ctx context.Context, user *biz.User) error {
	zap.L().Debug("userRepo.Update", zap.Int64("id", user.ID))

	result, err := r.data.db.ExecContext(ctx,
		"UPDATE users SET name = COALESCE(NULLIF(?, ''), name), email = COALESCE(NULLIF(?, ''), email) WHERE id = ?",
		user.Name, user.Email, user.ID,
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

	_, err := r.data.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
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
		"SELECT COUNT(*) FROM users WHERE email = ?", email,
	).Scan(&count)
	if err != nil {
		zap.L().Error("userRepo.EmailExists failed", zap.String("email", email), zap.Error(err))
		return false, err
	}
	return count > 0, nil
}
