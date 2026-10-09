package httpapi

import (
	"net/http"

	"github.com/FufuufuF/flash-order/internal/order"
)

type OrderHandler struct {
	service *order.Service
}

func NewOrderHandler(service *order.Service) *OrderHandler {
	return &OrderHandler{service: service}
}

func (h *OrderHandler) Get(w http.ResponseWriter, r *http.Request) {
	// TODO(002，小步 A)：r.PathValue("id") → strconv.ParseInt → 校验。
	// 从 r.Context() 派生 5 秒 context，defer cancel，再调用 service.Get。
	// 成功输出 JSON；失败按照文档映射。不要把原始 SQL 错误发给客户端。
	http.Error(w, "尚未实现：GET /orders/{id}", http.StatusNotImplemented)
}

func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	// TODO(002，小步 D)：检查 Content-Type、限制请求体、严格解码一个 JSON。
	// 从 r.Context() 派生 5 秒 context，调用 service.Create。
	// 新建返回 201，重放返回 200；错误正文采用文档约定的 JSON 结构。
	http.Error(w, "尚未实现：POST /orders", http.StatusNotImplemented)
}
