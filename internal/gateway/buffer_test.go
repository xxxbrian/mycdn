package gateway

import "testing"

func TestProxyBufferPoolKeepsFixedSizeAndIndependentOwnership(t *testing.T) {
	var pool proxyBufferPool
	first, second := pool.Get(), pool.Get()
	if len(first) != proxyBufferBytes || cap(first) != proxyBufferBytes || len(second) != proxyBufferBytes {
		t.Fatal("copy buffers are not 32 KiB")
	}
	first[0] = 1
	second[0] = 2
	if first[0] != 1 {
		t.Fatal("active copies share a buffer")
	}
	pool.Put(first[:1])
	pool.Put(second)
	pool.Put(make([]byte, 1024))
	pool.Put(make([]byte, proxyBufferBytes*2))
	buffer := pool.Get()
	if len(buffer) != proxyBufferBytes || cap(buffer) != proxyBufferBytes {
		t.Fatal("pool accepted or returned an unexpected size")
	}
	pool.Put(buffer)
}
