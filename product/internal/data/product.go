package data

import (
	"context"
	"database/sql"
	"errors"

	"product/internal/biz"

	"github.com/go-kratos/kratos/v2/log"
)

type productRepo struct {
	data   *Data
	logger log.Logger
}

func NewProductRepo(data *Data, logger log.Logger) biz.ProductRepo {
	return &productRepo{
		data:   data,
		logger: logger,
	}
}

func (r *productRepo) Get(ctx context.Context, id int64) (*biz.Product, error) {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "productRepo.Get", "id", id)

	row := r.data.db.QueryRowContext(ctx,
		"SELECT id, name, description, price, stock FROM products WHERE id = ?", id,
	)
	p := &biz.Product{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock)
	if err != nil {
		if err == sql.ErrNoRows {
			_log.Warnw("msg", "productRepo.Get not found", "id", id)
		} else {
			_log.Errorw("msg", "productRepo.Get failed", "id", id, "error", err)
		}
		return nil, errors.New("product not found")
	}
	return p, nil
}

func (r *productRepo) List(ctx context.Context, page, pageSize int32) ([]*biz.Product, int32, error) {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "productRepo.List", "page", page, "page_size", pageSize)

	var total int32
	err := r.data.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM products",
	).Scan(&total)
	if err != nil {
		_log.Errorw("msg", "productRepo.List count failed", "error", err)
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := r.data.db.QueryContext(ctx,
		"SELECT id, name, description, price, stock FROM products LIMIT ? OFFSET ?",
		pageSize, offset,
	)
	if err != nil {
		_log.Errorw("msg", "productRepo.List query failed", "error", err)
		return nil, 0, err
	}
	defer rows.Close()

	var items []*biz.Product
	for rows.Next() {
		p := &biz.Product{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock); err != nil {
			_log.Errorw("msg", "productRepo.List scan failed", "error", err)
			return nil, 0, err
		}
		items = append(items, p)
	}
	return items, total, rows.Err()
}

func (r *productRepo) Create(ctx context.Context, p *biz.Product) (int64, error) {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "productRepo.Create", "name", p.Name)

	result, err := r.data.db.ExecContext(ctx,
		"INSERT INTO products (name, description, price, stock) VALUES (?, ?, ?, ?)",
		p.Name, p.Description, p.Price, p.Stock,
	)
	if err != nil {
		_log.Errorw("msg", "productRepo.Create failed", "error", err)
		return 0, err
	}

	id, _ := result.LastInsertId()
	_log.Infow("msg", "productRepo.Create success", "id", id)
	return id, nil
}

func (r *productRepo) Update(ctx context.Context, p *biz.Product) error {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "productRepo.Update", "id", p.ID)

	result, err := r.data.db.ExecContext(ctx,
		"UPDATE products SET name = COALESCE(NULLIF(?, ''), name), description = COALESCE(NULLIF(?, ''), description), price = COALESCE(NULLIF(?, 0), price), stock = COALESCE(NULLIF(?, 0), stock) WHERE id = ?",
		p.Name, p.Description, p.Price, p.Stock, p.ID,
	)
	if err != nil {
		_log.Errorw("msg", "productRepo.Update failed", "id", p.ID, "error", err)
		return err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		_log.Warnw("msg", "productRepo.Update no rows affected", "id", p.ID)
	}
	return nil
}

func (r *productRepo) Delete(ctx context.Context, id int64) error {
	_log := log.NewHelper(r.logger)
	_log.Debugw("msg", "productRepo.Delete", "id", id)

	_, err := r.data.db.ExecContext(ctx, "DELETE FROM products WHERE id = ?", id)
	if err != nil {
		_log.Errorw("msg", "productRepo.Delete failed", "id", id, "error", err)
		return err
	}

	_log.Infow("msg", "productRepo.Delete success", "id", id)
	return nil
}
