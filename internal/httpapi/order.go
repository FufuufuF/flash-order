package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/FufuufuF/flash-order/internal/order"
)

type OrderHandler struct {
	service *order.Service
}

const maxOrderRequestBody = 4 << 10

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

	writeOrderJSON(w, http.StatusOK, result)
}

func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	// 检查 Content-Type、限制请求体、严格解码一个 JSON。
	// 从 r.Context() 派生 5 秒 context，调用 service.Create。
	// 新建返回 201，重放返回 200；错误正文采用文档约定的 JSON 结构。

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeHTTPError(
			w,
			http.StatusUnsupportedMediaType,
			"unsupported_media_type",
			"请求必须使用 application/json",
		)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxOrderRequestBody)
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var input order.CreateInput
	if err := decoder.Decode(&input); err != nil {
		writeDecodeError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	result, err := h.service.Create(ctx, input)
	if err != nil {
		writeOrderError(w, err)
		return
	}

	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeOrderJSON(w, status, result)
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
	case errors.Is(err, order.ErrProductNotFound):
		status = http.StatusNotFound
		code = "product_not_found"
		message = "商品不存在"
	case errors.Is(err, order.ErrProductInactive):
		status = http.StatusConflict
		code = "product_inactive"
		message = "商品不可售"
	case errors.Is(err, order.ErrInsufficientStock):
		status = http.StatusConflict
		code = "insufficient_stock"
		message = "库存不足"
	case errors.Is(err, order.ErrRequestConflict):
		status = http.StatusConflict
		code = "request_conflict"
		message = "请求 ID 对应的购买参数不同"
	case errors.Is(err, order.ErrInventoryMissing), errors.Is(err, order.ErrInvariantViolation):
		status = http.StatusInternalServerError
		code = "internal_error"
		message = "服务器内部错误"
	case errors.Is(err, order.ErrCommitUnknown):
		status = http.StatusServiceUnavailable
		code = "commit_unknown"
		message = "订单提交结果尚未确认，请使用原 request_id 重试"
	case errors.Is(err, order.ErrStorageUnavailable):
		status = http.StatusServiceUnavailable
		code = "storage_unavailable"
		message = "暂时无法处理订单"
	}

	writeOrderJSON(w, status, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func writeHTTPError(w http.ResponseWriter, status int, code, message string) {
	writeOrderJSON(w, status, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeHTTPError(w, http.StatusRequestEntityTooLarge,
			"body_too_large", "请求体过大")
		return
	}

	writeHTTPError(w, http.StatusBadRequest, "invalid_input", "请求参数无效")
}

func writeOrderJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
