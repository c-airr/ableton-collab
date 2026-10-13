package main

import (
	"net"
	"testing"
)

func TestSecondInstanceRefused(t *testing.T) {
	t.Setenv("ABCOLLAB_LOCK_ADDR", "127.0.0.1:19878")
	l, err := lockInstance()
	if err != nil || l == nil {
		t.Fatalf("first: %v %v", l, err)
	}
	if _, err := lockInstance(); err != errRunning {
		t.Fatalf("second instance not refused: %v", err)
	}
	l.Close()
	l2, err := lockInstance()
	if err != nil || l2 == nil {
		t.Fatalf("after close: %v", err)
	}
	l2.Close()
}

// Port zajety przez cos innego - abcollab startuje normalnie.
func TestLockPortTakenByOther(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Setenv("ABCOLLAB_LOCK_ADDR", ln.Addr().String())
	if l, err := lockInstance(); err != nil || l != nil {
		t.Fatalf("%v %v", l, err)
	}
}
