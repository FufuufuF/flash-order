package order

import (
	"context"
	"database/sql"
	"time"
)

// Store 持有连接池。连接池不是“一个数据库连接”，也不是一个事务。
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) FindByID(ctx context.Context, id int64) (Order, error) {
	// TODO(002，小步 A)：通过 s.db.QueryRowContext 查询并 Scan。
	// 显式选择订单字段，不使用 SELECT *；参数使用 ? 占位符。
	// sql.ErrNoRows → ErrOrderNotFound；其他错误不能伪装成查不到。
	return Order{}, ErrNotImplemented
}

func (s *Store) FindByRequestID(ctx context.Context, requestID string) (Order, error) {
	// TODO(002，小步 B)：通过连接池查已提交订单；沿用同一字段和错误规则。
	return Order{}, ErrNotImplemented
}

func (s *Store) BeginTx(ctx context.Context) (*sql.Tx, error) {
	// TODO(002，小步 C)：使用 s.db.BeginTx，不手写 BEGIN SQL。
	return nil, ErrNotImplemented
}

func (s *Store) InsertOrder(ctx context.Context, tx *sql.Tx, input CreateInput, createdAt time.Time) (Order, error) {
	// TODO(002，小步 B/C)：通过 tx.ExecContext 插入，处理 LastInsertId。
	// 识别 request_id 唯一键的 1062 → ErrRequestIDTaken；其他错误保留分类。
	// 返回值不代表已提交，提交边界由 Service 管理。
	return Order{}, ErrNotImplemented
}

func (s *Store) ReadProduct(ctx context.Context, tx *sql.Tx, productID int64) (Product, error) {
	// TODO(002，小步 C)：普通 SELECT，不加 FOR UPDATE。
	// 无记录 → ErrProductNotFound；是否可售交给 Service 判断。
	return Product{}, ErrNotImplemented
}

func (s *Store) LockInventory(ctx context.Context, tx *sql.Tx, productID int64) (int32, error) {
	// TODO(002，小步 C)：按主键 SELECT available ... FOR UPDATE。
	// 无记录 → ErrInventoryMissing；数量是否足够由 Service 判断。
	return 0, ErrNotImplemented
}

func (s *Store) DeductInventory(ctx context.Context, tx *sql.Tx, productID int64, quantity int32) error {
	// TODO(002，小步 C)：通过 tx.ExecContext 扣减，必须检查 RowsAffected。
	// 影响行数不为 1 → ErrInvariantViolation；不要在这里 Commit。
	return ErrNotImplemented
}
