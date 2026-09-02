package lock

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// OperationLock 防止两个 config-mesh 进程同时改写 Git 仓库、备份和状态文件。
type OperationLock struct {
	file *os.File
}

func Acquire() (*OperationLock, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(homeDir, ".config-mesh")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(stateDir, "operation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("另一个 config-mesh 写操作正在进行")
	}
	return &OperationLock{file: file}, nil
}

func (l *OperationLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN); err != nil {
		l.file.Close()
		return err
	}
	err := l.file.Close()
	l.file = nil
	return err
}
