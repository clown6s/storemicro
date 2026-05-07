package biz

import (
	"context"
	"errors"

	"github.com/google/wire"
	"go.uber.org/zap"
)

// ProviderSet is biz providers.
var ProviderSet = wire.NewSet(NewProductUsecase)

// Product 领域模型
type Product struct {
	ID          int64
	Name        string
	Description string
	Price       int64 // 分
	Stock       int32
}

// ProductRepo 数据仓库接口
type ProductRepo interface {
	Get(ctx context.Context, id int64) (*Product, error)
	List(ctx context.Context, page, pageSize int32) ([]*Product, int32, error)
	Create(ctx context.Context, p *Product) (int64, error)
	Update(ctx context.Context, p *Product) error
	Delete(ctx context.Context, id int64) error
}

// ProductUsecase 商品用例
type ProductUsecase struct {
	repo ProductRepo
}

// NewProductUsecase 创建商品用例
func NewProductUsecase(repo ProductRepo) *ProductUsecase {
	return &ProductUsecase{repo: repo}
}

// Get 获取商品
func (uc *ProductUsecase) Get(ctx context.Context, id int64) (*Product, error) {
	if id <= 0 {
		zap.L().Warn("validation failed", zap.String("method", "Get"), zap.String("reason", "invalid product id"))
		return nil, errors.New("invalid product id")
	}
	return uc.repo.Get(ctx, id)
}

// List 列出商品
func (uc *ProductUsecase) List(ctx context.Context, page, pageSize int32) ([]*Product, int32, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 10
	}

	zap.L().Debug("list products", zap.Int32("page", page), zap.Int32("page_size", pageSize))
	items, total, err := uc.repo.List(ctx, page, pageSize)
	if err != nil {
		zap.L().Error("list products failed", zap.Error(err))
		return nil, 0, err
	}
	return items, total, nil
}

// Create 创建商品
func (uc *ProductUsecase) Create(ctx context.Context, p *Product) (*Product, error) {
	if p.Name == "" {
		zap.L().Warn("validation failed", zap.String("method", "Create"), zap.String("reason", "product name is required"))
		return nil, errors.New("product name is required")
	}
	if p.Price < 0 {
		zap.L().Warn("validation failed", zap.String("method", "Create"), zap.String("reason", "price must be non-negative"))
		return nil, errors.New("price must be non-negative")
	}
	if p.Stock < 0 {
		zap.L().Warn("validation failed", zap.String("method", "Create"), zap.String("reason", "stock must be non-negative"))
		return nil, errors.New("stock must be non-negative")
	}

	id, err := uc.repo.Create(ctx, p)
	if err != nil {
		zap.L().Error("create product failed", zap.Error(err))
		return nil, err
	}

	p.ID = id
	zap.L().Info("product created", zap.Int64("id", id), zap.String("name", p.Name))
	return p, nil
}

// Update 更新商品
func (uc *ProductUsecase) Update(ctx context.Context, p *Product) error {
	if p.ID <= 0 {
		zap.L().Warn("validation failed", zap.String("method", "Update"), zap.String("reason", "invalid product id"))
		return errors.New("invalid product id")
	}

	err := uc.repo.Update(ctx, p)
	if err != nil {
		zap.L().Error("update product failed", zap.Int64("id", p.ID), zap.Error(err))
		return err
	}

	zap.L().Info("product updated", zap.Int64("id", p.ID))
	return nil
}

// Delete 删除商品
func (uc *ProductUsecase) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		zap.L().Warn("validation failed", zap.String("method", "Delete"), zap.String("reason", "invalid product id"))
		return errors.New("invalid product id")
	}

	err := uc.repo.Delete(ctx, id)
	if err != nil {
		zap.L().Error("delete product failed", zap.Int64("id", id), zap.Error(err))
		return err
	}

	zap.L().Info("product deleted", zap.Int64("id", id))
	return nil
}
