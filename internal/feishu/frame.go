package feishu

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// 帧的 method 字段
const (
	frameMethodControl int32 = 0
	frameMethodData    int32 = 1
)

// protobuf 的编码类型，长连接的帧只用到 varint 与 length-delimited，另外两种定长类型只在跳过未知字段时出现
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

var errMalformedFrame = errors.New("长连接帧的编码不完整")

// wsFrame 是长连接上收发的帧，字段与飞书 pbbp2.proto 的 Frame 一一对应
type wsFrame struct {
	SeqID           uint64
	LogID           uint64
	Service         int32
	Method          int32
	Headers         []wsHeader
	PayloadEncoding string
	PayloadType     string
	Payload         []byte
	LogIDNew        string
}

// wsHeader 是帧头里的一个键值对
type wsHeader struct {
	Key   string
	Value string
}

// header 取帧头里某个键的值，没有时返回空串
func (f *wsFrame) header(key string) string {
	for _, item := range f.Headers {
		if item.Key == key {
			return item.Value
		}
	}

	return ""
}

// marshal 把帧编码成 protobuf
//
// 必填的 1 至 4 号字段与三个字符串字段即使为零值也写出，payload 为 nil 时不写，与官方 SDK 的编码结果一致
func (f *wsFrame) marshal() []byte {
	var buffer []byte

	buffer = appendVarintField(buffer, 1, f.SeqID)
	buffer = appendVarintField(buffer, 2, f.LogID)
	// int32 按 protobuf 的约定做符号扩展，负数编码成 10 字节
	buffer = appendVarintField(buffer, 3, uint64(int64(f.Service)))
	buffer = appendVarintField(buffer, 4, uint64(int64(f.Method)))

	for _, item := range f.Headers {
		var encoded []byte

		encoded = appendBytesField(encoded, 1, item.Key)
		encoded = appendBytesField(encoded, 2, item.Value)
		buffer = appendBytesField(buffer, 5, encoded)
	}

	buffer = appendBytesField(buffer, 6, f.PayloadEncoding)
	buffer = appendBytesField(buffer, 7, f.PayloadType)

	if f.Payload != nil {
		buffer = appendBytesField(buffer, 8, f.Payload)
	}

	return appendBytesField(buffer, 9, f.LogIDNew)
}

// unmarshalFrame 解码一帧，未知字段与编码类型不符的字段一律跳过
func unmarshalFrame(data []byte) (*wsFrame, error) {
	frame := &wsFrame{}

	for len(data) > 0 {
		var (
			field protoField
			err   error
		)

		field, data, err = readField(data)
		if err != nil {
			return nil, err
		}

		switch {
		case field.is(1, wireVarint):
			frame.SeqID = field.varint
		case field.is(2, wireVarint):
			frame.LogID = field.varint
		case field.is(3, wireVarint):
			frame.Service = int32(field.varint)
		case field.is(4, wireVarint):
			frame.Method = int32(field.varint)
		case field.is(5, wireBytes):
			var item wsHeader

			item, err = unmarshalHeader(field.bytes)
			if err != nil {
				return nil, err
			}

			frame.Headers = append(frame.Headers, item)
		case field.is(6, wireBytes):
			frame.PayloadEncoding = string(field.bytes)
		case field.is(7, wireBytes):
			frame.PayloadType = string(field.bytes)
		case field.is(8, wireBytes):
			frame.Payload = field.bytes
		case field.is(9, wireBytes):
			frame.LogIDNew = string(field.bytes)
		}
	}

	return frame, nil
}

// unmarshalHeader 解码帧头里的一个键值对
func unmarshalHeader(data []byte) (wsHeader, error) {
	var item wsHeader

	for len(data) > 0 {
		var (
			field protoField
			err   error
		)

		field, data, err = readField(data)
		if err != nil {
			return item, err
		}

		switch {
		case field.is(1, wireBytes):
			item.Key = string(field.bytes)
		case field.is(2, wireBytes):
			item.Value = string(field.bytes)
		}
	}

	return item, nil
}

// protoField 是解码出的一个字段，varint 与 bytes 按编码类型二选一
type protoField struct {
	number   uint64
	wireType uint64
	varint   uint64
	bytes    []byte
}

func (f protoField) is(number, wireType uint64) bool {
	return f.number == number && f.wireType == wireType
}

// readField 从 data 头部读出一个字段，返回该字段与剩余的数据
func readField(data []byte) (protoField, []byte, error) {
	var field protoField

	tag, size := binary.Uvarint(data)
	if size <= 0 {
		return field, nil, errMalformedFrame
	}

	data = data[size:]
	field.number = tag >> 3
	field.wireType = tag & 7

	if field.number == 0 {
		return field, nil, errMalformedFrame
	}

	// 按编码类型取出字段值，定长类型只需要跳过
	switch field.wireType {
	case wireVarint:
		field.varint, size = binary.Uvarint(data)
		if size <= 0 {
			return field, nil, errMalformedFrame
		}

		return field, data[size:], nil
	case wireBytes:
		var length uint64

		length, size = binary.Uvarint(data)
		if size <= 0 || length > uint64(len(data)-size) {
			return field, nil, errMalformedFrame
		}

		end := size + int(length)

		field.bytes = data[size:end:end]

		return field, data[end:], nil
	case wireFixed64:
		return skipFixed(field, data, 8)
	case wireFixed32:
		return skipFixed(field, data, 4)
	default:
		return field, nil, fmt.Errorf("长连接帧里有不支持的编码类型 %d", field.wireType)
	}
}

// skipFixed 跳过定长类型的字段值
func skipFixed(field protoField, data []byte, width int) (protoField, []byte, error) {
	if len(data) < width {
		return field, nil, errMalformedFrame
	}

	return field, data[width:], nil
}

func appendVarintField(buffer []byte, number, value uint64) []byte {
	buffer = binary.AppendUvarint(buffer, number<<3|wireVarint)

	return binary.AppendUvarint(buffer, value)
}

func appendBytesField[T ~string | ~[]byte](buffer []byte, number uint64, value T) []byte {
	buffer = binary.AppendUvarint(buffer, number<<3|wireBytes)
	buffer = binary.AppendUvarint(buffer, uint64(len(value)))

	return append(buffer, value...)
}
