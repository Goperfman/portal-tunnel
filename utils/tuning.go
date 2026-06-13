package utils

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultGCPercent          = 100
	defaultMemoryLimitPercent = 90
	defaultAdaptiveInterval   = 5 * time.Second
	defaultBeaverPoolSize     = 64 << 20
	defaultAdaptiveGOCMin     = 25
	defaultAdaptiveGOCMax     = 500
	defaultAdaptiveGOCStep    = 25
	defaultTargetHeapRatio    = 0.75
)

// RuntimeTuningConfig controls process-wide runtime tuning knobs.
type RuntimeTuningConfig struct {
	GOGCPercent        int
	MemoryLimitPercent int
	GOMAXPROCS         int
	AdaptiveGC         bool
	AdaptiveInterval   time.Duration
	BeaverEnabled      bool
	BeaverPoolSize     int
	BeaverAllocator    string
}

// DefaultRuntimeTuningConfig returns conservative defaults tuned for low STW.
func DefaultRuntimeTuningConfig() RuntimeTuningConfig {
	return RuntimeTuningConfig{
		GOGCPercent:        defaultGCPercent,
		MemoryLimitPercent: defaultMemoryLimitPercent,
		GOMAXPROCS:         0,
		AdaptiveGC:         true,
		AdaptiveInterval:   defaultAdaptiveInterval,
		BeaverEnabled:      true,
		BeaverPoolSize:     defaultBeaverPoolSize,
		BeaverAllocator:    DefaultRuntimeAllocatorKind(),
	}
}

// ApplyEnv overrides config values from PORTAL_* environment variables.
func (c *RuntimeTuningConfig) ApplyEnv() {
	if v := os.Getenv("PORTAL_GOGC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.GOGCPercent = n
		}
	}
	if v := os.Getenv("PORTAL_MEMLIMIT_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
			c.MemoryLimitPercent = n
		}
	}
	if v := os.Getenv("PORTAL_GOMAXPROCS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.GOMAXPROCS = n
		}
	}
	if v := os.Getenv("PORTAL_ADAPTIVE_GC"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.AdaptiveGC = b
		}
	}
	if v := os.Getenv("PORTAL_BEAVER"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.BeaverEnabled = b
		}
	}
	if v := os.Getenv("PORTAL_BEAVER_POOL_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.BeaverPoolSize = n
		}
	}
	if v := os.Getenv("PORTAL_BEAVER_ALLOCATOR"); v != "" {
		if ValidateAllocatorKind(v) == nil {
			c.BeaverAllocator = v
		}
	}
}

// RuntimeTuning holds the active runtime tuning state and optional beaver-backed middleware.
type RuntimeTuning struct {
	cfg        RuntimeTuningConfig
	middleware func(http.Handler) http.Handler
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	lastGOGC   atomic.Int64
}

var activeTuning atomic.Pointer[RuntimeTuning]

// ActiveRuntimeTuning returns the most recently initialized RuntimeTuning, if any.
func ActiveRuntimeTuning() *RuntimeTuning {
	return activeTuning.Load()
}

// InitRuntimeTuning applies runtime knobs and starts the adaptive GC controller.
// It stores the instance as ActiveRuntimeTuning for use by HTTP middleware helpers.
func InitRuntimeTuning(ctx context.Context, cfg RuntimeTuningConfig) (*RuntimeTuning, error) {
	if cfg.GOGCPercent < 0 {
		cfg.GOGCPercent = defaultGCPercent
	}
	if cfg.MemoryLimitPercent <= 0 || cfg.MemoryLimitPercent > 100 {
		cfg.MemoryLimitPercent = defaultMemoryLimitPercent
	}
	if cfg.AdaptiveInterval <= 0 {
		cfg.AdaptiveInterval = defaultAdaptiveInterval
	}
	if cfg.BeaverPoolSize <= 0 {
		cfg.BeaverPoolSize = defaultBeaverPoolSize
	}
	if cfg.BeaverAllocator == "" {
		cfg.BeaverAllocator = DefaultRuntimeAllocatorKind()
	}
	SetDefaultAllocatorKind(ParseAllocatorKind(cfg.BeaverAllocator))

	rt := &RuntimeTuning{cfg: cfg}
	rt.lastGOGC.Store(int64(cfg.GOGCPercent))

	if cfg.GOMAXPROCS > 0 {
		runtime.GOMAXPROCS(cfg.GOMAXPROCS)
	} else {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	if os.Getenv("GOGC") == "" && cfg.GOGCPercent >= 0 {
		debug.SetGCPercent(cfg.GOGCPercent)
	}

	var memLimit int64
	if os.Getenv("GOMEMLIMIT") == "" {
		memLimit = memoryLimitFromPercent(cfg.MemoryLimitPercent)
		if memLimit > 0 {
			debug.SetMemoryLimit(memLimit)
		}
	}

	if cfg.BeaverEnabled {
		rt.middleware = newBeaverMiddleware(cfg.BeaverPoolSize)
	}

	if cfg.AdaptiveGC {
		var runCtx context.Context
		runCtx, rt.cancel = context.WithCancel(ctx)
		rt.wg.Add(1)
		go rt.adaptiveLoop(runCtx)
	}

	activeTuning.Store(rt)

	log.Info().
		Int("gomaxprocs", runtime.GOMAXPROCS(0)).
		Int("gogc", cfg.GOGCPercent).
		Int("memlimit_percent", cfg.MemoryLimitPercent).
		Int64("memlimit_bytes", memLimit).
		Bool("adaptive_gc", cfg.AdaptiveGC).
		Bool("beaver_enabled", cfg.BeaverEnabled).
		Int("beaver_pool_size", cfg.BeaverPoolSize).
		Str("beaver_allocator", cfg.BeaverAllocator).
		Msg("runtime tuning initialized")

	return rt, nil
}

// Close stops the adaptive controller. It does not close the beaver pool because
// pooled allocators may still be in use by in-flight HTTP handlers.
func (rt *RuntimeTuning) Close() error {
	if rt == nil {
		return nil
	}
	if rt.cancel != nil {
		rt.cancel()
		rt.wg.Wait()
	}
	return nil
}

// HTTPMiddleware returns beaver allocator middleware when enabled, otherwise a no-op.
func (rt *RuntimeTuning) HTTPMiddleware() func(http.Handler) http.Handler {
	if rt == nil || rt.middleware == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return rt.middleware
}

func (rt *RuntimeTuning) adaptiveLoop(ctx context.Context) {
	defer rt.wg.Done()

	ticker := time.NewTicker(rt.cfg.AdaptiveInterval)
	defer ticker.Stop()

	var lastStats runtime.MemStats
	runtime.ReadMemStats(&lastStats)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rt.adjustGC(&lastStats)
		}
	}
}

func (rt *RuntimeTuning) adjustGC(lastStats *runtime.MemStats) {
	limit := memoryLimitFromPercent(rt.cfg.MemoryLimitPercent)
	if limit <= 0 {
		return
	}

	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)

	targetHeap := uint64(float64(limit) * defaultTargetHeapRatio)
	if targetHeap == 0 {
		return
	}

	var allocDelta uint64
	if stats.TotalAlloc > lastStats.TotalAlloc {
		allocDelta = stats.TotalAlloc - lastStats.TotalAlloc
	}
	*lastStats = stats

	currentHeap := stats.HeapAlloc
	lastGOGC := int(rt.lastGOGC.Load())
	newGOGC := lastGOGC

	switch {
	case currentHeap > targetHeap:
		newGOGC -= defaultAdaptiveGOCStep
		if newGOGC < defaultAdaptiveGOCMin {
			newGOGC = defaultAdaptiveGOCMin
		}
	case currentHeap < targetHeap/2 && allocDelta < targetHeap/8:
		newGOGC += defaultAdaptiveGOCStep
		if newGOGC > defaultAdaptiveGOCMax {
			newGOGC = defaultAdaptiveGOCMax
		}
	}

	if newGOGC != lastGOGC {
		debug.SetGCPercent(newGOGC)
		rt.lastGOGC.Store(int64(newGOGC))
		log.Debug().
			Int("gogc", newGOGC).
			Uint64("heap_alloc", currentHeap).
			Uint64("target_heap", targetHeap).
			Uint64("alloc_delta", allocDelta).
			Msg("adaptive gc adjusted")
	}
}

func memoryLimitFromPercent(percent int) int64 {
	total := systemMemory()
	if total <= 0 {
		return 0
	}
	return int64(float64(total) * float64(percent) / 100.0)
}

const maxCgroupLimit = 1 << 60

func readIntFile(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
