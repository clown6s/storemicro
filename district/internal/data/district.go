package data

import (
	"context"
	"database/sql"
	"district/internal/biz"
)

type districtRepo struct {
	data *Data
}

func NewDistrictRepo(data *Data) biz.DistrictRepo {
	return &districtRepo{data: data}
}

func (d districtRepo) Get(ctx context.Context, adCode string) ([]*biz.District, error) {
	query := `
				WITH RECURSIVE district_tree AS (
					SELECT id, parent_adcode, adcode, name, level, center
					FROM district
					WHERE adcode = ?
				
					UNION ALL
				
					SELECT d.id, d.parent_adcode, d.adcode, d.name, d.level, d.center
					FROM district d
					INNER JOIN district_tree dt
						ON d.parent_adcode = dt.adcode
				)
				SELECT id, parent_adcode, adcode, name, level, center
				FROM district_tree
      `

	var args []any

	if adCode != "" {
		args = append(args, adCode)
	} else {
		query = `
	SELECT id, parent_adcode, adcode, name, level, center
	FROM district
	`
	}

	rows, err := d.data.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		err := rows.Close()
		if err != nil {
			return
		}
	}(rows)

	var districts []*biz.District

	for rows.Next() {
		d := &biz.District{}

		if err := rows.Scan(
			&d.ID,
			&d.ParentAdCode,
			&d.AdCode,
			&d.Name,
			&d.Level,
			&d.Center,
		); err != nil {
			return nil, err
		}

		districts = append(districts, d)
	}

	return districts, rows.Err()
}

func (d districtRepo) List(ctx context.Context, adCode string) ([]*biz.District, error) {
	//TODO implement me
	panic("implement me")
}
