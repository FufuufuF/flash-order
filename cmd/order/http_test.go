package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRunHTTPServerPortConflict(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &http.Server{Addr: listener.Addr().String()}
	result := startTestHTTPServer(t, ctx, server)

	err = waitTestServerResult(t, result, 2*time.Second)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("端口占用应该返回启动错误，实际为 %v", err)
	}
	var netErr *net.OpError
	if !errors.As(err, &netErr) {
		t.Fatalf("应该保留原始网络错误，实际为 %v", err)
	}
}

func TestRunHTTPServerGracefulShutdown(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	releaseContext, release := context.WithCancel(context.Background())
	defer release()

	server := &http.Server{
		Addr: testHTTPAddress(t),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ready" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			close(started)
			select {
			case <-releaseContext.Done():
				w.WriteHeader(http.StatusOK)
			case <-r.Context().Done():
				close(canceled)
			}
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := startTestHTTPServer(t, ctx, server)
	client := testHTTPClient(t)
	waitTestHTTPState(t, client, server.Addr, true)
	response := startTestRequest(client, "http://"+server.Addr+"/work")
	waitTestNotification(t, started)

	cancel()
	waitTestHTTPState(t, client, server.Addr, false)
	select {
	case err := <-result:
		t.Fatalf("在途请求尚未完成，服务却已返回: %v", err)
	case <-canceled:
		t.Fatal("优雅退出不应该立即取消在途请求")
	default:
	}

	release()
	if err := waitTestServerResult(t, result, 2*time.Second); err != nil {
		t.Fatalf("优雅退出应该成功，实际为 %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("正常完成的请求不应该进入取消分支")
	default:
	}
	select {
	case got := <-response:
		if got.err != nil || got.status != http.StatusOK {
			t.Fatalf("在途请求应该收到 200，实际为 %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到在途请求的响应")
	}
}

func TestRunHTTPServerForcedShutdown(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	releaseContext, release := context.WithCancel(context.Background())
	defer release()
	server := &http.Server{
		Addr: testHTTPAddress(t),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ready" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			close(started)
			<-r.Context().Done()
			close(canceled)
			// 暂停 handler 返回，观察 Close 本身是否结束客户端连接。
			<-releaseContext.Done()
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := startTestHTTPServer(t, ctx, server)
	client := testHTTPClient(t)
	waitTestHTTPState(t, client, server.Addr, true)
	response := startTestRequest(client, "http://"+server.Addr+"/work")
	waitTestNotification(t, started)

	cancel()
	waitTestHTTPState(t, client, server.Addr, false)
	select {
	case <-canceled:
		t.Fatal("5 秒关闭预算到期前不应该取消请求")
	default:
	}

	err := waitTestServerResult(t, result, 8*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应该保留关闭超时错误，实际为 %v", err)
	}
	waitTestNotification(t, canceled)
	select {
	case got := <-response:
		if got.err == nil {
			t.Fatal("超时请求的连接应该被强制关闭")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("强制关闭后客户端仍未结束")
	}
}

func TestRunHTTPServerAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &http.Server{Addr: "127.0.0.1:0"}
	result := startTestHTTPServer(t, ctx, server)
	if err := waitTestServerResult(t, result, 2*time.Second); err != nil {
		t.Fatalf("启动前取消也应该正常退出，实际为 %v", err)
	}
}

func startTestHTTPServer(t *testing.T, ctx context.Context, server *http.Server) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		result <- runHTTPServer(ctx, server)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("测试结束后服务任务仍未退出")
		}
	})
	return result
}

func testHTTPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func testHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func waitTestHTTPState(t *testing.T, client *http.Client, addr string, wantReady bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/ready", nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		response, err := client.Do(request)
		ready := err == nil
		if response != nil {
			_ = response.Body.Close()
		}
		cancel()
		if ready == wantReady {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("服务未进入预期监听状态，ready=%t", wantReady)
}

type testHTTPResponse struct {
	status int
	err    error
}

func startTestRequest(client *http.Client, url string) <-chan testHTTPResponse {
	result := make(chan testHTTPResponse, 1)
	go func() {
		response, err := client.Get(url)
		if err != nil {
			result <- testHTTPResponse{err: err}
			return
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		result <- testHTTPResponse{status: response.StatusCode, err: errors.Join(readErr, closeErr)}
	}()
	return result
}

func waitTestServerResult(t *testing.T, result <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(timeout):
		t.Fatal("服务没有在预算内退出")
		return nil
	}
}

func waitTestNotification(t *testing.T, notification <-chan struct{}) {
	t.Helper()
	select {
	case <-notification:
	case <-time.After(2 * time.Second):
		t.Fatal("未收到预期的请求状态通知")
	}
}
