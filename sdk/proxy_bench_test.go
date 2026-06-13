package sdk

import (
	"io"
	"net"
	"testing"
)

func BenchmarkProxyCopyBuffer(b *testing.B) {
	payload := make([]byte, 1024*1024)
	for i := range payload {
		payload[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c1, c2 := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = c1.Write(payload)
			_ = c1.Close()
		}()

		buf := tcpCopyBufferPool().Get()
		_, err := io.CopyBuffer(io.Discard, c2, buf)
		tcpCopyBufferPool().Put(buf)
		_ = c2.Close()
		<-done
		if err != nil {
			b.Fatal(err)
		}
	}
}
