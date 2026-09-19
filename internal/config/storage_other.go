//go:build !linux

package config

// CheckStorage cannot classify filesystems off Linux; Envoryx ships as a Linux container.
func CheckStorage(string) StorageCheck { return StorageCheck{Kind: StorageUnknown} }
