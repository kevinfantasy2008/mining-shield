// Package tunnel 实现隧道内的 Stream 多路复用。
//
// 一条 wss 连接承载多个 Stream（每个对应一条矿机/矿池 TCP 会话）。
// 本地端主动 Open；服务器端收到 OPEN 后通过 onOpen 回调拨号矿池。
// 写方向由单一 writeLoop 串行化（gorilla/websocket 只允许一个并发写者）。
package tunnel

import (
	"errors"
	"io"
	"log/slog"
	"sync"

	"mining_shield/internal/proto"
)

var ErrClosed = errors.New("mux closed")

const (
	sendQueueSize   = 512 // 隧道级发送队列
	streamQueueSize = 64  // 每 Stream 下行队列，满了说明对端消费慢，直接关流
	readBufSize     = 32 * 1024
)

// OnOpenFunc 收到对端 OPEN 时建立上行连接（服务器端：拨号矿池）。
type OnOpenFunc func(id uint32) (io.ReadWriteCloser, error)

type Mux struct {
	write  func([]byte) error // 发送一条 WS binary message
	onOpen OnOpenFunc         // 仅服务器端使用；本地端为 nil

	sendCh    chan proto.Frame
	done      chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	streams map[uint32]*stream
	closed  bool
}

type stream struct {
	id        uint32
	rw        io.ReadWriteCloser
	out       chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

// NewMux 创建 Mux 并启动写循环。write 必须不是并发安全的也没关系——Mux 内部串行调用。
func NewMux(write func([]byte) error, onOpen OnOpenFunc) *Mux {
	m := &Mux{
		write:   write,
		onOpen:  onOpen,
		sendCh:  make(chan proto.Frame, sendQueueSize),
		done:    make(chan struct{}),
		streams: make(map[uint32]*stream),
	}
	go m.writeLoop()
	return m
}

// Done 在 Mux 关闭时返回的 channel 会被关闭。
func (m *Mux) Done() <-chan struct{} { return m.done }

// Open 本地端使用：注册一条新 Stream（关联矿机连接）并通知对端。
func (m *Mux) Open(id uint32, rw io.ReadWriteCloser) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		rw.Close()
		return ErrClosed
	}
	m.addStreamLocked(id, rw)
	m.mu.Unlock()
	m.send(proto.Frame{Type: proto.TypeOpen, StreamID: id})
	return nil
}

// HandleMessage 读循环入口：处理收到的一条 WS binary message。
func (m *Mux) HandleMessage(b []byte) error {
	f, err := proto.Parse(b)
	if err != nil {
		return err
	}
	switch f.Type {
	case proto.TypeData:
		m.mu.Lock()
		s := m.streams[f.StreamID]
		m.mu.Unlock()
		if s == nil {
			return nil // 已关闭，丢弃
		}
		select {
		case s.out <- f.Payload:
		case <-s.done:
		default:
			// 对端消费过慢（队列积压），关流避免拖垮整条隧道
			slog.Warn("stream consumer too slow, closing", "stream", f.StreamID)
			m.closeStream(f.StreamID, true)
		}
	case proto.TypeOpen:
		if m.onOpen == nil {
			m.send(proto.Frame{Type: proto.TypeClose, StreamID: f.StreamID})
			return nil
		}
		m.mu.Lock()
		_, dup := m.streams[f.StreamID]
		m.mu.Unlock()
		if dup {
			m.closeStream(f.StreamID, true)
			return nil
		}
		rw, err := m.onOpen(f.StreamID)
		if err != nil {
			slog.Warn("open upstream failed", "stream", f.StreamID, "err", err)
			m.send(proto.Frame{Type: proto.TypeClose, StreamID: f.StreamID})
			return nil
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			rw.Close()
			return nil
		}
		m.addStreamLocked(f.StreamID, rw)
		m.mu.Unlock()
	case proto.TypeClose:
		m.closeStream(f.StreamID, false)
	}
	return nil
}

// Close 关闭整个 Mux：所有 Stream 的上行连接都会被关闭。
func (m *Mux) Close() {
	m.closeOnce.Do(func() {
		close(m.done)
		m.mu.Lock()
		ss := m.streams
		m.streams = make(map[uint32]*stream)
		m.closed = true
		m.mu.Unlock()
		for _, s := range ss {
			s.closeOnce.Do(func() {
				close(s.done)
				s.rw.Close()
			})
		}
	})
}

func (m *Mux) addStreamLocked(id uint32, rw io.ReadWriteCloser) {
	s := &stream{
		id:   id,
		rw:   rw,
		out:  make(chan []byte, streamQueueSize),
		done: make(chan struct{}),
	}
	m.streams[id] = s
	go s.writeLoop(m)
	go m.readPump(s)
}

func (m *Mux) closeStream(id uint32, notify bool) {
	m.mu.Lock()
	s, ok := m.streams[id]
	if ok {
		delete(m.streams, id)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	s.closeOnce.Do(func() {
		close(s.done)
		s.rw.Close()
		if notify {
			m.send(proto.Frame{Type: proto.TypeClose, StreamID: id})
		}
	})
}

func (m *Mux) send(f proto.Frame) {
	select {
	case m.sendCh <- f:
	case <-m.done:
	}
}

func (m *Mux) writeLoop() {
	for {
		select {
		case f := <-m.sendCh:
			if err := m.write(f.Marshal()); err != nil {
				m.Close()
				return
			}
		case <-m.done:
			return
		}
	}
}

// readPump 从上行连接（矿机侧或矿池侧）读字节并打成 DATA 帧。
func (m *Mux) readPump(s *stream) {
	buf := make([]byte, readBufSize)
	for {
		n, err := s.rw.Read(buf)
		if n > 0 {
			p := make([]byte, n)
			copy(p, buf[:n])
			m.send(proto.Frame{Type: proto.TypeData, StreamID: s.id, Payload: p})
		}
		if err != nil {
			m.closeStream(s.id, true)
			return
		}
	}
}

func (s *stream) writeLoop(m *Mux) {
	for {
		select {
		case p := <-s.out:
			if _, err := s.rw.Write(p); err != nil {
				m.closeStream(s.id, true)
				return
			}
		case <-s.done:
			return
		}
	}
}
