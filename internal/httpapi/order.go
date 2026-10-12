package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/FufuufuF/flash-order/internal/order"
)

type OrderHandler struct {
	service *order.Service
}

func NewOrderHandler(service *order.Service) *OrderHandler {
	return &OrderHandler{service: service}
}

func (h *OrderHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeOrderError(w, order.ErrInvalidInput)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	result, err := h.service.Get(ctx, id)
	if err != nil {
		writeOrderError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	// TODO(002，小步 D)：检查 Content-Type、限制请求体、严格解码一个 JSON。
	// 从 r.Context() 派生 5 秒 context，调用 service.Create。
	// 新建返回 201，重放返回 200；错误正文采用文档约定的 JSON 结构。
	http.Error(w, "尚未实现：POST /orders", http.StatusNotImplemented)
}

type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeOrderError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "服务器内部错误"

	switch {
	case errors.Is(err, order.ErrInvalidInput):
		status = http.StatusBadRequest
		code = "invalid_input"
		message = "请求参数无效"
	case errors.Is(err, order.ErrOrderNotFound):
		status = http.StatusNotFound
		code = "order_not_found"
		message = "订单不存在"
	case errors.Is(err, order.ErrStorageUnavailable):
		status = http.StatusServiceUnavailable
		code = "storage_unavailable"
		message = "暂时无法查询订单"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}
