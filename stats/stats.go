package stats

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxHistory       = 120
	maxLogs          = 100
	maxErrorHistory  = 50
)

// Stats holds mining statistics shared between miner, stratum, and TUI.
type Stats struct {
	StartTime      time.Time
	TotalHashes    atomic.Uint64
	SharesSent     atomic.Uint32
	SharesAccepted atomic.Uint32
	SharesRejected atomic.Uint32
	SharesErrors   atomic.Uint32
	JobsReceived   atomic.Uint32
	Reconnections  atomic.Uint32

	mu              sync.RWMutex
	currentHashrate float64
	hashrateHistory []float64
	currentJobID    string
	difficulty      float64
	extranonce2     uint64
	connected       bool
	poolAddr        string
	remoteUptime    string // set by LoadSnapshot in monitor mode

	logMu    sync.Mutex
	logLines []string

	errorMu     sync.Mutex
	errorEvents []ErrorEvent
}

// ErrorEvent records a share error with timestamp and details.
type ErrorEvent struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// New creates a new Stats instance.
func New(poolAddr string) *Stats {
	return &Stats{
		StartTime:       time.Now(),
		hashrateHistory: make([]float64, 0, maxHistory),
		poolAddr:        poolAddr,
	}
}

func (s *Stats) SetHashrate(hps float64) {
	s.mu.Lock()
	s.currentHashrate = hps
	s.hashrateHistory = append(s.hashrateHistory, hps)
	if len(s.hashrateHistory) > maxHistory {
		s.hashrateHistory = s.hashrateHistory[1:]
	}
	s.mu.Unlock()
}

func (s *Stats) Hashrate() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentHashrate
}

func (s *Stats) HashrateHistory() []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]float64, len(s.hashrateHistory))
	copy(out, s.hashrateHistory)
	return out
}

func (s *Stats) SetJobID(id string) {
	s.mu.Lock()
	s.currentJobID = id
	s.mu.Unlock()
}

func (s *Stats) JobID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentJobID
}

func (s *Stats) SetDifficulty(d float64) {
	s.mu.Lock()
	s.difficulty = d
	s.mu.Unlock()
}

func (s *Stats) Difficulty() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.difficulty
}

func (s *Stats) SetExtranonce2(n uint64) {
	s.mu.Lock()
	s.extranonce2 = n
	s.mu.Unlock()
}

func (s *Stats) Extranonce2() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.extranonce2
}

func (s *Stats) SetConnected(b bool) {
	s.mu.Lock()
	s.connected = b
	s.mu.Unlock()
}

func (s *Stats) Connected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected
}

func (s *Stats) PoolAddr() string {
	return s.poolAddr
}

// AddError increments the error counter and records the event for history.
func (s *Stats) AddError(msg string) {
	s.SharesErrors.Add(1)
	s.errorMu.Lock()
	defer s.errorMu.Unlock()
	s.errorEvents = append(s.errorEvents, ErrorEvent{
		Time:    time.Now(),
		Message: msg,
	})
	if len(s.errorEvents) > maxErrorHistory {
		s.errorEvents = s.errorEvents[1:]
	}
}

// ErrorEvents returns a copy of the error history.
func (s *Stats) ErrorEvents() []ErrorEvent {
	s.errorMu.Lock()
	defer s.errorMu.Unlock()
	out := make([]ErrorEvent, len(s.errorEvents))
	copy(out, s.errorEvents)
	return out
}

// Write implements io.Writer for log capture.
func (s *Stats) Write(p []byte) (int, error) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		s.logLines = append(s.logLines, line)
		if len(s.logLines) > maxLogs {
			s.logLines = s.logLines[1:]
		}
	}
	return len(p), nil
}

// LogLines returns the last n log lines.
func (s *Stats) LogLines(n int) []string {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if n > len(s.logLines) {
		n = len(s.logLines)
	}
	out := make([]string, n)
	copy(out, s.logLines[len(s.logLines)-n:])
	return out
}

func (s *Stats) Uptime() time.Duration {
	return time.Since(s.StartTime)
}

func (s *Stats) FormatUptime() string {
	s.mu.RLock()
	ru := s.remoteUptime
	s.mu.RUnlock()
	if ru != "" {
		return ru
	}
	d := s.Uptime()
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	return fmt.Sprintf("%dh%02dm%02ds", h, m, sec)
}

// Snapshot is a JSON-serializable snapshot of stats.
type Snapshot struct {
	Uptime          string    `json:"uptime"`
	Hashrate        float64   `json:"hashrate"`
	TotalHashes     uint64    `json:"total_hashes"`
	SharesSent      uint32    `json:"shares_sent"`
	SharesAccepted  uint32    `json:"shares_accepted"`
	SharesRejected  uint32    `json:"shares_rejected"`
	SharesErrors    uint32    `json:"shares_errors"`
	JobsReceived    uint32    `json:"jobs_received"`
	Reconnections   uint32    `json:"reconnections"`
	Difficulty      float64   `json:"difficulty"`
	JobID           string    `json:"job_id"`
	Extranonce2     uint64    `json:"extranonce2"`
	Connected       bool      `json:"connected"`
	Pool            string    `json:"pool"`
	HashrateHistory []float64    `json:"hashrate_history"`
	Logs            []string     `json:"logs"`
	ErrorEvents     []ErrorEvent `json:"error_events,omitempty"`
}

// Snapshot returns a JSON-serializable snapshot.
func (s *Stats) Snapshot() Snapshot {
	return Snapshot{
		Uptime:          s.FormatUptime(),
		Hashrate:        s.Hashrate(),
		TotalHashes:     s.TotalHashes.Load(),
		SharesSent:      s.SharesSent.Load(),
		SharesAccepted:  s.SharesAccepted.Load(),
		SharesRejected:  s.SharesRejected.Load(),
		SharesErrors:    s.SharesErrors.Load(),
		JobsReceived:    s.JobsReceived.Load(),
		Reconnections:   s.Reconnections.Load(),
		Difficulty:      s.Difficulty(),
		JobID:           s.JobID(),
		Extranonce2:     s.Extranonce2(),
		Connected:       s.Connected(),
		Pool:            s.PoolAddr(),
		HashrateHistory: s.HashrateHistory(),
		Logs:            s.LogLines(maxLogs),
		ErrorEvents:     s.ErrorEvents(),
	}
}

// LoadSnapshot updates stats from a remote snapshot (for monitor mode).
func (s *Stats) LoadSnapshot(data []byte) error {
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return err
	}
	s.SetHashrate(snap.Hashrate)
	s.TotalHashes.Store(snap.TotalHashes)
	s.SharesSent.Store(snap.SharesSent)
	s.SharesAccepted.Store(snap.SharesAccepted)
	s.SharesRejected.Store(snap.SharesRejected)
	s.SharesErrors.Store(snap.SharesErrors)
	s.JobsReceived.Store(snap.JobsReceived)
	s.Reconnections.Store(snap.Reconnections)
	s.SetDifficulty(snap.Difficulty)
	s.SetJobID(snap.JobID)
	s.SetExtranonce2(snap.Extranonce2)
	s.SetConnected(snap.Connected)

	// Replace hashrate history and remote uptime
	s.mu.Lock()
	s.hashrateHistory = snap.HashrateHistory
	s.poolAddr = snap.Pool
	s.remoteUptime = snap.Uptime
	s.mu.Unlock()

	// Replace logs
	s.logMu.Lock()
	s.logLines = snap.Logs
	s.logMu.Unlock()

	// Replace error events
	s.errorMu.Lock()
	s.errorEvents = snap.ErrorEvents
	s.errorMu.Unlock()

	return nil
}
