package order

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// Get 完成按 ID 查单。业务层不接收 http.ResponseWriter。
func (s *Service) Get(ctx context.Context, id int64) (Order, error) {
	// 校验正 ID，调用 store.FindByID，返回结果或错误。
	if id <= 0 {
		return Order{}, ErrInvalidInput
	}

	result, err := s.store.FindByID(ctx, id)

	if err != nil {
		return Order{}, err
	}

	return result, nil
}

// Create 负责业务判断和事务边界。所有事务内查询必须传递同一个 tx。
func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	// 严格按设计文档第 5 节实现。
	// 校验 → 预查已有订单 → BeginTx → 插入订单 → 可售检查
	// → 锁库存并判断 → 扣减 → Commit → 返回。
	// BeginTx 成功后立即 defer Rollback，错误路径不得返回成功。
	// 插入重复键：先显式回滚，再通过连接池重查并调用 replay。
	// Commit 错误：返回 ErrCommitUnknown，不宣称提交一定失败。

	if err := validateInput(input); err != nil {
		return CreateResult{}, err
	}

	existing, err := s.store.FindByRequestID(ctx, input.RequestID)
	if err == nil {
		return replay(existing, input)
	}
	if !errors.Is(err, ErrOrderNotFound) {
		return CreateResult{}, err
	}

	tx, err := s.store.BeginTx(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	defer tx.Rollback()

	// 先占住 request_id；后续检查失败时，插入也随事务一起回滚。
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	created, err := s.store.InsertOrder(ctx, tx, input, createdAt)
	if err != nil {
		if !errors.Is(err, ErrRequestIDTaken) {
			return CreateResult{}, err
		}

		// 插入时出现重复键
		// 必须先结束事务，释放其连接，再通过连接池读取赢家的订单。
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			slog.ErrorContext(ctx, "回滚重复请求事务失败",
				"request_id", input.RequestID,
				"stage", "rollback_duplicate",
				"error_code", "storage_unavailable")
			return CreateResult{}, fmt.Errorf("%w: 回滚重复请求事务: %w", ErrStorageUnavailable, rollbackErr)
		}
		existing, err := s.store.FindByRequestID(ctx, input.RequestID)
		if errors.Is(err, ErrOrderNotFound) {
			return CreateResult{}, fmt.Errorf("%w: 重复键后未找到订单: %w", ErrInvariantViolation, err)
		}
		if err != nil {
			return CreateResult{}, err
		}
		return replay(existing, input)
	}

	product, err := s.store.ReadProduct(ctx, tx, input.ProductID)
	if err != nil {
		return CreateResult{}, err
	}
	if !product.IsActive {
		return CreateResult{}, ErrProductInactive
	}

	available, err := s.store.LockInventory(ctx, tx, input.ProductID)
	if err != nil {
		return CreateResult{}, err
	}
	if available < input.Quantity {
		return CreateResult{}, ErrInsufficientStock
	}

	if err := s.store.DeductInventory(ctx, tx, input.ProductID, input.Quantity); err != nil {
		return CreateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CreateResult{}, fmt.Errorf("%w: %w", ErrCommitUnknown, err)
	}

	return CreateResult{Order: created, Replayed: false}, nil
}

func validateInput(input CreateInput) error {
	// 检查 request_id 的长度和字符、正商品 ID、正数量。
	if input.Quantity > 0 && input.ProductID > 0 && validRequestID(input.RequestID) {
		return nil
	}
	return ErrInvalidInput
}

func validRequestID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}

	for i := 0; i < len(id); i++ {
		c := id[i]

		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' ||
			c == '_') {
			return false
		}
	}

	return true
}

func replay(existing Order, input CreateInput) (CreateResult, error) {
	// 比较 product_id、quantity。
	// 一致：返回原订单并标记 Replayed；不同：返回 ErrRequestConflict。
	if existing.Quantity == input.Quantity && existing.ProductID == input.ProductID {
		return CreateResult{
			Order:    existing,
			Replayed: true,
		}, nil
	}
	return CreateResult{}, ErrRequestConflict
}
