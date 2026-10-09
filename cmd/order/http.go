package main

import (
	"context"
	"database/sql"

	"github.com/FufuufuF/flash-order/internal/order"
)

// runHTTP 负责组装对象和管理 HTTP 生命周期，不在这里编写下单业务。
func runHTTP(signalContext context.Context, db *sql.DB) error {
	// TODO(002，小步 A)：
	// 1. order.NewStore(db) → order.NewService(store)。
	// 2. httpapi.NewOrderHandler(service) → httpapi.NewRouter(handler)。
	// 3. 读取 HTTP_ADDR（默认 127.0.0.1:8080），构造 http.Server。
	// 4. 设置设计文档中的超时，启动监听，把监听错误传回主流程。
	// 5. 信号到达后使用独立的 5 秒 context 调用 Shutdown。
	// 6. 超时则 Close 并取消在途操作；退出后 main 才关闭数据库。
	// signalContext 用于观察退出，不直接作为请求的 BaseContext。
	return order.ErrNotImplemented
}
