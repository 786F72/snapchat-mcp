//go:build !windows

package main

import (
	"os"
	"syscall"
)

func lockJournalFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func protectTokenFile(string) error { return nil }

func privateTokenFile(_ string, info os.FileInfo) bool {
	return info.Mode().Perm()&0077 == 0
}
