# cudalotto

**Bitcoin solo miner for NVIDIA Jetson (GPU).** Minimal Go + CUDA implementation for "lottery mining" on ARM64 Tegra devices.

Built for Jetson Orin Nano Super (sm_87, 1024 CUDA cores), but adaptable to any Jetson with CUDA support.

## Why?

Solo mining Bitcoin on a Jetson is pure lottery — the odds of finding a block are ~1 in 200 million years. But the cost is near zero (~15W overnight), and the reward is 3.125 BTC (~$250k). It's a fun project and a great way to learn CUDA + Stratum protocol.

## Architecture

```
main.go ──► stratum/ (pool connection, JSON-RPC)
         ──► miner/   (job management, header assembly)
         ──► cuda/    (SHA256d GPU kernel via CGo)
```

- **~1200 lines of code** total
- **GPU-only mining** — minimal CPU usage (midstate computed once per job, GPU does the rest)
- **Stratum v1** client with automatic reconnection
- **Midstate optimization** — first 64 bytes of header hashed once on CPU, GPU only processes the remaining 16 bytes per nonce
- **TUI dashboard** — real-time hashrate sparkline, shares, stats
- **HTTP stats API** — JSON endpoint for remote monitoring
- **`.env` support** — auto-loads configuration, no flags needed

## Requirements

- NVIDIA Jetson (Orin Nano, Xavier NX, AGX, etc.) with JetPack/L4T
- CUDA Toolkit 12.x (auto-detected, or set `CUDA_PATH`)
- Go 1.21+
- Build dependencies: `gcc`, `g++`, `make`

## Build

```bash
git clone https://github.com/kwop/cudalotto.git
cd cudalotto
make
```

For a different CUDA path or GPU architecture:

```bash
make CUDA_PATH=/usr/local/cuda-12.2 ARCH="-gencode arch=compute_72,code=sm_72"
```

### Compatibility

#### Tested

| Device | GPU | SM | CUDA cores | Status |
|---|---|---|---|---|
| Jetson Orin Nano Super | Ampere iGPU | sm_87 | 1024 | **Tested** |

#### Should work (untested)

**Jetson (ARM64)**

| Device | GPU | SM | CUDA cores | ARCH flag |
|---|---|---|---|---|
| Jetson Orin NX 8GB | Ampere iGPU | sm_87 | 1024 | default |
| Jetson Orin NX 16GB | Ampere iGPU | sm_87 | 1024 | default |
| Jetson Orin Nano 4GB/8GB | Ampere iGPU | sm_87 | 512-1024 | default |
| Jetson AGX Orin 32GB/64GB | Ampere iGPU | sm_87 | 1792-2048 | default |
| Jetson AGX Xavier | Volta iGPU | sm_72 | 512 | `ARCH="-gencode arch=compute_72,code=sm_72"` |
| Jetson Xavier NX | Volta iGPU | sm_72 | 384 | `ARCH="-gencode arch=compute_72,code=sm_72"` |
| Jetson TX2 | Pascal iGPU | sm_62 | 256 | `ARCH="-gencode arch=compute_62,code=sm_62"` |
| Jetson Nano | Maxwell iGPU | sm_53 | 128 | `ARCH="-gencode arch=compute_53,code=sm_53"` |

**Desktop/Laptop GPUs (x86_64)** — should compile with the right ARCH flag:

| GPU family | SM | ARCH flag |
|---|---|---|
| RTX 4090/4080/4070/4060 (Ada) | sm_89 | `ARCH="-gencode arch=compute_89,code=sm_89"` |
| RTX 3090/3080/3070/3060 (Ampere) | sm_86 | `ARCH="-gencode arch=compute_86,code=sm_86"` |
| A100/A40 (Ampere) | sm_80 | `ARCH="-gencode arch=compute_80,code=sm_80"` |
| RTX 2080/2070/2060, GTX 1660 (Turing) | sm_75 | `ARCH="-gencode arch=compute_75,code=sm_75"` |
| V100 (Volta) | sm_70 | `ARCH="-gencode arch=compute_70,code=sm_70"` |
| GTX 1080/1070/1060 (Pascal) | sm_61 | `ARCH="-gencode arch=compute_61,code=sm_61"` |
| GTX 950/960/970/980 (Maxwell) | sm_52 | `ARCH="-gencode arch=compute_52,code=sm_52"` |

> **Note:** Desktop GPUs have not been tested. The Stratum client and SHA256d kernel are platform-independent, but the build system and CGo setup are optimized for Jetson/ARM64. Desktop builds may need minor Makefile adjustments for library paths.

## Usage

```bash
# With .env configured (reads BTC_ADDRESS, POOL, WORKER automatically)
./cudalotto

# Or with CLI flags
./cudalotto -user <BTC_ADDRESS>

# Full options
./cudalotto \
  -user bc1q... \
  -pool stratum+tcp://eusolo.ckpool.org:3333 \
  -worker jetson \
  -device 0 \
  -batch 16777216

# TUI dashboard (interactive)
./cudalotto -tui

# Monitor a running service (no GPU needed)
./cudalotto -monitor localhost:7777
```

### CLI flags

| Flag | Default | Description |
|---|---|---|
| `-user` | `.env` `BTC_ADDRESS` | Bitcoin address (required) |
| `-pool` | `.env` `POOL` | Stratum pool URL |
| `-worker` | `.env` `WORKER` | Worker name suffix |
| `-device` | `0` | CUDA device ID |
| `-batch` | `16777216` | Nonces per kernel launch |
| `-threads` | `256` | CUDA threads per block |
| `-tui` | `false` | Enable terminal UI dashboard |
| `-monitor` | | Connect to running service (e.g. `localhost:7777`) |
| `-http` | `127.0.0.1:7777` | HTTP stats endpoint address |

### Solo mining pools

| Pool | URL |
|---|---|
| CKPool EU Solo | `stratum+tcp://eusolo.ckpool.org:3333` |
| CKPool US Solo | `stratum+tcp://solo.ckpool.org:3333` |

## Configuration

```bash
cp .env.example .env
nano .env   # Fill in BTC_ADDRESS and ALERT_EMAIL
```

The binary reads `.env` automatically at startup. CLI flags override `.env` values. Environment variables override `.env` but are overridden by CLI flags.

## Monitoring

### TUI dashboard

Run with `-tui` for an interactive terminal dashboard:

```bash
./cudalotto -tui
```

Shows real-time hashrate sparkline, shares sent/accepted/rejected, jobs, reconnections, and live logs.

### Remote monitoring

The miner exposes a JSON stats endpoint on `127.0.0.1:7777` by default:

```bash
# JSON stats from a running service
curl http://localhost:7777/stats

# TUI dashboard connected to a running service (no GPU needed)
./cudalotto -monitor localhost:7777
```

### Email alerts

Add to root's crontab (`sudo crontab -e`):

```
*/15 * * * * /path/to/cudalotto/btc-miner-monitor.sh
```

The monitor script reads `ALERT_EMAIL` and `LOGFILE` from `.env`. It:
- Checks if the miner service is running
- Sends an email alert if a share/block is found
- Sends an email alert if the miner crashes
- Logs hashrate to the configured log file

## Systemd setup

Install and enable the service:

```bash
make install-service
```

Or manually:

```bash
sudo cp btc-miner.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable btc-miner
sudo systemctl start btc-miner
```

The service reads its config from `.env` (BTC address, pool, worker name).

Optionally, use timers for scheduled mining (e.g. overnight only):

```bash
sudo cp btc-miner-start.timer btc-miner-start.service /etc/systemd/system/
sudo cp btc-miner-stop.timer btc-miner-stop.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable btc-miner-start.timer btc-miner-stop.timer
sudo systemctl start btc-miner-start.timer btc-miner-stop.timer
```

### Manual control

```bash
sudo systemctl start btc-miner    # Start now
sudo systemctl stop btc-miner     # Stop
sudo systemctl status btc-miner   # Status
sudo journalctl -u btc-miner -f   # Live logs
```

## Expected performance

| Device | Est. hashrate | Time to find 1 block |
|---|---|---|
| Jetson Orin Nano Super | ~200-300 MH/s | ~200M years |
| Jetson Xavier NX | ~80-120 MH/s | ~500M years |
| Jetson Nano | ~30-50 MH/s | ~1B years |

Power consumption: ~10-15W additional. Yearly electricity cost: ~6-8 EUR.

## License

MIT
