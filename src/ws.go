package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsMagic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WSConn 极简 WebSocket 连接（仅实现转发连接/日志流所需的帧类型）。
type WSConn struct {
	c      net.Conn
	br     *bufio.Reader
	mu     sync.Mutex
	client bool // true 表示本端是客户端，发出的帧需要掩码
}

func wsAccept(key string) string {
	h := sha1.New()
	io.WriteString(h, key+wsMagic)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func randKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// UpgradeServer 把浏览器发来的 HTTP 请求升级为 WebSocket。
func UpgradeServer(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, fmt.Errorf("缺少 Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("当前连接不支持 Hijack")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	return &WSConn{c: conn, br: brw.Reader}, nil
}

// DialWS 作为客户端向 Mihomo 的 Unix Socket 发起 WebSocket 握手。
func DialWS(targetPath string) (*WSConn, error) {
	conn, err := DialUnixWS(P.MihomoSock())
	if err != nil {
		return nil, err
	}
	req := "GET " + targetPath + " HTTP/1.1\r\nHost: localhost\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + randKey() + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		conn.Close()
		return nil, fmt.Errorf("内核 WebSocket 握手失败：%s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return &WSConn{c: conn, br: br, client: true}, nil
}

// ReadMessage 读取一条完整消息（自动拼接分片）。
func (w *WSConn) ReadMessage() (int, []byte, error) {
	opcode := 0
	var payload []byte
	for {
		op, fin, data, err := w.readFrame()
		if err != nil {
			return 0, nil, err
		}
		if opcode == 0 {
			opcode = op
		}
		payload = append(payload, data...)
		if fin {
			return opcode, payload, nil
		}
	}
}

func (w *WSConn) readFrame() (op int, fin bool, payload []byte, err error) {
	h := make([]byte, 2)
	if _, err = io.ReadFull(w.br, h); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	op = int(h[0] & 0x0f)
	masked := h[1]&0x80 != 0
	ln := int64(h[1] & 0x7f)
	switch ln {
	case 126:
		b := make([]byte, 2)
		if _, err = io.ReadFull(w.br, b); err != nil {
			return
		}
		ln = int64(binary.BigEndian.Uint16(b))
	case 127:
		b := make([]byte, 8)
		if _, err = io.ReadFull(w.br, b); err != nil {
			return
		}
		ln = int64(binary.BigEndian.Uint64(b))
	}
	if ln < 0 || ln > 16<<20 {
		return 0, false, nil, fmt.Errorf("帧长度非法：%d", ln)
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err = io.ReadFull(w.br, mask); err != nil {
			return
		}
	}
	payload = make([]byte, ln)
	if _, err = io.ReadFull(w.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return op, fin, payload, nil
}

func (w *WSConn) writeFrame(op int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	b0 := byte(0x80 | (op & 0x0f))
	n := len(data)
	maskBit := byte(0)
	if w.client {
		maskBit = 0x80
	}
	var hdr []byte
	switch {
	case n < 126:
		hdr = []byte{b0, maskBit | byte(n)}
	case n < 65536:
		hdr = []byte{b0, maskBit | 126, byte(n >> 8), byte(n)}
	default:
		hdr = []byte{b0, maskBit | 127, 0, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	payload := data
	if w.client {
		mask := make([]byte, 4)
		_, _ = rand.Read(mask)
		payload = make([]byte, len(data))
		for i := range data {
			payload[i] = data[i] ^ mask[i%4]
		}
		hdr = append(hdr, mask...)
	}
	_, err := w.c.Write(append(hdr, payload...))
	return err
}

func (w *WSConn) WriteText(b []byte) error { return w.writeFrame(1, b) }
func (w *WSConn) WritePing() error         { return w.writeFrame(9, nil) }
func (w *WSConn) WriteClose() error        { return w.writeFrame(8, []byte{0x03, 0xe8}) }
func (w *WSConn) Close() error             { return w.c.Close() }

func (w *WSConn) SetReadDeadline(t time.Time) { _ = w.c.SetReadDeadline(t) }
