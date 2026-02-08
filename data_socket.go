// Copyright 2018 The goftp Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package server

import (
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/tejaskumark/goftp-server/ratelimit"
)

// DataSocket describes a data socket is used to send non-control data between the client and
// server.
type DataSocket interface {
	Host() string

	Port() int

	// the standard io.Reader interface
	Read(p []byte) (n int, err error)

	// the standard io.ReaderFrom interface
	ReadFrom(r io.Reader) (int64, error)

	// the standard io.Writer interface
	Write(p []byte) (n int, err error)

	// the standard io.Closer interface
	Close() error
}

type activeSocket struct {
	conn   *net.TCPConn
	reader io.Reader
	writer io.Writer
	sess   *Session
	host   string
	port   int
}

func newActiveSocket(sess *Session, remote string, port int) (DataSocket, error) {
	connectTo := net.JoinHostPort(remote, strconv.Itoa(port))

	sess.log("Opening active data connection to " + connectTo)
	// ----- Patch Start ----- //
	// 1. Configure the Dialer with a Control hook
	d := net.Dialer{
		Timeout: 10 * time.Second,
		Control: sess.setDSCPOnSocket,
	}
	// 2. Dial with a safety timeout
	conn, err := d.Dial("tcp", connectTo)
	if err != nil {
		sess.log(err)
		return nil, err
	}
	// 3. Type Assert to *net.TCPConn
	// This is safe because we specifically dialed "tcp" above.
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		// This theoretically shouldn't happen if network is "tcp"
		conn.Close()
		err := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: net.UnknownNetworkError("connection is not TCP"),
		}
		return nil, err
	}
	// ----- Patch End ----- //

	socket := new(activeSocket)
	socket.sess = sess
	socket.conn = tcpConn
	socket.reader = ratelimit.Reader(tcpConn, sess.server.rateLimiter)
	socket.writer = ratelimit.Writer(tcpConn, sess.server.rateLimiter)
	socket.host = remote
	socket.port = port

	return socket, nil
}

func (socket *activeSocket) Host() string {
	return socket.host
}

func (socket *activeSocket) Port() int {
	return socket.port
}

func (socket *activeSocket) Read(p []byte) (n int, err error) {
	return socket.reader.Read(p)
}

func (socket *activeSocket) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(socket.writer, r)
}

func (socket *activeSocket) Write(p []byte) (n int, err error) {
	return socket.writer.Write(p)
}

func (socket *activeSocket) Close() error {
	return socket.conn.Close()
}

type passiveSocket struct {
	sess    *Session
	conn    net.Conn
	reader  io.Reader
	writer  io.Writer
	port    int
	host    string
	ingress chan []byte
	egress  chan []byte
	lock    sync.Mutex // protects conn and err
	err     error
}

// Detect if an error is "bind: address already in use"
//
// Originally from https://stackoverflow.com/a/52152912/164234
func isErrorAddressAlreadyInUse(err error) bool {
	errOpError, ok := err.(*net.OpError)
	if !ok {
		return false
	}
	errSyscallError, ok := errOpError.Err.(*os.SyscallError)
	if !ok {
		return false
	}
	errErrno, ok := errSyscallError.Err.(syscall.Errno)
	if !ok {
		return false
	}
	if errErrno == syscall.EADDRINUSE {
		return true
	}
	const WSAEADDRINUSE = 10048
	if runtime.GOOS == "windows" && errErrno == WSAEADDRINUSE {
		return true
	}
	return false
}

func (sess *Session) newPassiveSocket() (DataSocket, error) {
	socket := new(passiveSocket)
	socket.ingress = make(chan []byte)
	socket.egress = make(chan []byte)
	socket.sess = sess
	socket.host = sess.passiveListenIP()

	const retries = 10
	var err error
	for i := 1; i <= retries; i++ {
		socket.port = sess.PassivePort()
		err = socket.ListenAndServe()
		if err != nil && socket.port != 0 && isErrorAddressAlreadyInUse(err) {
			// choose a different port on error already in use
			continue
		}
		break
	}
	sess.dataConn = socket
	return socket, err
}

func (socket *passiveSocket) Host() string {
	return socket.host
}

func (socket *passiveSocket) Port() int {
	return socket.port
}

func (socket *passiveSocket) Read(p []byte) (n int, err error) {
	socket.lock.Lock()
	defer socket.lock.Unlock()
	if socket.err != nil {
		return 0, socket.err
	}
	return socket.reader.Read(p)
}

func (socket *passiveSocket) ReadFrom(r io.Reader) (int64, error) {
	socket.lock.Lock()
	defer socket.lock.Unlock()
	if socket.err != nil {
		return 0, socket.err
	}

	// For normal TCPConn, this will use sendfile syscall; if not,
	// it will just downgrade to normal read/write procedure
	return io.Copy(socket.writer, r)
}

func (socket *passiveSocket) Write(p []byte) (n int, err error) {
	socket.lock.Lock()
	defer socket.lock.Unlock()
	if socket.err != nil {
		return 0, socket.err
	}
	return socket.writer.Write(p)
}

func (socket *passiveSocket) Close() error {
	socket.lock.Lock()
	defer socket.lock.Unlock()
	if socket.conn != nil {
		return socket.conn.Close()
	}
	return nil
}
