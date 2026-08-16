//go:build !linux

package proxy

func enableFreebind(fd uintptr) error {
	return nil
}
