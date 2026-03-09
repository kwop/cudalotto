#ifndef SHA256D_H
#define SHA256D_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Initialize CUDA device and pre-allocate buffers.
int cuda_init(int device_id);

// Free device buffers.
void cuda_cleanup(void);

// Scan a range of nonces for SHA256d hashes meeting the target.
// midstate: 8 x uint32 — SHA256 state after first 64 bytes of header.
// tail:     4 x uint32 — bytes 64-79 of header (last 4 bytes of merkle + ntime + nbits + nonce placeholder).
// start_nonce: first nonce to try.
// range_size:  number of nonces to scan.
// target:   8 x uint32 — 256-bit target (little-endian words, index 7 = most significant).
// found_nonces: output buffer for winning nonces.
// max_results:  size of found_nonces buffer.
// Returns number of nonces found.
int cuda_sha256d_scan(
    const uint32_t *midstate,
    const uint32_t *tail,
    uint32_t start_nonce,
    uint32_t range_size,
    const uint32_t *target,
    uint32_t *found_nonces,
    int max_results
);

// Compute SHA256 midstate for the first 64 bytes of block header (CPU-side).
void sha256_midstate(const uint32_t *data64, uint32_t *out_state);

#ifdef __cplusplus
}
#endif

#endif
