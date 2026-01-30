package server

import (
	"fmt"
	"log"
	"net"
	"syscall"
)

func (s *Session) setDSCPOnSocket(network, address string, c syscall.RawConn) error {
	tos := s.server.DSCP << 2
	var innerErr error
	err := c.Control(func(fd uintptr) {
		fdInt := int(fd)

		// Determine the socket type
		sa, err := syscall.Getsockname(fdInt)
		if err != nil {
			innerErr = err
			return
		}
		switch sa.(type) {
		case *syscall.SockaddrInet4:
			innerErr = syscall.SetsockoptInt(fdInt, syscall.IPPROTO_IP, syscall.IP_TOS, tos)
			if innerErr != nil {
				log.Printf("error while setting IPv4 TOS %s", err)
			}
		case *syscall.SockaddrInet6:
			innerErr = syscall.SetsockoptInt(fdInt, syscall.IPPROTO_IPV6, syscall.IPV6_TCLASS, tos)
			if innerErr != nil {
				log.Printf("error while setting IPv6 TOS %s", err)
			}
		default:
			log.Printf("unsupported socket address type while setting IPv4/IPv6 TOS")
			innerErr = fmt.Errorf("unsupported socket address type")
		}
	})
	if err != nil {
		return err
	}
	return innerErr
}

type qosFlow struct{}

func (s *qosFlow) setQoSFlow(_ net.Conn, _ string, _ int) error {
	return nil
}

func (s *qosFlow) closeQoSFlow() error {
	return nil
}
