//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

func acquireLock(name string) (func(), error) {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, errAlreadyRunning
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func processAlive(pid int) bool {
	return pid > 0 && (syscall.Kill(pid, 0) == nil || syscall.Kill(pid, 0) == syscall.EPERM)
}
func maintenanceLock(name string) (func(), error) { return acquireLock(name) }
func checkFreeSpace(dir string, expected int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < uint64(expected) {
		return fmt.Errorf("insufficient disk space: require approximately %d bytes", expected)
	}
	return nil
}
