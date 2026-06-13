package types

import (
	"encoding/binary"
	"testing"
)

func BenchmarkEncodeDatagram(b *testing.B) {
	payload := make([]byte, 1350)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeDatagram(12345, payload)
	}
}

func BenchmarkEncodeDatagramAppend(b *testing.B) {
	payload := make([]byte, 1350)
	dst := make([]byte, 0, binary.MaxVarintLen32+len(payload))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EncodeDatagramAppend(dst, 12345, payload)
	}
}
