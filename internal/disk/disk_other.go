//go:build !linux

package disk

import "errors"

func usage(string) (Usage, error) {
	return Usage{}, errors.New("disk usage is only available on Linux")
}
