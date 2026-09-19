package config

import (
	"fmt"
)

// StorageKind classifies the filesystem behind a directory as far as SQLite cares.
type StorageKind int

const (
	// StorageLocal is a normal local filesystem (ext4, xfs, btrfs, zfs, overlay, …).
	StorageLocal StorageKind = iota
	// StorageFUSE is a userspace filesystem – on Unraid that is /mnt/user (shfs). SQLite
	// works on it, but locking and fsync behaviour depend on the FUSE driver; the
	// community advice for databases on Unraid is the pool path (/mnt/cache/appdata).
	StorageFUSE
	// StorageNetwork is NFS/SMB/CIFS. SQLite's file locking is unreliable there;
	// silent corruption is the documented outcome.
	StorageNetwork
	// StorageUnknown means the check could not run (unsupported platform, stat failure).
	StorageUnknown
)

// StorageCheck is the result of CheckStorage.
type StorageCheck struct {
	Kind StorageKind
	// Name is the filesystem name for messages ("nfs", "cifs", "fuse", …).
	Name string
}

// ErrNetworkStorage is returned by ValidateStorage for a config directory on a network
// filesystem when ENVORYX_ALLOW_NETWORK_FS is not set.
type ErrNetworkStorage struct{ Dir, FS string }

func (e *ErrNetworkStorage) Error() string {
	return fmt.Sprintf("%s is on a network filesystem (%s): SQLite cannot lock files reliably there and the database would corrupt silently; use a local directory, or set ENVORYX_ALLOW_NETWORK_FS=true to accept the risk", e.Dir, e.FS)
}

// ValidateStorage checks the config directory's filesystem. It returns an error for
// network filesystems (unless allowed) and a warning text for FUSE; "" means fine.
func ValidateStorage(dir string, allowNetwork bool) (warning string, err error) {
	c := CheckStorage(dir)
	switch c.Kind {
	case StorageNetwork:
		if !allowNetwork {
			return "", &ErrNetworkStorage{Dir: dir, FS: c.Name}
		}
		return fmt.Sprintf("%s is on a network filesystem (%s); ENVORYX_ALLOW_NETWORK_FS is set, so Envoryx runs anyway – database corruption is possible", dir, c.Name), nil
	case StorageFUSE:
		return fmt.Sprintf("%s is on a FUSE filesystem (%s). On Unraid that is /mnt/user: point the config path at the pool directly (e.g. /mnt/cache/appdata/envoryx) or enable exclusive access for the appdata share, so the SQLite database is on a real filesystem", dir, c.Name), nil
	}
	return "", nil
}
