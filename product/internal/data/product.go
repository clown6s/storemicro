package data

import (
	"context"
	"database/sql"
	"errors"

	"product/internal/biz"

	"go.uber.org/zap"
)

type productRepo struct {
	data *Data
}

func NewProductRepo(data *Data) biz.ProductRepo {
	return &productRepo{data: data}
}

func (r *productRepo) Get(ctx context.Context, id int64) (*biz.Product, error) {
	zap.L().Debug("productRepo.Get", zap.Int64("id", id))

	row := r.data.db.QueryRowContext(ctx,
		"SELECT id, name, description, price, stock FROM products WHERE id = ?", id,
	)
	p := &biz.Product{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock)
	if err != nil {
		if err == sql.ErrNoRows {
			zap.L().Warn("productRepo.Get not found", zap.Int64("id", id))
		} else {
			zap.L().Error("productRepo.Get failed", zap.Int64("id", id), zap.Error(err))
		}
		return nil, errors.New("product not found")
	}
	return p, nil
}

func (r *productRepo) List(ctx context.Context, page, pageSize int32) ([]*biz.Product, int32, error) {
	zap.L().Debug("productRepo.List", zap.Int32("page", page), zap.Int32("page_size", pageSize))

	var total int32
	err := r.data.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM products",
	).Scan(&total)
	if err != nil {
		zap.L().Error("productRepo.List count failed", zap.Error(err))
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	rows, err := r.data.db.QueryContext(ctx,
		"SELECT id, name, description, price, stock FROM products LIMIT ? OFFSET ?",
		pageSize, offset,
	)
	if err != nil {
		zap.L().Error("productRepo.List query failed", zap.Error(err))
		return nil, 0, err
	}
	defer rows.Close()

	var items []*biz.Product
	for rows.Next() {
		p := &biz.Product{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock); err != nil {
			zap.L().Error("productRepo.List scan failed", zap.Error(err))
			return nil, 0, err
		}
		items = append(items, p)
	}
	return items, total, rows.Err()
}

func (r *productRepo) Create(ctx context.Context, p *biz.Product) (int64, error) {
	zap.L().Debug("productRepo.Create", zap.String("name", p.Name))

	result, err := r.data.db.ExecContext(ctx,
		"INSERT INTO products (name, description, price, stock) VALUES (?, ?, ?, ?)",
		p.Name, p.Description, p.Price, p.Stock,
	)
	if err != nil {
		zap.L().Error("productRepo.Create failed", zap.Error(err))
		return 0, err
	}

	id, _ := result.LastInsertId()
	zap.L().Info("productRepo.Create success", zap.Int64("id", id))
	return id, nil
}

func (r *productRepo) Update(ctx context.Context, p *biz.Product) error {
	zap.L().Debug("productRepo.Update", zap.Int64("id", p.ID))

	result, err := r.data.db.ExecContext(ctx,
		"UPDATE products SET name = COALESCE(NULLIF(?, ''), name), description = COALESCE(NULLIF(?, ''), description), price = COALESCE(NULLIF(?, 0), price), stock = COALESCE(NULLIF(?, 0), stock) WHERE id = ?",
		p.Name, p.Description, p.Price, p.Stock, p.ID,
	)
	if err != nil {
		zap.L().Error("productRepo.Update failed", zap.Int64("id", p.ID), zap.Error(err))
		return err
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		zap.L().Warn("productRepo.Update no rows affected", zap.Int64("id", p.ID))
	}
	return nil
}

func (r *productRepo) Delete(ctx context.Context, id int64) error {
	zap.L().Debug("productRepo.Delete", zap.Int64("id", id))

	_, err := r.data.db.ExecContext(ctx, "DELETE FROM products WHERE id = ?", id)
	if err != nil {
		zap.L().Error("productRepo.Delete failed", zap.Int64("id", id), zap.Error(err))
		return err
	}

	zap.L().Info("productRepo.Delete success", zap.Int64("id", id))
	return nil
}
