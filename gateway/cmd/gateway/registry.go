package main

import (
	productv1 "gateway/api/product/v1"
	userv1 "gateway/api/user/v1"
)

// services 服务注册表 —— 新增服务只需在此添加一行即可
var services = []ServiceRegistration{
	{
		Name:            "user",
		RegisterHandler: userv1.RegisterUserHandler,
	},
	{
		Name:            "product",
		RegisterHandler: productv1.RegisterProductHandler,
	},
}
