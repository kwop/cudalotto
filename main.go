package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arlequin/cudalotto/cuda"
	"github.com/arlequin/cudalotto/miner"
	"github.com/arlequin/cudalotto/stratum"
)

func main() {
	pool := flag.String("pool", "stratum+tcp://eusolo.ckpool.org:3333", "Stratum pool URL")
	user := flag.String("user", "", "Bitcoin address (required)")
	pass := flag.String("pass", "x", "Pool password")
	device := flag.Int("device", 0, "CUDA device ID")
	worker := flag.String("worker", "cudalotto", "Worker name suffix")
	batch := flag.Uint("batch", 1<<24, "Nonces per kernel launch")
	flag.Parse()

	if *user == "" {
		log.Fatal("Usage: cudalotto -user <BTC_ADDRESS> [-pool URL] [-device N]")
	}

	fullUser := *user + "." + *worker

	log.Printf("[cudalotto] Bitcoin Solo Miner for Jetson (GPU)")
	log.Printf("[cudalotto] Pool: %s", *pool)
	log.Printf("[cudalotto] User: %s", fullUser)

	// Initialize CUDA
	if err := cuda.Init(*device); err != nil {
		log.Fatalf("[cudalotto] %v", err)
	}
	defer cuda.Cleanup()

	// Connect to pool with retry
	client := stratum.NewClient(*pool, fullUser, *pass)
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
				*client = *newClient
				break
			}
		}
	}()

	// Start miner
	m := miner.New(client, uint32(*batch))
	go m.Run(jobChan, quit)

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	log.Printf("[cudalotto] received %v, shutting down...", s)
	close(quit)
}
