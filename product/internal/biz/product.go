package biz

import (
	"context"
	"errors"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
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
	repo   ProductRepo
	logger log.Logger
}

// NewProductUsecase 创建商品用例
func NewProductUsecase(repo ProductRepo, logger log.Logger) *ProductUsecase {
	return &ProductUsecase{repo: repo, logger: logger}
}

// Get 获取商品
func (uc *ProductUsecase) Get(ctx context.Context, id int64) (*Product, error) {
	_log := log.NewHelper(uc.logger)

	if id <= 0 {
		_log.Warnw("msg", "validation failed", "method", "Get", "reason", "invalid product id")
		return nil, errors.New("invalid product id")
	}
	return uc.repo.Get(ctx, id)
}

// List 列出商品
func (uc *ProductUsecase) List(ctx context.Context, page, pageSize int32) ([]*Product, int32, error) {
	_log := log.NewHelper(uc.logger)

	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 10
	}

	_log.Debugw("msg", "list products", "page", page, "page_size", pageSize)
	items, total, err := uc.repo.List(ctx, page, pageSize)
	if err != nil {
		_log.Errorw("msg", "list products failed", "error", err)
		return nil, 0, err
	}
	return items, total, nil
}

// Create 创建商品
func (uc *ProductUsecase) Create(ctx context.Context, p *Product) (*Product, error) {
	_log := log.NewHelper(uc.logger)

	if p.Name == "" {
		_log.Warnw("msg", "validation failed", "method", "Create", "reason", "product name is required")
		return nil, errors.New("product name is required")
	}
	if p.Price < 0 {
		_log.Warnw("msg", "validation failed", "method", "Create", "reason", "price must be non-negative")
		return nil, errors.New("price must be non-negative")
	}
	if p.Stock < 0 {
		_log.Warnw("msg", "validation failed", "method", "Create", "reason", "stock must be non-negative")
		return nil, errors.New("stock must be non-negative")
	}

	id, err := uc.repo.Create(ctx, p)
	if err != nil {
		_log.Errorw("msg", "create product failed", "error", err)
		return nil, err
	}

	p.ID = id
	_log.Infow("msg", "product created", "id", id, "name", p.Name)
	return p, nil
}

// Update 更新商品
func (uc *ProductUsecase) Update(ctx context.Context, p *Product) error {
	_log := log.NewHelper(uc.logger)

	if p.ID <= 0 {
		_log.Warnw("msg", "validation failed", "method", "Update", "reason", "invalid product id")
		return errors.New("invalid product id")
	}

	err := uc.repo.Update(ctx, p)
	if err != nil {
		_log.Errorw("msg", "update product failed", "id", p.ID, "error", err)
		return err
	}

	_log.Infow("msg", "product updated", "id", p.ID)
	return nil
}

// Delete 删除商品
func (uc *ProductUsecase) Delete(ctx context.Context, id int64) error {
	_log := log.NewHelper(uc.logger)

	if id <= 0 {
		_log.Warnw("msg", "validation failed", "method", "Delete", "reason", "invalid product id")
		return errors.New("invalid product id")
	}

	err := uc.repo.Delete(ctx, id)
	if err != nil {
		_log.Errorw("msg", "delete product failed", "id", id, "error", err)
		return err
	}

	_log.Infow("msg", "product deleted", "id", id)
	return nil
}
