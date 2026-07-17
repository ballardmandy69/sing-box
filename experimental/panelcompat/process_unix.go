//go:build !windows

package panelcompat

import (
	"os"
	"syscall"
)

func reloadProcess(process *os.Process) error {
	return process.Signal(syscall.SIGHUP)
}

func terminateProcess(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

func processSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

func isReloadSignal(signal os.Signal) bool {
	return signal == syscall.SIGHUP
}
