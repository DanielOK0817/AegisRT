//go:build windows

package benchmark

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

type memoryStatusEx struct {
	Length                   uint32
	MemoryLoad               uint32
	TotalPhysical            uint64
	AvailablePhysical        uint64
	TotalPageFile            uint64
	AvailablePageFile        uint64
	TotalVirtual             uint64
	AvailableVirtual         uint64
	AvailableExtendedVirtual uint64
}

type windowsVersionInfo struct {
	Size             uint32
	Major            uint32
	Minor            uint32
	Build            uint32
	PlatformID       uint32
	ServicePack      [128]uint16
	ServicePackMajor uint16
	ServicePackMinor uint16
	SuiteMask        uint16
	ProductType      byte
	Reserved         byte
}

func currentEnvironment() (Environment, error) {
	memory := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	status, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx").Call(uintptr(unsafe.Pointer(&memory)))
	if status == 0 {
		return Environment{}, fmt.Errorf("query physical memory: %v", callErr)
	}
	version := windowsVersionInfo{Size: uint32(unsafe.Sizeof(windowsVersionInfo{}))}
	result, _, callErr := syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion").Call(uintptr(unsafe.Pointer(&version)))
	if result != 0 {
		return Environment{}, fmt.Errorf("query Windows version: %v", callErr)
	}
	release := fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Build)
	return Environment{
		EvidenceScope: "windows-functional", OSFamily: "windows", DistributionID: "windows",
		DistributionVersion: release, KernelRelease: release, Architecture: architecture(),
		LogicalCPUCount: runtime.NumCPU(), MemoryTotalBytes: memory.TotalPhysical,
		Cgroup: CgroupEnvironment{Version: "none", Controllers: []string{}, Delegation: "none", PSI: "unavailable"},
	}, nil
}
