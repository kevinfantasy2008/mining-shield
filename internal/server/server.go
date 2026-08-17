// Package server 服务器端：在公网 VPS 上接收本地端的 wss 加密隧道，
// 解密后按标准 Stratum 协议连接真实矿池。
//
// 只监听 127.0.0.1，由 Nginx 在 443 端口反代进来；
// 路径或 token 不匹配时一律返回 404，对外表现得像普通网站。
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"mining_shield/internal/proto"
	"mining_shield/internal/tunnel"
)

type Server struct {
	cfg      *Config
	upgrader websocket.Upgrader
}

func New(cfg *Config) *Server {
	return &Server{
		cfg: cfg,
		upgrader: websocket.Upgrader{
			// 握手响应体大小够用即可；Origin 检查不需要（非浏览器客户端，且无 Origin 头时默认放行）
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
}

// Run 启动 HTTP 服务，直到 ctx 取消。
func (s *Server) Run(ctx context.Context) error {
	httpSrv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()

	slog.Info("relay server started", "addr", s.cfg.Listen, "path", s.cfg.Path, "pools", len(s.cfg.Pools))
	err := httpSrv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ServeHTTP 认证 + WebSocket 升级。认证失败一律 404，不暴露任何代理特征。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != s.cfg.Path || !tokenEqual(r.Header.Get("X-Auth-Token"), s.cfg.Token) {
		http.NotFound(w, r)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("upgrade failed", "remote", r.RemoteAddr, "err", err)
		return
	}
	slog.Info("tunnel connected", "remote", r.RemoteAddr)
	s.serve(conn)
}

func (s *Server) serve(conn *websocket.Conn) {
	defer conn.Close()

	readTimeout := s.cfg.ReadTimeout.D()
	conn.SetReadLimit(proto.MaxFrameSize)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	// 收到本地端心跳时刷新读超时并回 Pong
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	m := tunnel.NewMux(func(b []byte) error {
		conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
		return conn.WriteMessage(websocket.BinaryMessage, b)
	}, s.openPool)
	defer m.Close() // 隧道断开 → 关闭该隧道上所有矿池连接

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			slog.Info("tunnel closed", "err", err)
			return
		}
		if err := m.HandleMessage(data); err != nil {
			slog.Warn("bad frame", "err", err)
		}
	}
}

// openPool 收到 OPEN 时按配置顺序拨号矿池，全部失败则返回错误（本地端会关流）。
func (s *Server) openPool(id uint32) (io.ReadWriteCloser, error) {
	var lastErr error
	for _, p := range s.cfg.Pools {
		scheme, addr, _ := parsePoolURL(p)
		c, err := dialPool(scheme, addr, s.cfg.DialTimeout.D())
		if err != nil {
			slog.Warn("pool dial failed, trying next", "stream", id, "pool", p, "err", err)
			lastErr = err
			continue
		}
		slog.Info("pool connected", "stream", id, "pool", p)
		return c, nil
	}
	return nil, lastErr
}

func dialPool(scheme, addr string, timeout time.Duration) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	switch scheme {
	case "ssl":
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		return tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host})
	default:
		return dialer.Dial("tcp", addr)
	}
}

// tokenEqual 常量时间比较，避免时序侧信道。
func tokenEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0 && !strings.ContainsAny(a, "\r\n")
}
