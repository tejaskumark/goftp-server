package server

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

//sys winQoSCreateHandle(clientVersion unsafe.Pointer, qosHandle *windows.Handle) (ok bool, err error) [failretval==false] = qwave.QOSCreateHandle
//sys winQoSAddSocketToFlow(qosHandle windows.Handle, socket windows.Handle, destAddr unsafe.Pointer, trafficType uint32, flags uint32, flowId *uint32) (ok bool, err error) [failretval==false] = qwave.QOSAddSocketToFlow
//sys winQoSSetFlow(qosHandle windows.Handle, flowId uint32, operation uint32, size uint32, buffer unsafe.Pointer, flags uint32, overlapped *windows.Overlapped) (ok bool, err error) [failretval==false] = qwave.QOSSetFlow
//sys winQoSRemoveSocketFromFlow(qosHandle windows.Handle, socket windows.Handle, flowId uint32, flags uint32) (ok bool, err error) [failretval==false] = qwave.QOSRemoveSocketFromFlow
//sys winQoSCloseHandle(qosHandle windows.Handle) (ok bool, err error) [failretval==false] = qwave.QOSCloseHandle

type clientVersion struct {
	MajorVersion uint16
	MinorVersion uint16
}

const (
	QOS_NON_ADAPTIVE_FLOW   = 0x00000002
	QOSSetOutgoingDSCPValue = 2
)

type QOS_TRAFFIC_TYPE uint32

const (
	QOSTrafficTypeBestEffort QOS_TRAFFIC_TYPE = iota
	QOSTrafficTypeBackground
	QOSTrafficTypeExcellentEffort
	QOSTrafficTypeAudioVideo
	QOSTrafficTypeVoice
	QOSTrafficTypeControl
)

func (s *Session) setDSCPOnSocket(network, address string, c syscall.RawConn) error {
	return nil
}

func isIPv4(ipaddr string) bool {
	ip := net.ParseIP(ipaddr)
	return ip != nil && ip.To4() != nil
}

func isIPv6(ipaddr string) bool {
	ip := net.ParseIP(ipaddr)
	return ip != nil && (ip.To4() == nil && ip.To16() != nil)
}

func getSockAddrIntet4(address string) (*syscall.RawSockaddrInet4, error) {
	var sa syscall.RawSockaddrInet4
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	portInt, _ := strconv.Atoi(portStr)

	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP")
	}

	// Check if IPv4
	if ip4 := ip.To4(); ip4 != nil {
		sa.Family = syscall.AF_INET
		binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&sa.Port))[:], uint16(portInt))
		copy(sa.Addr[:], ip4)
		return &sa, nil
	}

	return &sa, fmt.Errorf("unknown IP family")
}

// GetSockAddrInet6 creates the correct C-compatible struct for IPv4
// and returns an error if any.
func getSockAddrIntet6(address string) (*syscall.RawSockaddrInet6, error) {
	var sa syscall.RawSockaddrInet6
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	portInt, _ := strconv.Atoi(portStr)
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, fmt.Errorf("invalid IP")
	}

	// Must be IPv6
	if ip6 := ip.To16(); ip6 != nil {
		sa.Family = syscall.AF_INET6
		binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&sa.Port))[:], uint16(portInt))
		copy(sa.Addr[:], ip6)
		// ScopeID is usually required for Link-Local addresses, skipping for simplicity here
		return &sa, nil
	}

	return &sa, fmt.Errorf("unknown IP family")
}

type qosFlow struct {
	qosHandle    windows.Handle
	socketHandle windows.Handle
	flowId       uint32
}

func (q *qosFlow) setQoSFlow(conn net.Conn, remoteAddr string, dscp int) error {
	var operr error
	uint32dscp := uint32(dscp)
	sysConn, ok := conn.(syscall.Conn)
	if !ok {
		return fmt.Errorf("connection is not a *net.TCPConn or does not support SyscallConn")
	}

	rawConn, err := sysConn.SyscallConn()
	if err != nil {
		return err
	}

	var version clientVersion
	version.MajorVersion = 1
	version.MinorVersion = 0
	ok, err = winQoSCreateHandle(unsafe.Pointer(&version), &q.qosHandle)
	if !ok || err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return err
	}

	var sockAddrPtr unsafe.Pointer
	if isIPv4(host) {
		var sockAddr *syscall.RawSockaddrInet4
		sockAddr, err = getSockAddrIntet4(remoteAddr)
		if err != nil {
			return err
		}
		sockAddrPtr = unsafe.Pointer(sockAddr)
	} else if isIPv6(host) {
		var sockAddr *syscall.RawSockaddrInet6
		sockAddr, err = getSockAddrIntet6(remoteAddr)
		if err != nil {
			return err
		}
		sockAddrPtr = unsafe.Pointer(sockAddr)
	} else {
		return fmt.Errorf("host - %s is not valid IPv4 or IPv6", host)
	}

	ctrlErr := rawConn.Control(func(fd uintptr) {
		q.socketHandle = windows.Handle(fd)
		ok, err = winQoSAddSocketToFlow(
			q.qosHandle,
			q.socketHandle,
			sockAddrPtr,
			uint32(QOSTrafficTypeExcellentEffort),
			QOS_NON_ADAPTIVE_FLOW,
			&q.flowId,
		)
		if !ok {
			operr = err
			return
		}
		ok, err = winQoSSetFlow(
			q.qosHandle,
			q.flowId,
			QOSSetOutgoingDSCPValue,
			4,
			unsafe.Pointer(&uint32dscp),
			0,
			nil,
		)
		if !ok {
			operr = err
			return
		}
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return operr
}

func (q *qosFlow) closeQoSFlow() error {
	if q.qosHandle != windows.InvalidHandle {
		ok, err := winQoSRemoveSocketFromFlow(q.qosHandle, windows.Handle(0),
			q.flowId, 0)
		// Ignore error 10038 (Not a socket) which occurs if Dial failed and closed the socket
		if !ok && err != syscall.Errno(10038) {
			err = fmt.Errorf("Qos flow remove error: %s", err)
		}

		ok, err = winQoSCloseHandle(q.qosHandle)
		if !ok {
			err = fmt.Errorf("Qos handle close error: %s", err)
		}
	}
	return nil
}
