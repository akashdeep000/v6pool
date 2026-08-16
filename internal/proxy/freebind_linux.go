//go:build linux

package proxy

import "syscall"

// enableFreebind sets IP_FREEBIND on the socket: the kernel then allows
// binding to addresses that are not assigned to any interface. This is the
// unprivileged, per-socket equivalent of the net.ipv6.ip_nonlocal_bind
// sysctl — it lets rotation work from a delegated prefix on Android/Termux
// or any host without root, as long as the network routes the prefix.
func enableFreebind(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_FREEBIND, 1)
}
