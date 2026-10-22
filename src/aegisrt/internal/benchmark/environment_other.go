//go:build !windows && !linux

package benchmark

import "fmt"

func currentEnvironment() (Environment, error) {
	return Environment{}, fmt.Errorf("experiment collector does not support this operating system")
}
