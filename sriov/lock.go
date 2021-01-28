package main

import (
	"fmt"
	"os"
	"syscall"
)

var defaultDeviceDir = "/var/lib/cni/devices"

type FileLocker struct {
	f *os.File
}

func NewFileLocker(vf int) (*FileLocker, error) {
	if err := os.MkdirAll(defaultDeviceDir, 0644); err != nil {
		return nil, err
	}

	path := fmt.Sprintf("%s/%d.lock", defaultDeviceDir, vf)

	file, err := os.Open(path)
	if err == nil {
		return &FileLocker{file}, nil
	}

	newfile, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &FileLocker{newfile}, nil

}

// Close closes underlying file
func (l *FileLocker) Close() error {
	return l.f.Close()
}

// Lock acquires an exclusive lock
func (l *FileLocker) Lock() error {
	return syscall.Flock(int(l.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// Unlock releases the lock
func (l *FileLocker) Unlock() error {
	return syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
}
