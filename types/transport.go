package types

import (
	"encoding/binary"
	"errors"
)

// errDatagramTooSmall is returned when a datagram payload is too short to
// contain a valid flow ID varint.
var errDatagramTooSmall = errors.New("datagram too small to decode")

// DatagramFrame carries one relayed datagram.
// Wire encoding uses only FlowID and Payload with layout:
// [flowID varint][payload bytes]
type DatagramFrame struct {
	FlowID   uint32
	Payload  []byte
	Address  string
	RelayURL string
	UDPAddr  string
}

// EncodeDatagram serialises a flow-framed datagram for transmission.
func EncodeDatagram(flowID uint32, payload []byte) []byte {
	return EncodeDatagramAppend(make([]byte, 0, binary.MaxVarintLen32+len(payload)), flowID, payload)
}

// EncodeDatagramAppend appends a flow-framed datagram to dst and returns the
// extended slice. Callers can pre-allocate or reuse a buffer and avoid one
// allocation in the hot path.
func EncodeDatagramAppend(dst []byte, flowID uint32, payload []byte) []byte {
	var buf [binary.MaxVarintLen32]byte
	n := binary.PutUvarint(buf[:], uint64(flowID))
	out := append(dst, buf[:n]...)
	out = append(out, payload...)
	return out
}

// DecodeDatagram deserialises a flow-framed datagram.
func DecodeDatagram(data []byte) (DatagramFrame, error) {
	flowID, n := binary.Uvarint(data)
	if n <= 0 {
		return DatagramFrame{}, errDatagramTooSmall
	}
	return DatagramFrame{
		FlowID:  uint32(flowID),
		Payload: data[n:],
	}, nil
}
