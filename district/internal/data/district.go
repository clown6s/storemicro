package data

import (
	"context"
	"district/internal/biz"
)

type districtRepo struct {
	data *Data
}

func (d *districtRepo) GetTree(ctx context.Context, adCode string) ([]*biz.District, error) {
	// 1. 查询所有行政区
	rows, err := d.data.db.QueryContext(ctx,
		"SELECT id, parent_adcode, adcode, name, level, center FROM district")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 2. 构建所有行政区的 map
	allDistricts := make(map[string]*biz.District)
	for rows.Next() {
		district := &biz.District{}
		err := rows.Scan(&district.ID, &district.ParentAdCode, &district.AdCode, &district.Name, &district.Level, &district.Center)
		if err != nil {
			return nil, err
		}
		allDistricts[district.AdCode] = district
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	// 3. 构建父子关系
	for _, district := range allDistricts {
		if district.ParentAdCode == "" {
			continue // 根节点，后面单独处理
		}
		if parent, ok := allDistricts[district.ParentAdCode]; ok {
			parent.Children = append(parent.Children, district)
		}
	}

	// 4. 根据 adCode 返回对应的树
	if adCode != "" {
		if district, ok := allDistricts[adCode]; ok {
			return []*biz.District{district}, nil
		}
		return nil, nil // adCode 不存在
	}

	// 返回所有根节点
	var roots []*biz.District
	for _, district := range allDistricts {
		if district.ParentAdCode == "" {
			roots = append(roots, district)
		}
	}
	return roots, nil
}

func NewDistrictRepo(data *Data) biz.DistrictRepo {
	return &districtRepo{data: data}
}
