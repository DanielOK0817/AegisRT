//go:build !linux

package benchmark

func faultKernelTestComponentOwner() uint32 {
	return 0
}
