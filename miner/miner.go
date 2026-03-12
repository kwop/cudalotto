package miner

import (
	"encoding/binary"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/kwop/cudalotto/cuda"
	"github.com/kwop/cudalotto/internal"
	"github.com/kwop/cudalotto/stats"
	"github.com/kwop/cudalotto/stratum"
)

const (
	BatchSize = 1 << 24 // ~16.7M nonces per kernel launch
)

const (
	maxPendingShares = 32
	maxShareAge      = 2 * time.Minute
	maxShareRetries  = 3
)

type pendingShare struct {
	jobID       string
	extranonce2 string
	ntime       string
	nonce       string
	attempts    int
	firstTry    time.Time
}

// Miner orchestrates GPU mining.
type Miner struct {
	client    *stratum.Client
	batchSize uint32
	stats     *stats.Stats
	pendingMu sync.Mutex
	pending   []pendingShare
}

// New creates a new Miner.
func New(client *stratum.Client, batchSize uint32, st *stats.Stats) *Miner {
	if batchSize == 0 {
		batchSize = BatchSize
	}
	return &Miner{
		client:    client,
		batchSize: batchSize,
		stats:     st,
	}
}

func (m *Miner) bufferShare(jobID, extranonce2, ntime, nonce string) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()

	if len(m.pending) >= maxPendingShares {
		log.Printf("[miner] share buffer full, dropping oldest share (job=%s nonce=%s)", m.pending[0].jobID, m.pending[0].nonce)
		m.pending = m.pending[1:]
	}

	m.pending = append(m.pending, pendingShare{
		jobID:       jobID,
		extranonce2: extranonce2,
		ntime:       ntime,
		nonce:       nonce,
		firstTry:    time.Now(),
	})
	log.Printf("[miner] share buffered for retry (job=%s nonce=%s, %d pending)", jobID, nonce, len(m.pending))
}

// FlushPending drops all buffered shares and records errors.
// Called on reconnection because ExtraNonce1 changes, making old shares invalid.
func (m *Miner) FlushPending() {
	m.pendingMu.Lock()
	shares := m.pending
	m.pending = nil
	m.pendingMu.Unlock()

	for _, s := range shares {
		msg := fmt.Sprintf("share invalidated by reconnection (job=%s nonce=%s)", s.jobID, s.nonce)
		log.Printf("[miner] %s", msg)
		if m.stats != nil {
			m.stats.AddError(msg)
		}
	}
}

func (m *Miner) retryPending() {
	m.pendingMu.Lock()
	if len(m.pending) == 0 {
		m.pendingMu.Unlock()
		return
	}

	if !m.client.IsConnected() || m.client.IsStale() {
		m.pendingMu.Unlock()
		return
	}

	// Take a copy and release the lock
	shares := make([]pendingShare, len(m.pending))
	copy(shares, m.pending)
	m.pending = m.pending[:0]
	m.pendingMu.Unlock()

	for _, s := range shares {
		if time.Since(s.firstTry) > maxShareAge {
			msg := fmt.Sprintf("share expired after %v (job=%s nonce=%s)", time.Since(s.firstTry).Round(time.Second), s.jobID, s.nonce)
			log.Printf("[miner] %s", msg)
			if m.stats != nil {
				m.stats.AddError(msg)
			}
			continue
		}
		if s.attempts >= maxShareRetries {
			msg := fmt.Sprintf("share dropped after %d retries (job=%s nonce=%s)", s.attempts, s.jobID, s.nonce)
			log.Printf("[miner] %s", msg)
			if m.stats != nil {
				m.stats.AddError(msg)
			}
			continue
		}

		log.Printf("[miner] retrying share (job=%s nonce=%s attempt=%d)", s.jobID, s.nonce, s.attempts+1)
		if err := m.client.Submit(s.jobID, s.extranonce2, s.ntime, s.nonce); err != nil {
			log.Printf("[miner] retry failed: %v", err)
			s.attempts++
			m.pendingMu.Lock()
			m.pending = append(m.pending, s)
			m.pendingMu.Unlock()
		}
	}
}

// Run is the main mining loop. It reads jobs from jobChan and mines on the GPU.
func (m *Miner) Run(jobChan <-chan stratum.Job, quit <-chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[miner] PANIC: %v\n%s", r, debug.Stack())
		}
	}()
	var currentJob *stratum.Job
	var header [80]byte
	var midstate [8]uint32
	var tail [4]uint32
	var nonce uint32
	var extranonce2 uint64
	var hashCount uint64
	var lastReport time.Time
	var cachedDiff float64
	var cachedTarget [8]uint32

	for {
		// Wait for first job or check for new jobs between batches
		select {
		case <-quit:
			return
		case job := <-jobChan:
			currentJob = &job
			nonce = 0
			extranonce2 = 0
			header, midstate, tail = m.buildWork(job, extranonce2)
			_ = header // used for debugging if needed
			lastReport = time.Now()
			hashCount = 0
			log.Printf("[miner] mining job %s", job.ID)
		default:
			if currentJob == nil {
				// No job yet, wait
				select {
				case <-quit:
					return
				case job := <-jobChan:
					currentJob = &job
					nonce = 0
					extranonce2 = 0
					header, midstate, tail = m.buildWork(job, extranonce2)
					_ = header
					lastReport = time.Now()
					hashCount = 0
					log.Printf("[miner] mining job %s", job.ID)
				}
				continue
			}
		}

		// Mine current job in batches
		for currentJob != nil {
			// Check for new job (non-blocking)
			select {
			case <-quit:
				return
			case job := <-jobChan:
				currentJob = &job
				nonce = 0
				extranonce2 = 0
				header, midstate, tail = m.buildWork(job, extranonce2)
				_ = header
				hashCount = 0
				lastReport = time.Now()
				log.Printf("[miner] new job %s", job.ID)
			default:
			}

			// Determine range for this batch
			rangeSize := m.batchSize
			if uint64(nonce)+uint64(rangeSize) > 0xFFFFFFFF {
				rangeSize = 0xFFFFFFFF - nonce
			}

			if rangeSize == 0 {
				// Nonce space exhausted, increment extranonce2
				extranonce2++
				nonce = 0
				header, midstate, tail = m.buildWork(*currentJob, extranonce2)
				_ = header
				if m.stats != nil {
					m.stats.SetExtranonce2(extranonce2)
				}
				log.Printf("[miner] extranonce2 rolled to %d", extranonce2)
				continue
			}

			diff := m.client.Difficulty()
			if diff != cachedDiff {
				cachedTarget = internal.DifficultyToTarget(diff)
				cachedDiff = diff
			}

			found, err := cuda.Scan(midstate, tail, nonce, rangeSize, cachedTarget)
			if err != nil {
				log.Printf("[miner] CUDA error: %v", err)
				time.Sleep(time.Second)
				continue
			}

			hashCount += uint64(rangeSize)
			nonce += rangeSize
			if m.stats != nil {
				m.stats.TotalHashes.Add(uint64(rangeSize))
			}

			// Report hashrate every 10 seconds
			if time.Since(lastReport) >= 10*time.Second {
				elapsed := time.Since(lastReport).Seconds()
				hps := float64(hashCount) / elapsed
				log.Printf("[miner] %s | total: %d hashes", internal.FormatHashrate(hps), hashCount)
				if m.stats != nil {
					m.stats.SetHashrate(hps)
				}
				hashCount = 0
				lastReport = time.Now()
			}

			// Retry any buffered shares
			m.retryPending()

			// Submit found nonces
			for _, n := range found {
				en2Hex := fmt.Sprintf("%0*x", m.client.ExtraNonce2Size*2, extranonce2)
				nonceHex := fmt.Sprintf("%08x", n)
				log.Printf("[miner] *** SHARE FOUND *** nonce=%s", nonceHex)
				if m.stats != nil {
					m.stats.SharesSent.Add(1)
				}

				if err := m.client.Submit(
					currentJob.ID,
					en2Hex,
					currentJob.NTime,
					nonceHex,
				); err != nil {
					msg := fmt.Sprintf("submit failed (job=%s nonce=%s): %v", currentJob.ID, nonceHex, err)
					log.Printf("[miner] %s — buffering for retry", msg)
					if m.stats != nil {
						m.stats.AddError(msg)
					}
					m.bufferShare(currentJob.ID, en2Hex, currentJob.NTime, nonceHex)
				}
			}
		}
	}
}

// buildWork constructs the 80-byte block header and computes midstate + tail.
func (m *Miner) buildWork(job stratum.Job, extranonce2 uint64) ([80]byte, [8]uint32, [4]uint32) {
	// Build coinbase transaction
	en2Hex := fmt.Sprintf("%0*x", m.client.ExtraNonce2Size*2, extranonce2)
	coinbaseHex := job.Coinbase1 + m.client.ExtraNonce1 + en2Hex + job.Coinbase2
	coinbaseBytes, _ := internal.HexToBytes(coinbaseHex)

	// Double SHA256 the coinbase
	merkleRoot := internal.DoubleSHA256(coinbaseBytes)

	// Build merkle root by hashing up the tree
	for _, branchHex := range job.MerkleBranches {
		branch, _ := internal.HexToBytes(branchHex)
		combined := append(merkleRoot[:], branch...)
		merkleRoot = internal.DoubleSHA256(combined)
	}

	// Assemble 80-byte block header
	var header [80]byte

	// Version (4 bytes, little-endian in header)
	versionBytes, _ := internal.HexToBytes(job.Version)
	versionBytes = internal.SwapEndian32(versionBytes)
	copy(header[0:4], versionBytes)

	// PrevHash (32 bytes) — stratum sends it as 8 x 4-byte words, each word in LE
	prevHashBytes, _ := internal.HexToBytes(job.PrevHash)
	prevHashSwapped := internal.SwapEndian32(prevHashBytes)
	copy(header[4:36], prevHashSwapped)

	// Merkle root (32 bytes)
	copy(header[36:68], merkleRoot[:])

	// NTime (4 bytes)
	ntimeBytes, _ := internal.HexToBytes(job.NTime)
	ntimeBytes = internal.SwapEndian32(ntimeBytes)
	copy(header[68:72], ntimeBytes)

	// NBits (4 bytes)
	nbitsBytes, _ := internal.HexToBytes(job.NBits)
	nbitsBytes = internal.SwapEndian32(nbitsBytes)
	copy(header[72:76], nbitsBytes)

	// Nonce placeholder (4 bytes) — will be set by GPU
	// header[76:80] = 0x00000000

	// Compute midstate from first 64 bytes
	var data16 [16]uint32
	for i := 0; i < 16; i++ {
		data16[i] = binary.LittleEndian.Uint32(header[i*4 : i*4+4])
	}
	midstate := cuda.Midstate(data16)

	// Tail: bytes 64-79 as big-endian uint32 (for the CUDA kernel)
	var tail [4]uint32
	for i := 0; i < 4; i++ {
		tail[i] = internal.Bswap32(binary.LittleEndian.Uint32(header[64+i*4 : 68+i*4]))
	}

	if strings.Contains(job.ID, "") {
		log.Printf("[miner] header built: version=%s prevhash=%s...%s merkle=%s...%s ntime=%s nbits=%s",
			job.Version,
			job.PrevHash[:8], job.PrevHash[len(job.PrevHash)-8:],
			internal.BytesToHex(merkleRoot[:4]), internal.BytesToHex(merkleRoot[28:]),
			job.NTime, job.NBits)
	}

	return header, midstate, tail
}

