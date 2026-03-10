package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/kwop/cudalotto/cuda"
	"github.com/kwop/cudalotto/miner"
	"github.com/kwop/cudalotto/stats"
	"github.com/kwop/cudalotto/stratum"
	"github.com/kwop/cudalotto/tui"
)

// loadEnv reads a .env file and sets environment variables (does not override existing).
func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}

// envDefault returns the environment variable value or a fallback.
func envDefault(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\n[cudalotto] PANIC: %v\n[cudalotto] stack trace:\n%s\n", r, debug.Stack())
			os.Exit(2)
		}
	}()

	// Load .env (won't override existing env vars or CLI flags)
	loadEnv(".env")

	pool := flag.String("pool", envDefault("POOL", "stratum+tcp://eusolo.ckpool.org:3333"), "Stratum pool URL")
	user := flag.String("user", envDefault("BTC_ADDRESS", ""), "Bitcoin address (required)")
	pass := flag.String("pass", "x", "Pool password")
	device := flag.Int("device", 0, "CUDA device ID")
	worker := flag.String("worker", envDefault("WORKER", "cudalotto"), "Worker name suffix")
	batch := flag.Uint("batch", 1<<24, "Nonces per kernel launch")
	threads := flag.Int("threads", 256, "Threads per block (power of 2: 32-1024)")
	tuiMode := flag.Bool("tui", false, "Enable terminal UI dashboard")
	monitor := flag.String("monitor", "", "Monitor a running service (e.g. -monitor localhost:7777)")
	httpAddr := flag.String("http", "127.0.0.1:7777", "HTTP stats endpoint address")
	bench := flag.Bool("bench", false, "Run GPU benchmark (no pool connection needed)")
	flag.Parse()

	// Benchmark mode
	if *bench {
		runBenchmark(*device, *threads, *batch)
		return
	}

	// Monitor mode: connect to a running service and display TUI
	if *monitor != "" {
		runMonitor(*monitor)
		return
	}

	if *user == "" {
		log.Fatal("Usage: cudalotto -user <BTC_ADDRESS> [-pool URL] [-device N] [-tui]\n       Or set BTC_ADDRESS in .env\n       Or: cudalotto -monitor localhost:7777")
	}

	fullUser := *user + "." + *worker

	// Initialize stats
	poolDisplay := strings.TrimPrefix(*pool, "stratum+tcp://")
	poolDisplay = strings.TrimPrefix(poolDisplay, "tcp://")
	st := stats.New(poolDisplay)

	// Capture logs: TUI mode = stats only, otherwise = stderr + stats
	if *tuiMode {
		log.SetOutput(st)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, st))
	}
	log.SetFlags(log.Ltime)

	log.Printf("[cudalotto] Bitcoin Solo Miner for Jetson (GPU)")
	log.Printf("[cudalotto] Pool: %s", *pool)
	log.Printf("[cudalotto] User: %s", fullUser)

	// Initialize CUDA
	if err := cuda.Init(*device); err != nil {
		log.Fatalf("[cudalotto] %v", err)
	}
	defer cuda.Cleanup()
	cuda.SetBlockSize(*threads)

	// Connect to pool with retry
	client := stratum.NewClient(*pool, fullUser, *pass)
	client.SetStats(st)
	for {
		if err := client.Connect(); err != nil {
			log.Printf("[cudalotto] connect failed: %v, retrying in 10s...", err)
			time.Sleep(10 * time.Second)
			continue
		}
		break
	}
	defer client.Close()

	if err := client.Subscribe(); err != nil {
		log.Fatalf("[cudalotto] %v", err)
	}

	if err := client.Authorize(); err != nil {
		log.Fatalf("[cudalotto] %v", err)
	}

	// Channels
	jobChan := make(chan stratum.Job, 2)
	quit := make(chan struct{})

	// Start stratum listener
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[cudalotto] PANIC in stratum listener: %v\n%s", r, debug.Stack())
			}
		}()
		for {
			err := client.Listen(jobChan)
			log.Printf("[cudalotto] stratum disconnected: %v", err)
			select {
			case <-quit:
				return
			default:
			}

			// Reconnect
			for {
				log.Printf("[cudalotto] reconnecting in 10s...")
				time.Sleep(10 * time.Second)
				select {
				case <-quit:
					return
				default:
				}

				client.Close()
				newClient := stratum.NewClient(*pool, fullUser, *pass)
				newClient.SetStats(st)
				if err := newClient.Connect(); err != nil {
					log.Printf("[cudalotto] reconnect failed: %v", err)
					continue
				}
				if err := newClient.Subscribe(); err != nil {
					log.Printf("[cudalotto] resubscribe failed: %v", err)
					newClient.Close()
					continue
				}
				if err := newClient.Authorize(); err != nil {
					log.Printf("[cudalotto] reauthorize failed: %v", err)
					newClient.Close()
					continue
				}
				st.Reconnections.Add(1)
				*client = *newClient
				break
			}
		}
	}()

	// Start HTTP stats endpoint
	if *httpAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(st.Snapshot())
		})
		go http.ListenAndServe(*httpAddr, mux)
		log.Printf("[cudalotto] stats endpoint: http://%s/stats", *httpAddr)
	}

	// Start miner
	m := miner.New(client, uint32(*batch), st)
	go m.Run(jobChan, quit)

	// Signal handling
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	if *tuiMode {
		// TUI blocks main goroutine, signal closes quit
		go func() {
			s := <-sig
			log.Printf("[cudalotto] received %v, shutting down...", s)
			close(quit)
		}()
		tui.Run(st, quit)
	} else {
		s := <-sig
		log.Printf("[cudalotto] received %v, shutting down...", s)
		close(quit)
	}
}

// runBenchmark tests different GPU configurations and reports hashrate.
func runBenchmark(device, defaultThreads int, defaultBatch uint) {
	fmt.Println("=== cudalotto GPU Benchmark ===")
	fmt.Println()

	if err := cuda.Init(device); err != nil {
		log.Fatalf("[bench] %v", err)
	}
	defer cuda.Cleanup()

	// Dummy mining data (realistic SHA256d workload)
	midstate := [8]uint32{0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
		0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19}
	tail := [4]uint32{0x12345678, 0x9abcdef0, 0x11223344, 0x00000000}
	// Very high target (almost all hashes pass) to measure raw speed
	target := [8]uint32{0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF,
		0xFFFFFFFF, 0xFFFFFFFF, 0xFFFFFFFF, 0x0000FFFF}

	type config struct {
		threads int
		batch   uint
		label   string
	}

	configs := []config{
		{128, 1 << 23, "threads=128 batch=2^23 (8M)"},
		{128, 1 << 24, "threads=128 batch=2^24 (16M)"},
		{128, 1 << 25, "threads=128 batch=2^25 (33M)"},
		{128, 1 << 26, "threads=128 batch=2^26 (67M)"},
		{256, 1 << 23, "threads=256 batch=2^23 (8M)"},
		{256, 1 << 24, "threads=256 batch=2^24 (16M) [CURRENT]"},
		{256, 1 << 25, "threads=256 batch=2^25 (33M)"},
		{256, 1 << 26, "threads=256 batch=2^26 (67M)"},
		{256, 1 << 27, "threads=256 batch=2^27 (134M)"},
		{512, 1 << 24, "threads=512 batch=2^24 (16M)"},
		{512, 1 << 25, "threads=512 batch=2^25 (33M)"},
		{512, 1 << 26, "threads=512 batch=2^26 (67M)"},
		{64, 1 << 24, "threads=64  batch=2^24 (16M)"},
		{64, 1 << 25, "threads=64  batch=2^25 (33M)"},
	}

	fmt.Printf("%-45s %12s %12s\n", "Configuration", "MH/s", "Time/batch")
	fmt.Println(strings.Repeat("-", 72))

	var bestRate float64
	var bestLabel string

	for _, c := range configs {
		cuda.SetBlockSize(c.threads)

		batchSize := uint32(c.batch)

		// Warmup
		cuda.Scan(midstate, tail, 0, batchSize, target)

		// Benchmark: 3 iterations
		iterations := 3
		start := time.Now()
		for i := 0; i < iterations; i++ {
			cuda.Scan(midstate, tail, uint32(uint64(i)*uint64(batchSize)), batchSize, target)
		}
		elapsed := time.Since(start)

		totalHashes := uint64(batchSize) * uint64(iterations)
		hashrate := float64(totalHashes) / elapsed.Seconds()
		perBatch := elapsed / time.Duration(iterations)

		fmt.Printf("%-45s %9.2f MH/s %10s\n", c.label, hashrate/1e6, perBatch.Round(time.Millisecond))

		if hashrate > bestRate {
			bestRate = hashrate
			bestLabel = c.label
		}
	}

	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("BEST: %-39s %9.2f MH/s\n", bestLabel, bestRate/1e6)
	fmt.Println()
	fmt.Println("To apply the best config, restart with the matching -threads and -batch flags.")
}

// runMonitor connects to a running cudalotto service and displays the TUI.
func runMonitor(addr string) {
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	url := addr + "/stats"

	st := stats.New("")
	quit := make(chan struct{})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		close(quit)
	}()

	// Fetch stats in background
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				resp, err := client.Get(url)
				if err != nil {
					st.SetConnected(false)
					continue
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				st.LoadSnapshot(body)
			}
		}
	}()

	fmt.Fprintf(os.Stderr, "Connecting to %s...\n", url)
	// Initial fetch before starting TUI
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot connect to %s\nMake sure the miner is running with -http flag\n", url)
		os.Exit(1)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	st.LoadSnapshot(body)

	tui.Run(st, quit)
}
