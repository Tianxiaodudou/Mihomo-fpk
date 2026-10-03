package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Mihomo 管理 Mihomo 内核进程（显式代理，无 TUN）。
type Mihomo struct {
	mu   sync.Mutex
	lastErr string
}

func NewMihomo() *Mihomo { return &Mihomo{} }

func (m *Mihomo) pid() int {
	b, err := os.ReadFile(P.MihomoPid())
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return v
}

// Running 判定内核是否在运行（pid 存活 且 Unix Socket 存在）。
func (m *Mihomo) Running() bool {
	p := m.pid()
	if p == 0 || !processAlive(p) {
		return false
	}
	if _, err := os.Stat(P.MihomoSock()); err != nil {
		return false
	}
	return true
}

func (m *Mihomo) LastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}

// Start 启动内核并等待 Unix Socket 就绪。
func (m *Mihomo) Start(cfgPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningLocked() {
		return nil
	}
	bin := P.MihomoBin()
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return fmt.Errorf("mihomo 二进制不存在：%s", bin)
	}
	if st, err := os.Stat(cfgPath); err != nil || st.IsDir() {
		return fmt.Errorf("配置文件不存在：%s", cfgPath)
	}
	_ = os.Remove(P.MihomoSock())

	logf, err := os.OpenFile(P.MihomoLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	fmt.Fprintf(logf, "\n===== %s start mihomo =====\n", time.Now().Format(time.RFC3339))

	cmd := exec.Command(bin, "-d", P.Var, "-f", cfgPath)
	cmd.Dir = P.Var
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		m.lastErr = err.Error()
		return fmt.Errorf("启动 mihomo 失败：%w", err)
	}
	pid := cmd.Process.Pid
	_ = atomicWrite(P.MihomoPid(), []byte(strconv.Itoa(pid)), 0o600)

	// 立即回收，避免僵尸进程
	go func() { _ = cmd.Wait() }()

	for i := 0; i < 60; i++ {
		if _, err := os.Stat(P.MihomoSock()); err == nil {
			_ = os.Chmod(P.MihomoSock(), 0o600)
			m.lastErr = ""
			return nil
		}
		if !processAlive(pid) {
			m.lastErr = "内核进程提前退出"
			return fmt.Errorf("mihomo 启动后立即退出，请查看 %s", P.MihomoLog())
		}
		time.Sleep(100 * time.Millisecond)
	}
	m.lastErr = "内核 socket 未就绪"
	return fmt.Errorf("mihomo 启动超时（未生成 %s）", P.MihomoSock())
}

func (m *Mihomo) runningLocked() bool {
	p := m.pid()
	if p == 0 || !processAlive(p) {
		return false
	}
	_, err := os.Stat(P.MihomoSock())
	return err == nil
}

// Stop 发送 SIGTERM，超时后 SIGKILL。
func (m *Mihomo) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pid()
	if p > 0 && processAlive(p) {
		_ = syscall.Kill(p, syscall.SIGTERM)
		for i := 0; i < 100; i++ {
			if !processAlive(p) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if processAlive(p) {
			_ = syscall.Kill(p, syscall.SIGKILL)
			time.Sleep(200 * time.Millisecond)
		}
	}
	_ = os.Remove(P.MihomoPid())
	_ = os.Remove(P.MihomoSock())
	return nil
}

// Reload 通过管理 API 重载配置；失败则降级为重启。
func (m *Mihomo) Reload() error {
	if !m.Running() {
		return fmt.Errorf("内核未运行")
	}
	_, code, err := m.API(http.MethodPut, "/configs?force=true", map[string]any{"path": P.ConfigFile()})
	if err != nil || code >= 300 {
		if err == nil {
			err = fmt.Errorf("reload 返回 %d", code)
		}
		_ = m.Stop()
		return m.Start(P.ConfigFile())
	}
	return nil
}

// API 通过 Unix Socket 调用内核管理接口。
func (m *Mihomo) API(method, path string, body any) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", P.MihomoSock())
			},
		},
		Timeout: 20 * time.Second,
	}
	req, err := http.NewRequest(method, "http://localhost"+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return data, resp.StatusCode, nil
}

// DialUnixWS 建立到内核 Unix Socket 的裸连接（供 WebSocket 转发使用）。
func DialUnixWS(socket string) (net.Conn, error) {
	return net.DialTimeout("unix", socket, 10*time.Second)
}

// ReadSSEHeaders 读取 HTTP 响应头，用于日志流代理。
func ReadSSEHeaders(c net.Conn) (int, http.Header, *bufio.Reader, error) {
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return 0, nil, br, err
	}
	return resp.StatusCode, resp.Header, br, nil
}
