// Package agent 本地端：对矿机暴露标准 stratum+tcp 入口，
// 通过 wss 加密隧道把流量中转到服务器端。
package agent

import (
	"context"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"mining_shield/internal/proto"
	"mining_shield/internal/tunnel"
)

type Agent struct {
	cfg *Config

	mu     sync.Mutex
	mux    *tunnel.Mux // nil 表示隧道未建立
	nextID uint32
}

func New(cfg *Config) *Agent {
	return &Agent{cfg: cfg}
}

// Run 启动矿机监听并维持隧道，直到 ctx 取消。
func (a *Agent) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	slog.Info("miner listener started", "addr", a.cfg.Listen)

	go a.acceptLoop(ctx, ln)

	return a.tunnelLoop(ctx)
}

// acceptLoop 接受矿机连接。隧道未就绪时直接断开，让矿机走自己的重试逻辑。
func (a *Agent) acceptLoop(ctx context.Context, ln net.Listener) {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("accept failed", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		a.handleMiner(c)
	}
}

func (a *Agent) handleMiner(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	a.mu.Lock()
	m := a.mux
	if m == nil {
		a.mu.Unlock()
		slog.Warn("tunnel not ready, rejecting miner", "remote", c.RemoteAddr())
		c.Close()
		return
	}
	a.nextID++
	id := a.nextID
	a.mu.Unlock()

	if err := m.Open(id, c); err != nil {
		slog.Warn("open stream failed", "remote", c.RemoteAddr(), "err", err)
		return
	}
	slog.Info("miner connected", "remote", c.RemoteAddr(), "stream", id)
}

// tunnelLoop 按服务器列表轮换拨号，断线后指数退避重连。
func (a *Agent) tunnelLoop(ctx context.Context) error {
	backoff := a.cfg.MinBackoff.D()
	serverIdx := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		srv := a.cfg.Servers[serverIdx%len(a.cfg.Servers)]
		serverIdx++

		start := time.Now()
		err := a.runSession(ctx, srv)
		if ctx.Err() != nil {
			return nil
		}
		// 会话维持超过 1 分钟说明连接本身稳定，重置退避
		if time.Since(start) > time.Minute {
			backoff = a.cfg.MinBackoff.D()
		}
		slog.Warn("tunnel disconnected, will reconnect",
			"server", srv.URL, "err", err, "retry_in", backoff)
		select {
		case <-time.After(jitter(backoff)):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, a.cfg.MaxBackoff.D())
	}
}

// runSession 建立一条 wss 隧道并运行至断开。
func (a *Agent) runSession(ctx context.Context, srv ServerEntry) error {
	header := http.Header{}
	header.Set("X-Auth-Token", srv.Token)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	conn, resp, err := dialer.DialContext(ctx, srv.URL, header)
	if err != nil {
		if resp != nil {
			return errWithStatus(err, resp.StatusCode)
		}
		return err
	}
	defer conn.Close()
	slog.Info("tunnel established", "server", srv.URL)

	readTimeout := a.cfg.ReadTimeout.D()
	conn.SetReadLimit(proto.MaxFrameSize)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return nil
	})

	m := tunnel.NewMux(func(b []byte) error {
		conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return conn.WriteMessage(websocket.BinaryMessage, b)
	}, nil)

	a.setMux(m)
	defer func() {
		a.clearMux(m)
		m.Close() // 隧道断开 → 断开所有矿机连接，矿机自行重连
	}()

	// 会话级 done：ctx 取消或 Mux 关闭时统一退出
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-m.Done():
		case <-sessionDone:
		}
	}()

	// 心跳：保持 NAT/防火墙会话，同时探测死链
	go a.pingLoop(conn, m, sessionDone)

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if err := m.HandleMessage(data); err != nil {
			slog.Warn("bad frame", "err", err)
		}
	}
}

func (a *Agent) pingLoop(conn *websocket.Conn, m *tunnel.Mux, sessionDone chan struct{}) {
	t := time.NewTicker(a.cfg.PingInterval.D())
	defer t.Stop()
	for {
		select {
		case <-t.C:
			// WriteControl 与写循环的 WriteMessage 并发安全（gorilla 保证）
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				m.Close()
				return
			}
		case <-m.Done():
			return
		case <-sessionDone:
			return
		}
	}
}

func (a *Agent) setMux(m *tunnel.Mux) {
	a.mu.Lock()
	a.mux = m
	a.mu.Unlock()
}

func (a *Agent) clearMux(m *tunnel.Mux) {
	a.mu.Lock()
	if a.mux == m {
		a.mux = nil
	}
	a.mu.Unlock()
}

func jitter(d time.Duration) time.Duration {
	// ±20% 抖动，避免断线风暴时所有节点同时重连
	delta := d / 5
	return d - delta + time.Duration(rand.Int63n(int64(2*delta)+1))
}

type statusError struct {
	err    error
	status int
}

func (e *statusError) Error() string { return e.err.Error() }

func errWithStatus(err error, status int) error {
	return &statusError{err: err, status: status}
}
