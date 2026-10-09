package main

import (
	"encoding/hex"
	"log"
	"net"
	"time"
)

// tapConn 把读写到的原始字节打印出来，用于字节级排查。
type tapConn struct {
	net.Conn
}

func (t tapConn) Read(b []byte) (int, error) {
	n, err := t.Conn.Read(b)
	if n > 0 {
		log.Printf("TCP<< %s", hex.EncodeToString(b[:n]))
	}
	return n, err
}

func (t tapConn) Write(b []byte) (int, error) {
	log.Printf("TCP>> %s", hex.EncodeToString(b))
	return t.Conn.Write(b)
}

func (t tapConn) SetReadDeadline(tm time.Time) error { return t.Conn.SetReadDeadline(tm) }
