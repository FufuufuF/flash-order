package order

import (
	"errors"
	"time"
)

// CreateInput 表示一次购买的参数。字段标签指定 JSON 中的名字。
type CreateInput struct {
	RequestID string `json:"request_id"`
	ProductID int64  `json:"product_id"`
	Quantity  int32  `json:"quantity"`
}

// Product 仅包含当前下单判断需要的信息，不是完整的商品管理模型。
type Product struct {
	ID       int64
	IsActive bool
}

type Order struct {
	ID        int64     `json:"id"`
	RequestID string    `json:"request_id"`
	ProductID int64     `json:"product_id"`
	Quantity  int32     `json:"quantity"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateResult struct {
	Order    Order `json:"order"`
	Replayed bool  `json:"replayed"`
}

// 调用方通过 errors.Is 判断错误类别；不要比较 Error() 的文字。
var (
	ErrNotImplemented     = errors.New("尚未实现：请按设计文档完成 TODO")
	ErrInvalidInput       = errors.New("请求参数无效")
	ErrOrderNotFound      = errors.New("订单不存在")
	ErrProductNotFound    = errors.New("商品不存在")
	ErrProductInactive    = errors.New("商品不可售")
	ErrInventoryMissing   = errors.New("缺少库存记录")
	ErrInsufficientStock  = errors.New("库存不足")
	ErrRequestConflict    = errors.New("请求 ID 对应的购买参数不同")
	ErrStorageUnavailable = errors.New("数据库操作不可用")
	ErrCommitUnknown      = errors.New("事务提交结果尚未确认")
	ErrInvariantViolation = errors.New("数据库修改结果不符合预期")

	// ErrRequestIDTaken 是 InsertOrder 的内部结果。
	// Service 遇到它需要先回滚、再重查；它不等于参数冲突。
	ErrRequestIDTaken = errors.New("请求 ID 已被占用")
)
