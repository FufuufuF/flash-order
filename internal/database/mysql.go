package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Config contains the connection settings supplied by the application.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

type openError struct {
	stage  string
	reason string
	cause  error
}

func (e *openError) Error() string {
	if e.reason == "" {
		return e.stage
	}
	return fmt.Sprintf("%s（%s）", e.stage, e.reason)
}

func (e *openError) Unwrap() error {
	return e.cause
}

// Open creates a MySQL connection pool and verifies that it can reach the database.
// It does not read environment variables, run migrations, or execute business SQL.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	driverConfig := mysql.NewConfig()
	driverConfig.User = cfg.User
	driverConfig.Passwd = cfg.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	driverConfig.DBName = cfg.Database
	driverConfig.ParseTime = true
	driverConfig.Loc = time.UTC
	driverConfig.Timeout = 3 * time.Second
	driverConfig.ReadTimeout = 5 * time.Second
	driverConfig.WriteTimeout = 5 * time.Second

	db, err := sql.Open("mysql", driverConfig.FormatDSN())
	if err != nil {
		return nil, &openError{stage: "创建数据库连接池失败", reason: "配置无效", cause: err}
	}

	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(5)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, &openError{
			stage:  "检查数据库连接失败",
			reason: classifyConnectionError(err),
			cause:  err,
		}
	}

	return db, nil
}

func classifyConnectionError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "超时"
	case errors.Is(err, context.Canceled):
		return "已取消"
	}

	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "超时"
	}

	return "数据库不可用"
}
