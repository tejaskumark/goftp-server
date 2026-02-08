// Copyright 2018 The goftp Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

//go:build !darwin

package server

import (
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tejaskumark/goftp-server/ratelimit"
)

func (socket *passiveSocket) ListenAndServe() (err error) {
	// --- PATCH STARTS ---
	address := net.JoinHostPort("", strconv.Itoa(socket.port))
	lc := net.ListenConfig{
		Control: socket.sess.setDSCPOnSocket,
	}

	// Create the generic listener
	ln, err := lc.Listen(socket.sess.server.ctx, "tcp", address)
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
	if socket.sess.server.tlsConfig != nil {
		listener = tls.NewListener(listener, socket.sess.server.tlsConfig)
	}

	socket.lock.Lock()
	go func() {
		defer socket.lock.Unlock()

		conn, err := listener.Accept()
		if err != nil {
			socket.err = err
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
	}()
	return nil
}
