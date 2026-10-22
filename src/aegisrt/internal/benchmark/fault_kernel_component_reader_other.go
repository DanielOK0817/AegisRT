//go:build !linux

package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

func readFaultKernelComponent(path string, _ uint32) (int64, string, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil || !validPortableFaultKernelComponentInfo(pathInfo) ||
		pathInfo.Mode()&os.ModeSymlink != 0 {
		return 0, "", faultKernelDaemonFailure("component-validation")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", faultKernelDaemonFailure("component-validation")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !validPortableFaultKernelComponentInfo(before) || !os.SameFile(pathInfo, before) {
		return 0, "", faultKernelDaemonFailure("component-validation")
	}
	digest := sha256.New()
	written, err := io.CopyN(digest, file, before.Size())
	if err != nil || written != before.Size() || !faultKernelComponentEOF(file) {
		return 0, "", faultKernelDaemonFailure("component-read")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return 0, "", faultKernelDaemonFailure("component-read")
	}
	return written, hex.EncodeToString(digest.Sum(nil)), nil
}

func validPortableFaultKernelComponentInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Size() > 0
}

func faultKernelComponentEOF(file *os.File) bool {
	var extra [1]byte
	count, err := file.Read(extra[:])
	return count == 0 && err == io.EOF
}
