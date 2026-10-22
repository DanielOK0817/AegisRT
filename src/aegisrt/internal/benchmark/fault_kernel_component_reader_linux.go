//go:build linux

package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"syscall"
)

func readFaultKernelComponent(path string, requiredOwner uint32) (int64, string, error) {
	fd, err := syscall.Open(
		path,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK,
		0,
	)
	if err != nil {
		return 0, "", faultKernelDaemonFailure("component-validation")
	}
	file := os.NewFile(uintptr(fd), "fault-kernel-component")
	if file == nil {
		_ = syscall.Close(fd)
		return 0, "", faultKernelDaemonFailure("component-validation")
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil || !validFaultKernelComponentInfo(before, requiredOwner) {
		return 0, "", faultKernelDaemonFailure("component-validation")
	}
	digest := sha256.New()
	written, err := io.CopyN(digest, file, before.Size())
	if err != nil || written != before.Size() || !faultKernelComponentEOF(file) {
		return 0, "", faultKernelDaemonFailure("component-read")
	}
	after, err := file.Stat()
	if err != nil || !stableFaultKernelComponentInfo(before, after, requiredOwner) {
		return 0, "", faultKernelDaemonFailure("component-read")
	}
	return written, hex.EncodeToString(digest.Sum(nil)), nil
}

func validFaultKernelComponentInfo(info os.FileInfo, requiredOwner uint32) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() <= 0 ||
		info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == requiredOwner
}

func stableFaultKernelComponentInfo(before, after os.FileInfo, requiredOwner uint32) bool {
	return validFaultKernelComponentInfo(after, requiredOwner) &&
		os.SameFile(before, after) && before.Size() == after.Size() &&
		before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

func faultKernelComponentEOF(file *os.File) bool {
	var extra [1]byte
	count, err := file.Read(extra[:])
	return count == 0 && err == io.EOF
}
