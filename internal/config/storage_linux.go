package config

import "syscall"

// Magic numbers from linux/magic.h.
const (
	nfsSuperMagic  = 0x6969
	smbSuperMagic  = 0x517B
	cifsMagic      = 0xFF534D42
	smb2Magic      = 0xFE534D42
	fuseSuperMagic = 0x65735546
	cephSuperMagic = 0x00C36400
	afsSuperMagic  = 0x5346414F
	v9fsMagic      = 0x01021997
)

// CheckStorage classifies the filesystem that holds dir.
func CheckStorage(dir string) StorageCheck {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return StorageCheck{Kind: StorageUnknown}
	}
	switch uint32(st.Type) { // Type is int64 on most architectures, int32 on some
	case nfsSuperMagic:
		return StorageCheck{Kind: StorageNetwork, Name: "nfs"}
	case smbSuperMagic, cifsMagic, smb2Magic:
		return StorageCheck{Kind: StorageNetwork, Name: "cifs/smb"}
	case cephSuperMagic:
		return StorageCheck{Kind: StorageNetwork, Name: "ceph"}
	case afsSuperMagic:
		return StorageCheck{Kind: StorageNetwork, Name: "afs"}
	case v9fsMagic:
		return StorageCheck{Kind: StorageNetwork, Name: "9p"}
	case fuseSuperMagic:
		return StorageCheck{Kind: StorageFUSE, Name: "fuse"}
	}
	return StorageCheck{Kind: StorageLocal}
}
