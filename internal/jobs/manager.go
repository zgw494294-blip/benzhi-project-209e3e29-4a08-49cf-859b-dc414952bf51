package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"thermoguard/internal/app"
)

type Manager struct {
	service      *app.Service
	queue        chan string
	mu           sync.Mutex
	pending      map[string]pendingTask
	wg           sync.WaitGroup
	alive        atomic.Bool
	lastError    atomic.Value
	scanInterval time.Duration
}

type pendingTask struct {
	attempt     int
	nextAttempt time.Time
	lastError   string
}

func New(service *app.Service, queueSize int, scanInterval time.Duration) *Manager {
	if queueSize <= 0 {
		queueSize = 128
	}
	if scanInterval <= 0 {
		scanInterval = time.Minute
	}
	return &Manager{service: service, queue: make(chan string, queueSize), pending: map[string]pendingTask{}, scanInterval: scanInterval}
}
func (m *Manager) Start(ctx context.Context) {
	if m.alive.Swap(true) {
		return
	}
	m.wg.Add(2)
	go m.evaluateLoop(ctx)
	go m.overdueLoop(ctx)
}
func (m *Manager) Healthy() bool { return m.alive.Load() && m.LastError() == "" }
func (m *Manager) LastError() string {
	v := m.lastError.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}
func (m *Manager) Enqueue(lotID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.pending[lotID]; exists {
		return true
	}
	select {
	case m.queue <- lotID:
		m.pending[lotID] = pendingTask{}
		return true
	default:
		m.lastError.Store("偏差评估队列已满")
		return false
	}
}
func (m *Manager) evaluateLoop(ctx context.Context) {
	defer m.wg.Done()
	retryInterval := 250 * time.Millisecond
	if m.scanInterval < retryInterval {
		retryInterval = m.scanInterval
	}
	retryTicker := time.NewTicker(retryInterval)
	defer retryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.drainPending()
			return
		case lotID := <-m.queue:
			m.evaluate(lotID, time.Now())
		case now := <-retryTicker.C:
			for _, lotID := range m.dueRetries(now) {
				m.evaluate(lotID, now)
			}
		}
	}
}

func (m *Manager) evaluate(lotID string, now time.Time) {
	_, err := m.service.Evaluate("system:evaluator", lotID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.pending, lotID)
		m.refreshLastErrorLocked()
		return
	}
	task := m.pending[lotID]
	task.attempt++
	delay := 250 * time.Millisecond
	for i := 1; i < task.attempt && i < 5; i++ {
		delay *= 2
	}
	task.nextAttempt = now.Add(delay)
	task.lastError = err.Error()
	m.pending[lotID] = task
	m.lastError.Store(err.Error())
}

func (m *Manager) dueRetries(now time.Time) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, 0)
	for lotID, task := range m.pending {
		if task.lastError != "" && !task.nextAttempt.After(now) {
			result = append(result, lotID)
		}
	}
	return result
}

func (m *Manager) drainPending() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.pending))
	for lotID := range m.pending {
		ids = append(ids, lotID)
	}
	m.mu.Unlock()
	for _, lotID := range ids {
		m.evaluate(lotID, time.Now())
	}
}

func (m *Manager) refreshLastErrorLocked() {
	for _, task := range m.pending {
		if task.lastError != "" {
			m.lastError.Store(task.lastError)
			return
		}
	}
	m.lastError.Store("")
}
func (m *Manager) overdueLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, overdueErr := m.service.ScanOverdue("system:overdue-scanner")
			pendingLots, pendingErr := m.service.PendingEvaluationLotIDs()
			if overdueErr != nil {
				m.lastError.Store(overdueErr.Error())
			} else if pendingErr != nil {
				m.lastError.Store(pendingErr.Error())
			} else {
				for _, lotID := range pendingLots {
					_ = m.Enqueue(lotID)
				}
				m.mu.Lock()
				m.refreshLastErrorLocked()
				m.mu.Unlock()
			}
		}
	}
}
func (m *Manager) Wait() { m.wg.Wait(); m.alive.Store(false) }
