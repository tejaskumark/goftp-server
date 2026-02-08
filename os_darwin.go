package server

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tejaskumark/goftp-server/ratelimit"
)

func (socket *passiveSocket) ListenAndServe() (err error) {
	// --- PATCH STARTS ---
	socket.sess.log(socket.port)
	address := net.JoinHostPort("", strconv.Itoa(socket.port))
	lc := net.ListenConfig{
		Control: socket.sess.setDSCPOnSocket,
	}

	// Create the generic listener
	// v6 Change
	ln, err := lc.Listen(socket.sess.server.ctx, "tcp4", address)
	if err != nil {
		socket.sess.log(err)
		return err
	}

	// Convert back to *net.TCPListener to keep existing logic (SetDeadline) working
	// This is safe because we forced "tcp" network in Listen
	tcplistener, ok := ln.(*net.TCPListener)
	if !ok {
		socket.sess.log("fatal error while type asserting *net.TCPListener")
		return errors.New("fatal error while type asserting *net.TCPListener")
	}

	// --- PATCH END ---

	// The timeout, for a remote client to establish connection
	// with a PASV style data connection.
	const acceptTimeout = 60 * time.Second
	err = tcplistener.SetDeadline(time.Now().Add(acceptTimeout))
	if err != nil {
		socket.sess.log(err)
		return err
	}

	var listener net.Listener = tcplistener

	add := listener.Addr()
	parts := strings.Split(add.String(), ":")
	port, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		socket.sess.log(err)
		return err
	}

	socket.port = port

	// For darwin if we use tcp generic listener then when ever we try to set QoS on IPv4 connection
	// it fails and not getting applied as darwin uses IPv4 mapped IPv6 socket behind the scene
	// and IPv4 QoS can not be set on such sockets so we need to create two different listenrs
	// sepearately IPv4 and IPv6 to accept data connection and set QoS on such connection is
	// possible
	lc6 := net.ListenConfig{
		Control: socket.sess.setDSCPOnSocket,
	}

	ln6, err := lc6.Listen(socket.sess.server.ctx, "tcp6", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		socket.sess.log(err)
		return err
	}

	tcplistener6, ok := ln6.(*net.TCPListener)
	if !ok {
		socket.sess.log("fatal error while type asserting *net.TCPListener")
		return errors.New("fatal error while type asserting *net.TCPListener")
	}

	err = tcplistener6.SetDeadline(time.Now().Add(acceptTimeout))
	if err != nil {
		socket.sess.log(err)
		return err
	}

	var listener6 net.Listener = tcplistener6

	if socket.sess.server.tlsConfig != nil {
		listener = tls.NewListener(listener, socket.sess.server.tlsConfig)
		listener6 = tls.NewListener(listener6, socket.sess.server.tlsConfig)
	}

	socket.lock.Lock()
	cch := make(chan net.Conn, 2)
	go func() {
		defer socket.lock.Unlock()
		var wg sync.WaitGroup
		wg.Add(2)
		// spawn two go routinges to handle IPv4 and IPv6 connection and client connection
		// can be established on any of them, and return that connection immediately through
		// channel and other go routine will always timeout or getting closed early
		go func() {
			defer wg.Done()
			conn, err := listener.Accept()
			if err != nil {
				socket.sess.log(err)
				return
			} else {
				cch <- conn
			}
		}()
		go func() {
			defer wg.Done()
			conn, err := listener6.Accept()
			if err != nil {
				socket.sess.log(err)
				return
			} else {
				cch <- conn
			}
		}()

		go func() {
			wg.Wait()
			close(cch)
		}()

		conn, ok := <-cch
		// if both go routine times out then we close the channel and will hit this error condition
		if !ok {
			socket.err = fmt.Errorf("connection not established by client")
			return
		}
		// This is just to handle error condition which are unkwnon, not sure at this point
		// in which case this can hit, so handling it here only
		if conn == nil {
			socket.err = fmt.Errorf("connection not established by client, unknown path")
			return
		}

		socket.err = nil
		socket.conn = conn
		// ---- PATCH START ----
		var q qosFlow
		socket.sess.qos = &q
		if err := socket.sess.qos.setQoSFlow(conn, conn.RemoteAddr().String(),
			socket.sess.server.DSCP); err != nil {
			socket.sess.logf("error:%s setting QoS for %s", err, conn.RemoteAddr().String())
		}
		// ---- PATCH END ----

		socket.reader = ratelimit.Reader(socket.conn, socket.sess.server.rateLimiter)
		socket.writer = ratelimit.Writer(socket.conn, socket.sess.server.rateLimiter)
		_ = listener.Close()
		_ = listener6.Close()
		socket.sess.log("listener close")
	}()
	return nil
}

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
