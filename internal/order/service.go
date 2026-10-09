package order

import "context"

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// Get 完成按 ID 查单。业务层不接收 http.ResponseWriter。
func (s *Service) Get(ctx context.Context, id int64) (Order, error) {
	// TODO(002，小步 A)：校验正 ID，调用 store.FindByID，返回结果或错误。
	return Order{}, ErrNotImplemented
}

// Create 负责业务判断和事务边界。所有事务内查询必须传递同一个 tx。
func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	// TODO(002，小步 C)：严格按设计文档第 5 节实现。
	// 校验 → 预查已有订单 → BeginTx → 插入订单 → 可售检查
	// → 锁库存并判断 → 扣减 → Commit → 返回。
	// BeginTx 成功后立即 defer Rollback，错误路径不得返回成功。
	// 插入重复键：先显式回滚，再通过连接池重查并调用 replay。
	// Commit 错误：返回 ErrCommitUnknown，不宣称提交一定失败。
	return CreateResult{}, ErrNotImplemented
}

func validateInput(input CreateInput) error {
	// TODO(002，小步 B)：检查 request_id 的长度和字符、正商品 ID、正数量。
	return ErrNotImplemented
}

func replay(existing Order, input CreateInput) (CreateResult, error) {
	// TODO(002，小步 B)：比较 product_id、quantity。
	// 一致：返回原订单并标记 Replayed；不同：返回 ErrRequestConflict。
	return CreateResult{}, ErrNotImplemented
}
