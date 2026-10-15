package gateway

import "sync"

const proxyBufferBytes = 32 << 10

// proxyBufferPool reuses ReverseProxy's copy buffer. It is not a memory budget;
// each active copy owns its buffer until ReverseProxy returns it.
type proxyBufferPool struct {
	pool sync.Pool
}

func (p *proxyBufferPool) Get() []byte {
	if buffer, ok := p.pool.Get().(*[proxyBufferBytes]byte); ok {
		return buffer[:]
	}
	return make([]byte, proxyBufferBytes)
}

func (p *proxyBufferPool) Put(buffer []byte) {
	if cap(buffer) == proxyBufferBytes {
		p.pool.Put((*[proxyBufferBytes]byte)(buffer[:proxyBufferBytes]))
	}
}
