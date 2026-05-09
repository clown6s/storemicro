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
		query = `
WITH RECURSIVE district_tree AS (
    SELECT id, parent_adcode, adcode, name, level, center
    FROM district
    WHERE adcode = ?
    UNION ALL
    SELECT d.id, d.parent_adcode, d.adcode, d.name, d.level, d.center
    FROM district d
    INNER JOIN district_tree dt ON d.parent_adcode = dt.adcode
)
SELECT id, parent_adcode, adcode, name, level, center FROM district_tree`
		args = append(args, adCode)
	} else {
		query = `SELECT id, parent_adcode, adcode, name, level, center FROM district WHERE level != 'country'`
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
	return districts, nil
}
