package clock

import (
	"sync"
	"time"
)

type Clock interface{ Now() time.Time }

type Real struct{}

func (Real) Now() time.Time { return time.Now().UTC() }

type Fixed struct {
	mu  sync.RWMutex
	now time.Time
}

func NewFixed(t time.Time) *Fixed    { return &Fixed{now: t.UTC()} }
func (f *Fixed) Now() time.Time      { f.mu.RLock(); defer f.mu.RUnlock(); return f.now }
func (f *Fixed) Set(t time.Time)     { f.mu.Lock(); f.now = t.UTC(); f.mu.Unlock() }
func (f *Fixed) Add(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }
