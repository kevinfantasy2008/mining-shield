// Package proto 定义本地端与服务器端之间隧道内部的帧协议。
//
// 帧格式（承载于 WebSocket binary message，天然有消息边界）：
//
//	┌──────────┬─────────────┬──────────────────┐
//	│ Type (1) │ StreamID(4) │ Payload (N)      │
//	└──────────┴─────────────┴──────────────────┘
//
// 每条矿机/矿池会话对应一个 StreamID，一条 wss 连接上多路复用多个 Stream。
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	TypeOpen  byte = 0x01 // 建立新 Stream（Payload 为空，预留扩展）
	TypeData  byte = 0x02 // 数据载荷，原样转发
	TypeClose byte = 0x03 // 关闭 Stream
)

const (
	// MaxPayload 单帧载荷上限（Stratum 消息通常只有几百字节，留足余量）
	MaxPayload = 1 << 20 // 1 MiB
	// MaxFrameSize = 头部长度 + MaxPayload，用于 WS 读限制
	MaxFrameSize = 5 + MaxPayload
	headerSize   = 5
)

// Frame 隧道内的一帧
type Frame struct {
	Type     byte
	StreamID uint32
	Payload  []byte
}

func (f Frame) Marshal() []byte {
	b := make([]byte, headerSize+len(f.Payload))
	b[0] = f.Type
	binary.BigEndian.PutUint32(b[1:headerSize], f.StreamID)
	copy(b[headerSize:], f.Payload)
	return b
}

var ErrFrameTooShort = errors.New("frame too short")

func Parse(b []byte) (Frame, error) {
	if len(b) < headerSize {
		return Frame{}, ErrFrameTooShort
	}
	f := Frame{
		Type:     b[0],
		StreamID: binary.BigEndian.Uint32(b[1:headerSize]),
		Payload:  b[headerSize:],
	}
	switch f.Type {
	case TypeOpen, TypeData, TypeClose:
	default:
		return Frame{}, fmt.Errorf("unknown frame type: 0x%02x", f.Type)
	}
	if len(f.Payload) > MaxPayload {
		return Frame{}, fmt.Errorf("payload too large: %d", len(f.Payload))
	}
	return f, nil
}
