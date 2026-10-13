package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// Jeden panel abcollab na komputer: dwa na tej samej sesji zapisuja wersje
// i nanosza zmiany w Live jednoczesnie. Blokada = port na 127.0.0.1 -
// zwalnia sie sama, takze gdy abcollab padnie.

const lockAddr = "127.0.0.1:9878"

const lockHello = "abcollab"

var errRunning = errors.New("abcollab is already running on this computer - use that window (or close it first)")

// lockInstance: nil, nil - port zajety przez cos innego niz abcollab (wtedy
// nie blokujemy).
func lockInstance() (io.Closer, error) {
	addr := lockAddr
	if a := os.Getenv("ABCOLLAB_LOCK_ADDR"); a != "" {
		addr = a
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if otherAbcollab(addr) {
			return nil, errRunning
		}
		return nil, nil
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte(lockHello + "\n"))
			c.Close()
		}
	}()
	return ln, nil
}

func otherAbcollab(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line) == lockHello
}
