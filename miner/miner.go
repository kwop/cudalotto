package miner

import (
	"encoding/binary"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/arlequin/cudalotto/cuda"
	"github.com/arlequin/cudalotto/internal"
	"github.com/arlequin/cudalotto/stratum"
)

const (
	BatchSize = 1 << 24 // ~16.7M nonces per kernel launch
)

// Miner orchestrates GPU mining.
type Miner struct {
	client    *stratum.Client
	batchSize uint32
}

// New creates a new Miner.
func New(client *stratum.Client, batchSize uint32) *Miner {
	if batchSize == 0 {
		batchSize = BatchSize
	}
	return &Miner{
		client:    client,
		batchSize: batchSize,
	}
}

// Run is the main mining loop. It reads jobs from jobChan and mines on the GPU.
func (m *Miner) Run(jobChan <-chan stratum.Job, quit <-chan struct{}) {
	var currentJob *stratum.Job
	var header [80]byte
	var midstate [8]uint32
	var tail [4]uint32
	var nonce uint32
	var extranonce2 uint64
	var hashCount uint64
	var lastReport time.Time

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
				log.Printf("[miner] extranonce2 rolled to %d", extranonce2)
				continue
			}

			target := internal.DifficultyToTarget(m.client.Difficulty())

			found, err := cuda.Scan(midstate, tail, nonce, rangeSize, target)
			if err != nil {
				log.Printf("[miner] CUDA error: %v", err)
				time.Sleep(time.Second)
				continue
			}

			hashCount += uint64(rangeSize)
			nonce += rangeSize

			// Report hashrate every 10 seconds
			if time.Since(lastReport) >= 10*time.Second {
				elapsed := time.Since(lastReport).Seconds()
				hps := float64(hashCount) / elapsed
				log.Printf("[miner] %s | total: %d hashes", internal.FormatHashrate(hps), hashCount)
				hashCount = 0
				lastReport = time.Now()
			}

			// Submit found nonces
			for _, n := range found {
				en2Hex := fmt.Sprintf("%0*x", m.client.ExtraNonce2Size*2, extranonce2)
				nonceHex := fmt.Sprintf("%08x", n)
				log.Printf("[miner] *** SHARE FOUND *** nonce=%s", nonceHex)

				if err := m.client.Submit(
					currentJob.ID,
					en2Hex,
					currentJob.NTime,
					nonceHex,
				); err != nil {
					log.Printf("[miner] submit error: %v", err)
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

