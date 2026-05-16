package data

import (
	"context"
	"database/sql"
	"fmt"

	"district/internal/biz"

	"go.uber.org/zap"
)

type districtRepo struct {
	data *Data
}

func NewDistrictRepo(data *Data) biz.DistrictRepo {
	return &districtRepo{data: data}
}

func (d districtRepo) Get(ctx context.Context, adCode string) ([]*biz.District, error) {
	var (
		query string
		args  []any
	)

	if adCode != "" {
		// [M1] 修复：CTE 分两步
		// 1. 向上回溯找到根节点（省级）
		// 2. 从根节点向下遍历所有子节点
		// 显式限制递归深度防止循环引用数据导致资源耗尽
		query = `
WITH RECURSIVE ancestors AS (
    -- 向上回溯：从给定节点找到省级根节点（排除 country 层级）
    SELECT id, parent_adcode, adcode, name, level, center, 1 AS depth
    FROM district
    WHERE adcode = ? AND level != 'country'
    UNION ALL
    SELECT d.id, d.parent_adcode, d.adcode, d.name, d.level, d.center, a.depth + 1
    FROM district d
    INNER JOIN ancestors a ON d.adcode = a.parent_adcode
    WHERE a.level != 'country' AND a.depth < 20
),
root AS (
    -- 取到最顶层的省级行政区
    SELECT id, parent_adcode, adcode, name, level, center
    FROM ancestors
    WHERE parent_adcode = '100000' OR level = 'province'
    LIMIT 1
),
district_tree AS (
    -- 从根节点向下遍历所有子节点（限制深度防止循环引用）
    SELECT r.id, r.parent_adcode, r.adcode, r.name, r.level, r.center, 1 AS depth
    FROM root r
    UNION ALL
    SELECT d.id, d.parent_adcode, d.adcode, d.name, d.level, d.center, dt.depth + 1
    FROM district d
    INNER JOIN district_tree dt ON d.parent_adcode = dt.adcode
    WHERE dt.depth < 20
)
SELECT id, parent_adcode, adcode, name, level, center FROM district_tree`
		args = append(args, adCode)
	} else {
		query = `SELECT id, parent_adcode, adcode, name, level, center FROM district WHERE level != 'country' ORDER BY adcode LIMIT 10000`
	}

	rows, err := d.data.db.QueryContext(ctx, query, args...)
	if err != nil {
		zap.L().Error("districtRepo.Get query failed", zap.String("adCode", adCode), zap.Error(err))
		return nil, fmt.Errorf("query district failed: %w", err)
	}
	defer rows.Close()

	return scanDistricts(rows)
}

// scanDistricts 扫描行政区查询结果
func scanDistricts(rows *sql.Rows) ([]*biz.District, error) {
	var districts []*biz.District
	for rows.Next() {
		item := &biz.District{}
		if err := rows.Scan(
			&item.ID,
			&item.ParentAdCode,
			&item.AdCode,
			&item.Name,
			&item.Level,
			&item.Center,
		); err != nil {
			zap.L().Error("districtRepo scan failed", zap.Error(err))
			return nil, fmt.Errorf("scan district failed: %w", err)
		}
		districts = append(districts, item)
	}
	if err := rows.Err(); err != nil {
		zap.L().Error("districtRepo rows error", zap.Error(err))
		return nil, fmt.Errorf("district rows error: %w", err)
	}
	if districts == nil {
		districts = []*biz.District{}
	}
	return districts, nil
}
