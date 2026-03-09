#include "sha256d.h"
#include <stdio.h>
#include <string.h>
#include <cuda_runtime.h>

// SHA-256 constants
__constant__ uint32_t K[64] = {
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
    0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
    0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
    0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
    0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
    0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
    0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
    0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
};

// SHA-256 initial hash values (used inline in kernel for register efficiency)

#define ROTR(x, n) (((x) >> (n)) | ((x) << (32 - (n))))
#define CH(x, y, z)  (((x) & (y)) ^ (~(x) & (z)))
#define MAJ(x, y, z) (((x) & (y)) ^ ((x) & (z)) ^ ((y) & (z)))
#define EP0(x)  (ROTR(x, 2) ^ ROTR(x, 13) ^ ROTR(x, 22))
#define EP1(x)  (ROTR(x, 6) ^ ROTR(x, 11) ^ ROTR(x, 25))
#define SIG0(x) (ROTR(x, 7) ^ ROTR(x, 18) ^ ((x) >> 3))
#define SIG1(x) (ROTR(x, 17) ^ ROTR(x, 19) ^ ((x) >> 10))

// Swap endianness of a uint32
__device__ __host__ __forceinline__
uint32_t bswap32(uint32_t x) {
    return ((x & 0xFF) << 24) | ((x & 0xFF00) << 8) |
           ((x >> 8) & 0xFF00) | ((x >> 24) & 0xFF);
}

// SHA-256 compression: transform state with a 16-word (64-byte) block.
// block must be in big-endian uint32.
__device__ __forceinline__
void sha256_transform(uint32_t state[8], const uint32_t block[16]) {
    uint32_t W[64];
    #pragma unroll
    for (int i = 0; i < 16; i++) W[i] = block[i];
    #pragma unroll
    for (int i = 16; i < 64; i++)
        W[i] = SIG1(W[i-2]) + W[i-7] + SIG0(W[i-15]) + W[i-16];

    uint32_t a = state[0], b = state[1], c = state[2], d = state[3];
    uint32_t e = state[4], f = state[5], g = state[6], h = state[7];

    #pragma unroll
    for (int i = 0; i < 64; i++) {
        uint32_t t1 = h + EP1(e) + CH(e, f, g) + K[i] + W[i];
        uint32_t t2 = EP0(a) + MAJ(a, b, c);
        h = g; g = f; f = e; e = d + t1;
        d = c; c = b; b = a; a = t1 + t2;
    }

    state[0] += a; state[1] += b; state[2] += c; state[3] += d;
    state[4] += e; state[5] += f; state[6] += g; state[7] += h;
}

// SHA256d kernel: each thread tests one nonce.
// midstate: SHA256 state after first 64 bytes (big-endian words).
// tail:     last 16 bytes of header as big-endian uint32 (4 words).
//           tail[3] is the nonce position — overridden per thread.
// target:   256-bit target as uint32[8], little-endian word order (index 0 = least significant).
__global__ void sha256d_kernel(
    const uint32_t *midstate,
    const uint32_t *tail,
    uint32_t start_nonce,
    const uint32_t *target,
    uint32_t *results,
    uint32_t *result_count,
    int max_results
) {
    uint32_t nonce = start_nonce + blockIdx.x * blockDim.x + threadIdx.x;

    // --- First SHA256: finish hashing the 80-byte header ---
    // Second block: tail[0..2] + nonce + padding (80 bytes = 640 bits)
    uint32_t block[16];
    block[0] = tail[0];
    block[1] = tail[1];
    block[2] = tail[2];
    block[3] = bswap32(nonce); // nonce in big-endian for SHA256
    block[4] = 0x80000000;     // padding bit
    #pragma unroll
    for (int i = 5; i < 15; i++) block[i] = 0;
    block[15] = 640;           // message length in bits (80 bytes)

    uint32_t state[8];
    #pragma unroll
    for (int i = 0; i < 8; i++) state[i] = midstate[i];

    sha256_transform(state, block);

    // --- Second SHA256: hash the 32-byte intermediate ---
    // Prepare the single block: 32 bytes of hash + padding
    uint32_t block2[16];
    #pragma unroll
    for (int i = 0; i < 8; i++) block2[i] = state[i];
    block2[8] = 0x80000000;    // padding bit
    #pragma unroll
    for (int i = 9; i < 15; i++) block2[i] = 0;
    block2[15] = 256;          // 32 bytes = 256 bits

    // Reset state to H_INIT
    state[0] = 0x6a09e667; state[1] = 0xbb67ae85;
    state[2] = 0x3c6ef372; state[3] = 0xa54ff53a;
    state[4] = 0x510e527f; state[5] = 0x9b05688c;
    state[6] = 0x1f83d9ab; state[7] = 0x5be0cd19;

    sha256_transform(state, block2);

    // --- Check against target ---
    // Bitcoin hash comparison: convert state words to little-endian bytes,
    // then compare as 256-bit LE integer.
    // state[7] (after bswap) corresponds to the most significant bytes of the hash.
    // We compare from most significant to least significant.
    #pragma unroll
    for (int i = 7; i >= 0; i--) {
        uint32_t h_word = bswap32(state[i]);
        uint32_t t_word = target[i];
        if (h_word > t_word) return;
        if (h_word < t_word) break;
    }

    // Found a valid nonce
    uint32_t idx = atomicAdd(result_count, 1);
    if (idx < (uint32_t)max_results) {
        results[idx] = nonce;
    }
}

// Device buffers (pre-allocated)
static uint32_t *d_midstate = NULL;
static uint32_t *d_tail = NULL;
static uint32_t *d_target = NULL;
static uint32_t *d_results = NULL;
static uint32_t *d_result_count = NULL;
static int initialized = 0;

#define MAX_RESULTS 16
#define THREADS_PER_BLOCK 256

#define CUDA_CHECK(call) do { \
    cudaError_t err = (call); \
    if (err != cudaSuccess) { \
        fprintf(stderr, "CUDA error %s:%d: %s\n", __FILE__, __LINE__, cudaGetErrorString(err)); \
        return -1; \
    } \
} while(0)

#define CUDA_CHECK_VOID(call) do { \
    cudaError_t err = (call); \
    if (err != cudaSuccess) { \
        fprintf(stderr, "CUDA error %s:%d: %s\n", __FILE__, __LINE__, cudaGetErrorString(err)); \
        return; \
    } \
} while(0)

extern "C" int cuda_init(int device_id) {
    cudaError_t err = cudaSetDevice(device_id);
    if (err != cudaSuccess) {
        fprintf(stderr, "cuda_init: cudaSetDevice(%d) failed: %s\n", device_id, cudaGetErrorString(err));
        return -1;
    }

    CUDA_CHECK(cudaMalloc(&d_midstate, 8 * sizeof(uint32_t)));
    CUDA_CHECK(cudaMalloc(&d_tail, 4 * sizeof(uint32_t)));
    CUDA_CHECK(cudaMalloc(&d_target, 8 * sizeof(uint32_t)));
    CUDA_CHECK(cudaMalloc(&d_results, MAX_RESULTS * sizeof(uint32_t)));
    CUDA_CHECK(cudaMalloc(&d_result_count, sizeof(uint32_t)));

    initialized = 1;

    struct cudaDeviceProp props;
    cudaGetDeviceProperties(&props, device_id);
    fprintf(stderr, "[cudalotto] GPU: %s (%d cores, %d MHz)\n",
        props.name, props.multiProcessorCount * 128, props.clockRate / 1000);

    return 0;
}

extern "C" void cuda_cleanup(void) {
    if (!initialized) return;
    cudaFree(d_midstate);
    cudaFree(d_tail);
    cudaFree(d_target);
    cudaFree(d_results);
    cudaFree(d_result_count);
    initialized = 0;
}

extern "C" int cuda_sha256d_scan(
    const uint32_t *midstate,
    const uint32_t *tail,
    uint32_t start_nonce,
    uint32_t range_size,
    const uint32_t *target,
    uint32_t *found_nonces,
    int max_results
) {
    if (!initialized) return -1;
    if (max_results > MAX_RESULTS) max_results = MAX_RESULTS;

    uint32_t zero = 0;
    CUDA_CHECK(cudaMemcpy(d_midstate, midstate, 8 * sizeof(uint32_t), cudaMemcpyHostToDevice));
    CUDA_CHECK(cudaMemcpy(d_tail, tail, 4 * sizeof(uint32_t), cudaMemcpyHostToDevice));
    CUDA_CHECK(cudaMemcpy(d_target, target, 8 * sizeof(uint32_t), cudaMemcpyHostToDevice));
    CUDA_CHECK(cudaMemcpy(d_result_count, &zero, sizeof(uint32_t), cudaMemcpyHostToDevice));

    int blocks = (range_size + THREADS_PER_BLOCK - 1) / THREADS_PER_BLOCK;

    sha256d_kernel<<<blocks, THREADS_PER_BLOCK>>>(
        d_midstate, d_tail, start_nonce, d_target, d_results, d_result_count, max_results
    );

    CUDA_CHECK(cudaDeviceSynchronize());

    uint32_t count = 0;
    CUDA_CHECK(cudaMemcpy(&count, d_result_count, sizeof(uint32_t), cudaMemcpyDeviceToHost));

    if (count > (uint32_t)max_results) count = max_results;
    if (count > 0) {
        CUDA_CHECK(cudaMemcpy(found_nonces, d_results, count * sizeof(uint32_t), cudaMemcpyDeviceToHost));
    }

    return (int)count;
}

// CPU-side SHA256 midstate computation (processes first 64 bytes of header).
static void sha256_transform_cpu(uint32_t state[8], const uint32_t block[16]) {
    uint32_t W[64];
    for (int i = 0; i < 16; i++) W[i] = block[i];
    for (int i = 16; i < 64; i++)
        W[i] = SIG1(W[i-2]) + W[i-7] + SIG0(W[i-15]) + W[i-16];

    uint32_t a = state[0], b = state[1], c = state[2], d = state[3];
    uint32_t e = state[4], f = state[5], g = state[6], h = state[7];

    for (int i = 0; i < 64; i++) {
        uint32_t t1 = h + EP1(e) + CH(e, f, g) +
            ((const uint32_t[]){
                0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,
                0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
                0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,
                0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
                0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,
                0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
                0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,
                0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
                0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,
                0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
                0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,
                0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
                0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,
                0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
                0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,
                0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2
            })[i] + W[i];
        uint32_t t2 = EP0(a) + MAJ(a, b, c);
        h = g; g = f; f = e; e = d + t1;
        d = c; c = b; b = a; a = t1 + t2;
    }

    state[0] += a; state[1] += b; state[2] += c; state[3] += d;
    state[4] += e; state[5] += f; state[6] += g; state[7] += h;
}

static uint32_t bswap32_cpu(uint32_t x) {
    return ((x & 0xFF) << 24) | ((x & 0xFF00) << 8) |
           ((x >> 8) & 0xFF00) | ((x >> 24) & 0xFF);
}

extern "C" void sha256_midstate(const uint32_t *data64, uint32_t *out_state) {
    // data64 is 16 x uint32 = first 64 bytes of header in native byte order.
    // SHA256 expects big-endian words.
    uint32_t block[16];
    for (int i = 0; i < 16; i++)
        block[i] = bswap32_cpu(data64[i]);

    out_state[0] = 0x6a09e667; out_state[1] = 0xbb67ae85;
    out_state[2] = 0x3c6ef372; out_state[3] = 0xa54ff53a;
    out_state[4] = 0x510e527f; out_state[5] = 0x9b05688c;
    out_state[6] = 0x1f83d9ab; out_state[7] = 0x5be0cd19;

    sha256_transform_cpu(out_state, block);
}
