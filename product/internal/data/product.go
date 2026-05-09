package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

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
		"SELECT id, name, description, price, stock FROM products WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')", id,
	)
	p := &biz.Product{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Price, &p.Stock)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			zap.L().Warn("productRepo.Get not found", zap.Int64("id", id))
			return nil, biz.ErrProductNotFound
		}
		zap.L().Error("productRepo.Get failed", zap.Int64("id", id), zap.Error(err))
		return nil, err
	}
	return p, nil
}

func (r *productRepo) List(ctx context.Context, page, pageSize int32) ([]*biz.Product, int32, error) {
	zap.L().Debug("productRepo.List", zap.Int32("page", page), zap.Int32("page_size", pageSize))

	var total int32
	err := r.data.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM products WHERE (deleted_at IS NULL OR deleted_at = '')",
	).Scan(&total)
	if err != nil {
		zap.L().Error("productRepo.List count failed", zap.Error(err))
		return nil, 0, err
	}

	offset := int64(page-1) * int64(pageSize)
	rows, err := r.data.db.QueryContext(ctx,
		"SELECT id, name, description, price, stock FROM products WHERE (deleted_at IS NULL OR deleted_at = '') LIMIT ? OFFSET ?",
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

	id, err := result.LastInsertId()
	if err != nil {
		zap.L().Error("productRepo.Create LastInsertId failed", zap.Error(err))
		return 0, err
	}
	zap.L().Info("productRepo.Create success", zap.Int64("id", id))
	return id, nil
}

func (r *productRepo) Update(ctx context.Context, p *biz.Product) error {
	zap.L().Debug("productRepo.Update", zap.Int64("id", p.ID))

	// 动态构建 SET 子句：只更新实际传入了值的字段
	// 约定：string 字段空值不更新，int64 < 0 表示不更新（0 是有效值），int32 < 0 表示不更新
	var setClauses []string
	var args []any

	if p.Name != "" {
		setClauses = append(setClauses, "name = ?")
		args = append(args, p.Name)
	}
	if p.Description != "" {
		setClauses = append(setClauses, "description = ?")
		args = append(args, p.Description)
	}
	if p.Price >= 0 {
		setClauses = append(setClauses, "price = ?")
		args = append(args, p.Price)
	}
	if p.Stock >= 0 {
		setClauses = append(setClauses, "stock = ?")
		args = append(args, p.Stock)
	}

	if len(setClauses) == 0 {
		zap.L().Debug("productRepo.Update: no fields to update", zap.Int64("id", p.ID))
		return nil
	}

	args = append(args, p.ID)
	query := fmt.Sprintf("UPDATE products SET %s WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')", strings.Join(setClauses, ", "))

	result, err := r.data.db.ExecContext(ctx, query, args...)
	if err != nil {
		zap.L().Error("productRepo.Update failed", zap.Int64("id", p.ID), zap.Error(err))
		return fmt.Errorf("update product failed: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		zap.L().Warn("productRepo.Update no rows affected", zap.Int64("id", p.ID))
		return biz.ErrProductNotFound
	}
	return nil
}

func (r *productRepo) Delete(ctx context.Context, id int64) error {
	zap.L().Debug("productRepo.Delete", zap.Int64("id", id))

	// 软删除：设置 deleted_at 字段，与 user 服务保持一致
	result, err := r.data.db.ExecContext(ctx,
		"UPDATE products SET deleted_at = ? WHERE id = ? AND (deleted_at IS NULL OR deleted_at = '')",
		time.Now().UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		zap.L().Error("productRepo.Delete failed", zap.Int64("id", id), zap.Error(err))
		return fmt.Errorf("delete product failed: %w", err)
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		return biz.ErrProductNotFound
	}

	zap.L().Info("productRepo.Delete success (soft)", zap.Int64("id", id))
	return nil
}
