package disk

import (
	"fmt"
	"syscall"
)

func usage(dir string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return Usage{}, err
	}
	bs := uint64(st.Bsize)
	u := Usage{Path: dir, TotalBytes: st.Blocks * bs, FreeBytes: st.Bavail * bs, fsid: fmt.Sprintf("%x-%x", st.Fsid.X__val[0], st.Fsid.X__val[1])}
	u.Low = isLow(u)
	return u, nil
}
