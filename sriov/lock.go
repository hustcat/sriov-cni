package main

import (
	"fmt"
	"os"
	"syscall"
)

var defaultDeviceDir = "/var/lib/cni/devices"

var locker = &FileLocker{}

type FileLocker struct {
	file *os.File
}

// Lock acquires an exclusive lock
func (l *FileLocker) Lock(vf int) error {
	path := fmt.Sprintf("%s/%d.lock", defaultDeviceDir, vf)

	if err := os.MkdirAll(defaultDeviceDir, 0644); err != nil {
		return err
	}

	file, err := os.Open(path)
	if err != nil {
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		l.file = file
	} else {
		l.file = file
	}

	return syscall.Flock(int(l.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// Unlock releases the lock
func (l *FileLocker) Unlock() error {
	defer l.file.Close()
	return syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
}