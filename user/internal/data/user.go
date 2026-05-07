package data

import (
	"context"
	"database/sql"
	"errors"

	"user/internal/biz"

	"github.com/go-kratos/kratos/v2/log"
)

type userRepo struct {
	data   *Data
	logger log.Logger
}

// NewUserRepo 创建 UserRepo 实现
func NewUserRepo(data *Data, logger log.Logger) biz.UserRepo {
	return &userRepo{
		data:   data,
		logger: logger,
	}
}

func (r *userRepo) Create(ctx context.Context, user *biz.User) (int64, error) {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "userRepo.Create", "name", user.Name, "email", user.Email)

	result, err := r.data.db.ExecContext(ctx,
		"INSERT INTO users (name, email) VALUES (?, ?)",
		user.Name, user.Email,
	)
	if err != nil {
		_log.Errorw("msg", "userRepo.Create failed", "error", err)
		return 0, err
	}

	id, _ := result.LastInsertId()
	_log.Infow("msg", "userRepo.Create success", "id", id)
	return id, nil
}

func (r *userRepo) Get(ctx context.Context, id int64) (*biz.User, error) {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "userRepo.Get", "id", id)

	row := r.data.db.QueryRowContext(ctx,
		"SELECT id, name, email FROM users WHERE id = ?", id,
	)
	user := &biz.User{}
	err := row.Scan(&user.ID, &user.Name, &user.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			_log.Warnw("msg", "userRepo.Get not found", "id", id)
		} else {
			_log.Errorw("msg", "userRepo.Get failed", "id", id, "error", err)
		}
		return nil, errors.New("user not found")
	}
	return user, nil
}

func (r *userRepo) Update(ctx context.Context, user *biz.User) error {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "userRepo.Update", "id", user.ID)

	result, err := r.data.db.ExecContext(ctx,
		"UPDATE users SET name = COALESCE(NULLIF(?, ''), name), email = COALESCE(NULLIF(?, ''), email) WHERE id = ?",
		user.Name, user.Email, user.ID,
	)
	if err != nil {
		_log.Errorw("msg", "userRepo.Update failed", "id", user.ID, "error", err)
		return err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		_log.Warnw("msg", "userRepo.Update no rows affected", "id", user.ID)
	}
	return nil
}

func (r *userRepo) Delete(ctx context.Context, id int64) error {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "userRepo.Delete", "id", id)

	_, err := r.data.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		_log.Errorw("msg", "userRepo.Delete failed", "id", id, "error", err)
		return err
	}

	_log.Infow("msg", "userRepo.Delete success", "id", id)
	return nil
}

func (r *userRepo) EmailExists(ctx context.Context, email string) (bool, error) {
	_log := log.NewHelper(r.logger)

	var count int
	err := r.data.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM users WHERE email = ?", email,
	).Scan(&count)
	if err != nil {
		_log.Errorw("msg", "userRepo.EmailExists failed", "email", email, "error", err)
		return false, err
	}
	return count > 0, nil
}
