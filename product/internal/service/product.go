package service

import (
	"context"

	productv1 "product/api/product/v1"
	"product/internal/biz"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProviderSet is service providers.
var ProviderSet = wire.NewSet(NewProductService)

type ProductService struct {
	productv1.UnimplementedProductServer
	uc     *biz.ProductUsecase
	logger log.Logger
}

// NewProductService 创建商品 gRPC 服务
func NewProductService(uc *biz.ProductUsecase, logger log.Logger) *ProductService {
	return &ProductService{uc: uc, logger: logger}
}

func (s *ProductService) GetProduct(ctx context.Context, req *productv1.GetProductReq) (*productv1.GetProductResp, error) {
	_log := log.NewHelper(s.logger)
	_log.Infow("msg", "GetProduct called", "id", req.Id)

	p, err := s.uc.Get(ctx, req.Id)
	if err != nil {
		_log.Warnw("msg", "GetProduct failed", "id", req.Id, "error", err)
		return nil, s.mapError(err)
	}
	return toProductResp(p), nil
}

func (s *ProductService) ListProducts(ctx context.Context, req *productv1.ListProductsReq) (*productv1.ListProductsResp, error) {
	_log := log.NewHelper(s.logger)
	_log.Infow("msg", "ListProducts called", "page", req.Page, "page_size", req.PageSize)

	items, total, err := s.uc.List(ctx, req.Page, req.PageSize)
	if err != nil {
		_log.Warnw("msg", "ListProducts failed", "error", err)
		return nil, s.mapError(err)
	}
	resps := make([]*productv1.GetProductResp, 0, len(items))
	for _, p := range items {
		resps = append(resps, toProductResp(p))
	}
	return &productv1.ListProductsResp{
		Items: resps,
		Total: total,
	}, nil
}

func toProductResp(p *biz.Product) *productv1.GetProductResp {
	return &productv1.GetProductResp{
		Id:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Price:       p.Price,
		Stock:       p.Stock,
	}
}

func (s *ProductService) mapError(err error) error {
	if err == nil {
		return nil
	}
	switch err.Error() {
	case "product not found":
		return status.Error(codes.NotFound, err.Error())
	case "invalid product id", "product name is required", "price must be non-negative", "stock must be non-negative":
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
