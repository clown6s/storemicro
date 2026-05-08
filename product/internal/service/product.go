package service

import (
	"context"
	"errors"
	"strconv"

	productv1 "product/api/product/v1"
	"product/internal/biz"

	"github.com/google/wire"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProviderSet is service providers.
var ProviderSet = wire.NewSet(NewProductService)

type ProductService struct {
	productv1.UnimplementedProductServer
	uc *biz.ProductUsecase
}

// NewProductService 创建商品 gRPC 服务
func NewProductService(uc *biz.ProductUsecase) *ProductService {
	return &ProductService{uc: uc}
}

func (s *ProductService) GetProduct(ctx context.Context, req *productv1.GetProductReq) (*productv1.GetProductResp, error) {
	zap.L().Info("GetProduct called", zap.String("id", req.Id))

	id, err := strconv.ParseInt(req.Id, 10, 64)
	if err != nil {
		zap.L().Warn("invalid product id", zap.String("id", req.Id), zap.Error(err))
		return nil, status.Errorf(codes.InvalidArgument, "invalid product id: %s", req.Id)
	}

	p, err := s.uc.Get(ctx, id)
	if err != nil {
		zap.L().Warn("GetProduct failed", zap.Int64("id", id), zap.Error(err))
		return nil, s.mapError(err)
	}
	return toProductResp(p), nil
}

func (s *ProductService) ListProducts(ctx context.Context, req *productv1.ListProductsReq) (*productv1.ListProductsResp, error) {
	zap.L().Info("ListProducts called", zap.Int32("page", req.Page), zap.Int32("page_size", req.PageSize))

	items, total, err := s.uc.List(ctx, req.Page, req.PageSize)
	if err != nil {
		zap.L().Warn("ListProducts failed", zap.Error(err))
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

func (s *ProductService) Ping(ctx context.Context, req *productv1.PingReq) (*productv1.PingResp, error) {
	return &productv1.PingResp{Message: "pong"}, nil
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
	if errors.Is(err, biz.ErrProductNotFound) {
		return status.Error(codes.NotFound, "product not found")
	}
	zap.L().Error("unexpected product error", zap.Error(err))
	return status.Error(codes.Internal, "internal error")
}
