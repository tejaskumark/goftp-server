// Copyright 2018 The goftp Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package server

import (
	"fmt"
	"log"
)

func init() {
	// This tells the standard logger to include the file name and line number
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

// Logger represents an interface to record all ftp information and command
type Logger interface {
	Print(sessionID string, message interface{})
	Printf(sessionID string, format string, v ...interface{})
	PrintCommand(sessionID string, command string, params string)
	PrintResponse(sessionID string, code int, message string)
}

// StdLogger use an instance of this to log in a standard format
type StdLogger struct{}

// Print implements Logger
func (logger *StdLogger) Print(sessionID string, message interface{}) {
	log.Output(3, fmt.Sprintf("%s  %s", sessionID, message))
}

// Printf implements Logger
func (logger *StdLogger) Printf(sessionID string, format string, v ...interface{}) {
	log.Output(3, fmt.Sprintf(sessionID, fmt.Sprintf(format, v...)))
}

// PrintCommand implements Logger
func (logger *StdLogger) PrintCommand(sessionID string, command string, params string) {
	if command == "PASS" {
		log.Output(3, fmt.Sprintf("%s > PASS ****", sessionID))
	} else {
		log.Output(3, fmt.Sprintf("%s > %s %s", sessionID, command, params))
	}
}

// PrintResponse implements Logger
func (logger *StdLogger) PrintResponse(sessionID string, code int, message string) {
	log.Output(3, fmt.Sprintf("%s < %d %s", sessionID, code, message))
}

// DiscardLogger represents a silent logger, produces no output
type DiscardLogger struct{}

// Print implements Logger
func (logger *DiscardLogger) Print(sessionID string, message interface{}) {}

// Printf implements Logger
func (logger *DiscardLogger) Printf(sessionID string, format string, v ...interface{}) {}

// PrintCommand implements Logger
func (logger *DiscardLogger) PrintCommand(sessionID string, command string, params string) {}

// PrintResponse implements Logger
func (logger *DiscardLogger) PrintResponse(sessionID string, code int, message string) {}
