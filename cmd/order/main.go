package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/FufuufuF/flash-order/internal/database"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("process_failed", "error", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	startupContext, cancelStartup := context.WithTimeout(signalContext, 3*time.Second)
	db, err := database.Open(startupContext, cfg)
	cancelStartup()
	if err != nil {
		return err
	}

	defer func() {
		if err := db.Close(); err != nil {
			closeErr := errors.New("关闭数据库连接池失败")
			slog.Error("database_close_failed", "error", closeErr)
			if runErr == nil {
				runErr = closeErr
			}
			return
		}
		slog.Info("database_closed")
	}()

	slog.Info("database_connected")
	<-signalContext.Done()
	slog.Info("shutdown_requested")

	return nil
}

func loadConfig() (database.Config, error) {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		host = "127.0.0.1"
	}

	port, err := loadPort()
	if err != nil {
		return database.Config{}, err
	}

	user, err := requiredEnv("MYSQL_USER")
	if err != nil {
		return database.Config{}, err
	}

	password, err := requiredEnv("MYSQL_PASSWORD")
	if err != nil {
		return database.Config{}, err
	}

	databaseName, err := requiredEnv("MYSQL_DATABASE")
	if err != nil {
		return database.Config{}, err
	}

	return database.Config{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Database: databaseName,
	}, nil
}

func loadPort() (int, error) {
	portText, ok := os.LookupEnv("MYSQL_PORT")
	if !ok || portText == "" {
		return 3306, nil
	}

	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("环境变量 MYSQL_PORT 无效")
	}

	return port, nil
}

func requiredEnv(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", fmt.Errorf("缺少环境变量 %s", name)
	}

	return value, nil
}
