//go:build windows

package panelcompat

import (
	"fmt"
	"os"
)

func reloadProcess(process *os.Process) error {
	return fmt.Errorf("panel hot reload is only supported on Unix")
}

func terminateProcess(process *os.Process) error {
	return process.Kill()
}

func processSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func isReloadSignal(signal os.Signal) bool {
	return false
}
