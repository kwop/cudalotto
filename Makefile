CUDA_PATH  ?= $(shell ls -d /usr/local/cuda-* 2>/dev/null | sort -V | tail -1 || echo /usr/local/cuda)
NVCC       := $(CUDA_PATH)/bin/nvcc
GO         ?= $(shell command -v go 2>/dev/null || find /usr/local/go/bin /snap/bin -name go -type f 2>/dev/null | head -1)
ARCH       ?= -gencode arch=compute_87,code=sm_87
NVCC_FLAGS := $(ARCH) -O3 --compiler-options '-fPIC'
BINARY     := cudalotto
INSTALL_DIR := $(CURDIR)

.PHONY: all clean test install install-service

all: cuda/libsha256d.a
	CGO_ENABLED=1 \
	CGO_CFLAGS="-I$(CUDA_PATH)/include" \
	CGO_LDFLAGS="-L$(CURDIR)/cuda -lsha256d -L$(CUDA_PATH)/lib64 -lcudart -lstdc++ -lm" \
	$(GO) build -o $(BINARY) .

cuda/sha256d.o: cuda/sha256d.cu cuda/sha256d.h
	$(NVCC) $(NVCC_FLAGS) -c -o $@ $<

cuda/libsha256d.a: cuda/sha256d.o
	ar rcs $@ $<

clean:
	rm -f cuda/sha256d.o cuda/libsha256d.a $(BINARY)

test:
	$(GO) test ./internal/ ./stratum/ -v

install: all
	sudo install -m 755 $(BINARY) /usr/local/bin/

install-service:
	@test -f .env || { echo "ERROR: .env not found. Run: cp .env.example .env && nano .env"; exit 1; }
	sed 's|INSTALL_DIR|$(INSTALL_DIR)|g' btc-miner.service > /tmp/btc-miner.service
	sudo cp /tmp/btc-miner.service /etc/systemd/system/btc-miner.service
	sudo cp btc-miner-start.timer btc-miner-start.service /etc/systemd/system/
	sudo cp btc-miner-stop.timer btc-miner-stop.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable btc-miner-start.timer btc-miner-stop.timer
	sudo systemctl start btc-miner-start.timer btc-miner-stop.timer
	@echo "Services installed. Mining scheduled 23:00-07:00."
