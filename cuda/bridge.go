package cuda

/*
#cgo CFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -L${SRCDIR} -lsha256d -lcudart -lstdc++ -lm
#include "sha256d.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"unsafe"
)

const MaxResults = 16

// Init initializes the CUDA device and pre-allocates buffers.
func Init(deviceID int) error {
	ret := C.cuda_init(C.int(deviceID))
	if ret != 0 {
		return fmt.Errorf("cuda_init failed (device %d)", deviceID)
	}
	return nil
}

// Cleanup frees GPU resources.
func Cleanup() {
	C.cuda_cleanup()
}

// Scan launches the SHA256d kernel over a nonce range.
// Returns found nonces that produce a hash <= target.
func Scan(midstate [8]uint32, tail [4]uint32, startNonce, rangeSize uint32, target [8]uint32) ([]uint32, error) {
	var found [MaxResults]uint32

	ret := C.cuda_sha256d_scan(
		(*C.uint32_t)(unsafe.Pointer(&midstate[0])),
		(*C.uint32_t)(unsafe.Pointer(&tail[0])),
		C.uint32_t(startNonce),
		C.uint32_t(rangeSize),
		(*C.uint32_t)(unsafe.Pointer(&target[0])),
		(*C.uint32_t)(unsafe.Pointer(&found[0])),
		C.int(MaxResults),
	)

	if ret < 0 {
		return nil, fmt.Errorf("cuda_sha256d_scan failed")
	}

	if ret == 0 {
		return nil, nil
	}

	results := make([]uint32, ret)
	copy(results, found[:ret])
	return results, nil
}

// SetBlockSize configures threads per block for the mining kernel.
// Must be a power of 2 between 32 and 1024.
func SetBlockSize(tpb int) {
	C.cuda_set_block_size(C.int(tpb))
}

// Midstate computes the SHA256 midstate for the first 64 bytes of the block header.
func Midstate(data [16]uint32) [8]uint32 {
	var out [8]uint32
	C.sha256_midstate(
		(*C.uint32_t)(unsafe.Pointer(&data[0])),
		(*C.uint32_t)(unsafe.Pointer(&out[0])),
	)
	return out
}
