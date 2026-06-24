package policy

import (
	"sync"
	"time"

	"github.com/gosuda/portal-tunnel/v2/utils"
)

const bpsManagerShards = 16

type bpsLimiterShard struct {
	mu       sync.Mutex
	limiters map[string]*bpsLimiter
}

type BPSManager struct {
	identityBPS *utils.Snapshot[map[string]int64]
	shards      [bpsManagerShards]*bpsLimiterShard
}

func NewBPSManager() *BPSManager {
	m := &BPSManager{
		identityBPS: utils.NewSnapshot(map[string]int64{}, utils.CloneMap[string, int64]),
	}
	for i := range m.shards {
		m.shards[i] = &bpsLimiterShard{limiters: make(map[string]*bpsLimiter)}
	}
	return m
}

func bpsShardKey(key string) int {
	// FNV-1a hash reduced to a power-of-two shard index.
	h := uint32(0x811c9dc5)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 0x01000193
	}
	return int(h) & (bpsManagerShards - 1)
}

func (m *BPSManager) IdentityBPS(key string) int64 {
	if m == nil || key == "" {
		return 0
	}
	if m.identityBPS == nil {
		return 0
	}
	return m.identityBPS.Load()[key]
}

func (m *BPSManager) SetIdentityBPS(key string, bps int64) {
	if m == nil || key == "" {
		return
	}

	if m.identityBPS == nil {
		return
	}
	if bps <= 0 {
		m.identityBPS.UpdateCopy(func(limits *map[string]int64) {
			delete(*limits, key)
		})
		shard := m.shards[bpsShardKey(key)]
		shard.mu.Lock()
		delete(shard.limiters, key)
		shard.mu.Unlock()
		return
	}
	m.identityBPS.UpdateCopy(func(limits *map[string]int64) {
		if *limits == nil {
			*limits = make(map[string]int64)
		}
		(*limits)[key] = bps
	})
}

func (m *BPSManager) DeleteIdentityBPS(key string) {
	if m == nil || key == "" {
		return
	}

	if m.identityBPS != nil {
		m.identityBPS.UpdateCopy(func(limits *map[string]int64) {
			delete(*limits, key)
		})
	}
	shard := m.shards[bpsShardKey(key)]
	shard.mu.Lock()
	delete(shard.limiters, key)
	shard.mu.Unlock()
}

func (m *BPSManager) IdentityBPSLimits() map[string]int64 {
	if m == nil {
		return nil
	}

	if m.identityBPS == nil {
		return nil
	}
	return m.identityBPS.Load()
}

func (m *BPSManager) SetIdentityBPSLimits(limits map[string]int64) {
	if m == nil {
		return
	}

	next := make(map[string]int64, len(limits))
	for key, bps := range limits {
		if key == "" || bps <= 0 {
			continue
		}
		next[key] = bps
	}

	if m.identityBPS != nil {
		m.identityBPS.Store(next)
	}
	for _, shard := range m.shards {
		shard.mu.Lock()
		shard.limiters = make(map[string]*bpsLimiter)
		shard.mu.Unlock()
	}
}

func (m *BPSManager) ThrottleIdentityBPS(key string, maxBytes int) int {
	if m == nil || key == "" || maxBytes <= 0 {
		return maxBytes
	}

	for {
		bps, limiter := m.identityLimiter(key)
		if bps <= 0 || limiter == nil {
			return maxBytes
		}
		chunkSize := bpsChunkSize(maxBytes, bps)
		if wait := limiter.reserve(float64(chunkSize), float64(bps)); wait > 0 {
			time.Sleep(wait)
			continue
		}
		return chunkSize
	}
}

func (m *BPSManager) identityLimiter(key string) (int64, *bpsLimiter) {
	bps := m.IdentityBPS(key)
	if bps <= 0 {
		return 0, nil
	}

	shard := m.shards[bpsShardKey(key)]
	shard.mu.Lock()
	limiter := shard.limiters[key]
	if limiter == nil {
		limiter = &bpsLimiter{}
		shard.limiters[key] = limiter
	}
	shard.mu.Unlock()
	return bps, limiter
}

func bpsChunkSize(length int, bps int64) int {
	if bps <= 0 {
		return length
	}
	chunk := bps / 10
	if chunk < 1 {
		chunk = 1
	}
	if chunk > int64(length) {
		chunk = int64(length)
	}
	return int(chunk)
}

type bpsLimiter struct {
	mu        sync.Mutex
	tokens    float64
	updatedAt time.Time
}

func (l *bpsLimiter) reserve(bytes, bps float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if l.updatedAt.IsZero() {
		l.updatedAt = now
	} else if elapsed := now.Sub(l.updatedAt).Seconds(); elapsed > 0 {
		l.tokens += elapsed * bps
		l.updatedAt = now
	}
	if l.tokens > bps {
		l.tokens = bps
	}

	if l.tokens >= bytes {
		l.tokens -= bytes
		return 0
	}

	missing := bytes - l.tokens
	l.tokens = 0
	l.updatedAt = now
	return time.Duration(missing / bps * float64(time.Second))
}
