//go:build !linux

package main

import (
	"os"
)

// 非 Linux 构建仅供本地协议及归档测试，不作为发布的节点执行器。
func acquireLock(name string) (func(), error) {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, errAlreadyRunning
		}
		return nil, err
	}
	return func() { _ = f.Close(); _ = os.Remove(name) }, nil
}
func processAlive(pid int) bool          { return pid == os.Getpid() }
func checkFreeSpace(string, int64) error { return nil }
