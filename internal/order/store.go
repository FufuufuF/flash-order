package order

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Store 持有连接池。连接池不是“一个数据库连接”，也不是一个事务。
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) FindByID(ctx context.Context, id int64) (Order, error) {
	// 通过 s.db.QueryRowContext 查询并 Scan。
	// 显式选择订单字段，不使用 SELECT *；参数使用 ? 占位符。
	// sql.ErrNoRows → ErrOrderNotFound；其他错误不能伪装成查不到。

	query := `
		SELECT id, request_id, product_id, quantity, created_at
		FROM orders
		WHERE id = ?
	`

	row := s.db.QueryRowContext(ctx, query, id)

	var result Order
	err := row.Scan(
		&result.ID,
		&result.RequestID,
		&result.ProductID,
		&result.Quantity,
		&result.CreatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Order{}, ErrOrderNotFound
		}

		return Order{}, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	return result, nil
}

func (s *Store) FindByRequestID(ctx context.Context, requestID string) (Order, error) {
	// 通过连接池查已提交订单；沿用同一字段和错误规则。
	query := `
		SELECT id, request_id, product_id, quantity, created_at
		FROM orders
		WHERE request_id = ?
	`

	row := s.db.QueryRowContext(ctx, query, requestID)

	var result Order
	err := row.Scan(
		&result.ID,
		&result.RequestID,
		&result.ProductID,
		&result.Quantity,
		&result.CreatedAt,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Order{}, ErrOrderNotFound
		}

		return Order{}, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	return result, nil
}

func (s *Store) BeginTx(ctx context.Context) (*sql.Tx, error) {
	// 使用 s.db.BeginTx，不手写 BEGIN SQL。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	return tx, nil
}

func (s *Store) InsertOrder(ctx context.Context, tx *sql.Tx, input CreateInput, createdAt time.Time) (Order, error) {
	// 通过 tx.ExecContext 插入，处理 LastInsertId。
	// 识别 request_id 唯一键的 1062 → ErrRequestIDTaken；其他错误保留分类。
	// 返回值不代表已提交，提交边界由 Service 管理。
	insertQuery := `
		INSERT INTO orders (request_id, product_id, quantity, created_at)
		VALUES (?, ?, ?, ?)
	`

	insertResult, err := tx.ExecContext(ctx, insertQuery,
		input.RequestID,
		input.ProductID,
		input.Quantity,
		createdAt,
	)

	if err != nil {
		// 1062 也可能来自其他唯一键；只识别消息末尾的 request_id 索引。
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 &&
			(strings.HasSuffix(mysqlErr.Message, " for key 'orders.uq_orders_request_id'") ||
				strings.HasSuffix(mysqlErr.Message, " for key 'uq_orders_request_id'")) {
			return Order{}, fmt.Errorf("%w: %w", ErrRequestIDTaken, err)
		}
		return Order{}, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	// 从 insertResult 中提取 Order 并返回
	id, err := insertResult.LastInsertId()
	if err != nil {
		return Order{}, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	return Order{
		ID:        id,
		RequestID: input.RequestID,
		ProductID: input.ProductID,
		Quantity:  input.Quantity,
		CreatedAt: createdAt,
	}, nil
}

func (s *Store) ReadProduct(ctx context.Context, tx *sql.Tx, productID int64) (Product, error) {
	// 普通 SELECT，不加 FOR UPDATE。
	// 无记录 → ErrProductNotFound；是否可售交给 Service 判断。

	query := `
		SELECT id, is_active
		FROM products
		WHERE id = ?
	`

	row := tx.QueryRowContext(ctx, query, productID)

	var result Product
	err := row.Scan(
		&result.ID,
		&result.IsActive,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Product{}, ErrProductNotFound
		}

		return Product{}, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	return result, nil
}

func (s *Store) LockInventory(ctx context.Context, tx *sql.Tx, productID int64) (int32, error) {
	// 按主键 SELECT available ... FOR UPDATE。
	// 无记录 → ErrInventoryMissing；数量是否足够由 Service 判断。
	query := `
		SELECT available
		FROM inventory
		WHERE product_id = ?
		FOR UPDATE
	`

	row := tx.QueryRowContext(ctx, query, productID)

	var result int32
	err := row.Scan(&result)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrInventoryMissing
		}
		return 0, fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	return result, nil
}

func (s *Store) DeductInventory(ctx context.Context, tx *sql.Tx, productID int64, quantity int32) error {
	// 通过 tx.ExecContext 扣减，必须检查 RowsAffected。
	// 影响行数不为 1 → ErrInvariantViolation；不要在这里 Commit。
	query := `
		UPDATE inventory
		SET available = available - ?
		WHERE product_id = ?
	`

	result, err := tx.ExecContext(ctx, query, quantity, productID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("%w: 扣减库存影响行数为 %d", ErrInvariantViolation, rowsAffected)
	}

	return nil
}
