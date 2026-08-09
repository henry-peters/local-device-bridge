// Package daemonlock prevents two local-device-bridge daemons from sharing
// one state directory and starting duplicate discovery, Telegram polling, or
// dashboard listeners.
package daemonlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Lock struct {
	path string
	file *os.File
}

// Acquire creates a process lock in the state directory. A stale lock from a
// crashed process is removed after checking the recorded PID, so a restart
// does not require manual cleanup.
func Acquire(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon lock directory: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid()); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write daemon lock: %w", writeErr)
			}
			_ = file.Sync()
			return &Lock{path: path, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create daemon lock: %w", err)
		}

		pid := readPID(path)
		if pid > 0 && processAlive(pid) {
			return nil, fmt.Errorf("another local-device-bridge daemon is already running (pid %d)", pid)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale daemon lock: %w", removeErr)
		}
	}
	return nil, errors.New("another local-device-bridge daemon is already starting")
}

func (lock *Lock) Close() error {
	if lock == nil {
		return nil
	}
	closeErr := lock.file.Close()
	removeErr := os.Remove(lock.path)
	if closeErr != nil {
		return closeErr
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return nil
}

func readPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
