package zk

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"zkscan/internal/core"
)

// multiDevice is a fake TCP device that accepts several connections and records the commands
// of each connection separately.
type multiDevice struct {
	f     *fake
	mu    sync.Mutex
	conns [][]uint16
	// hook runs before a request is answered; returning true drops the connection.
	hook func(conn int, cmd uint16) (drop bool)
}

func (m *multiDevice) connCmds() [][]uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]uint16, len(m.conns))
	for i, c := range m.conns {
		out[i] = append([]uint16(nil), c...)
	}
	return out
}

func spawnMulti(t *testing.T, m *multiDevice) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			m.mu.Lock()
			idx := len(m.conns)
			m.conns = append(m.conns, nil)
			m.mu.Unlock()
			go func() {
				defer c.Close()
				for {
					var pre [8]byte
					_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
					if _, err := io.ReadFull(c, pre[:]); err != nil {
						return
					}
					payload := make([]byte, binary.LittleEndian.Uint32(pre[4:]))
					if _, err := io.ReadFull(c, payload); err != nil {
						return
					}
					pk, err := parsePacket(payload)
					if err != nil {
						return
					}
					m.mu.Lock()
					m.conns[idx] = append(m.conns[idx], pk.header.command)
					m.mu.Unlock()
					if m.hook != nil && m.hook(idx, pk.header.command) {
						return
					}
					replies, exit := m.f.handle(payload)
					var wire []byte
					for _, r := range replies {
						wire = append(wire, tcpFrame(r)...)
					}
					if _, err := c.Write(wire); err != nil || exit {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func count(cmds []uint16, c uint16) int {
	n := 0
	for _, x := range cmds {
		if x == c {
			n++
		}
	}
	return n
}

// waitRecovery waits until connection #1 (the recovery connection) has sent ENABLEDEVICE.
func waitRecovery(t *testing.T, m *multiDevice) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cs := m.connCmds(); len(cs) >= 2 && count(cs[1], cmdEnableDevice) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("recovery connection never sent ENABLEDEVICE: %v", m.connCmds())
}

func TestCancelDuringAttendanceReadRecoversEnable(t *testing.T) {
	m := &multiDevice{f: newFake(nil, false)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepares := 0
	m.hook = func(conn int, cmd uint16) bool {
		if conn == 0 && cmd == cmdPrepareBuffer {
			prepares++
			if prepares == 2 { // attendance read: the device answers slowly
				go cancel()
				time.Sleep(3 * time.Second)
			}
		}
		return false
	}
	port := spawnMulti(t, m)
	start := time.Now()
	_, err := New().Fetch(ctx, dev(port, core.TransportTCP, 0))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Fetch took %v after cancel", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	deviceErr(t, err)
	waitRecovery(t, m)
	cs := m.connCmds()
	if len(cs) != 2 {
		t.Errorf("want exactly 2 connections, got %v", cs)
	}
	if cs[1][0] != cmdConnect || count(cs[1], cmdEnableDevice) != 1 {
		t.Errorf("recovery conn: %v", cs[1])
	}
}

func TestDroppedConnectionMidReadRecoversEnable(t *testing.T) {
	m := &multiDevice{f: newFake(nil, false)}
	m.hook = func(conn int, cmd uint16) bool {
		return conn == 0 && cmd == cmdPrepareBuffer // drop on the first bulk read
	}
	port := spawnMulti(t, m)
	_, err := New().Fetch(context.Background(), dev(port, core.TransportTCP, 0))
	deviceErr(t, err)
	waitRecovery(t, m)
	cs := m.connCmds()
	if len(cs) != 2 || count(cs[1], cmdEnableDevice) != 1 {
		t.Errorf("conns: %v", cs)
	}
	if count(cs[0], cmdEnableDevice) != 0 {
		t.Errorf("first conn must not have ENABLE: %v", cs[0])
	}
}

func TestSuccessSendsOneEnableNoRecoveryConn(t *testing.T) {
	m := &multiDevice{f: newFake(nil, false)}
	port := spawnMulti(t, m)
	snap, err := New().Fetch(context.Background(), dev(port, core.TransportTCP, 0))
	mustNoErr(t, err)
	checkSnapshot(t, snap)
	time.Sleep(200 * time.Millisecond)
	cs := m.connCmds()
	if len(cs) != 1 {
		t.Fatalf("want 1 connection, got %v", cs)
	}
	if count(cs[0], cmdEnableDevice) != 1 || count(cs[0], cmdDisableDevice) != 1 {
		t.Errorf("cmds: %v", cs[0])
	}
}

func TestFailureBeforeDisableOpensNoRecoveryConn(t *testing.T) {
	m := &multiDevice{f: newFake(u32p(4321), false)}
	port := spawnMulti(t, m)
	_, err := New().Fetch(context.Background(), dev(port, core.TransportTCP, 1))
	deviceErr(t, err)
	time.Sleep(200 * time.Millisecond)
	cs := m.connCmds()
	if len(cs) != 1 || count(cs[0], cmdDisableDevice) != 0 {
		t.Errorf("conns: %v", cs)
	}
}
