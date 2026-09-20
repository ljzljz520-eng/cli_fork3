// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package solver

import (
	"net"
	"time"
)

// probePort returns true when no process accepts connections on the address.
func probePort(network, address string) bool {
	conn, err := net.DialTimeout(network, address, 500*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}
