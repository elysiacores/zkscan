package zk

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zkscan/internal/core"
)

const fakeSession uint16 = 0x1234

type seen struct {
	mu       sync.Mutex
	auth     []byte
	setTime  *uint32
	commands []uint16
}

func (s *seen) cmds() []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

// fake is the fake device (port of the Rust test harness).
type fake struct {
	commKey          *uint32
	chunkedAttendace bool
	usersBlob        []byte
	attBlob          []byte
	counts           [2]int32
	seen             *seen
}

func t0() time.Time { return dt(2026, 11, 4, 8, 5, 9) }

func newFake(commKey *uint32, chunked bool) *fake {
	usersBlob := withSize(
		user72(1, 14, "สมชาย", 111111, "1001"),
		user72(2, 0, "สุดา", 222222, "1002"),
	)
	// deliberately not sorted
	attBlob := withSize(
		att40(2, "1002", 1, dt(2026, 11, 3, 17, 30, 0), 1),
		att40(1, "1001", 1, dt(2026, 11, 3, 8, 0, 5), 0),
		att40(2, "1002", 2, dt(2026, 11, 3, 8, 10, 0), 0),
	)
	return &fake{commKey: commKey, chunkedAttendace: chunked, usersBlob: usersBlob, attBlob: attBlob,
		counts: [2]int32{2, 3}, seen: &seen{}}
}

func u32p(v uint32) *uint32 { return &v }

// handle returns the reply payloads (headers + data) and whether the session ended.
func (f *fake) handle(req []byte) ([][]byte, bool) {
	pk, err := parsePacket(req)
	if err != nil {
		panic(err)
	}
	rid, cmd := pk.header.replyID, pk.header.command
	rep := func(c uint16, d []byte) []byte { return buildPacket(c, fakeSession, rid, d) }
	ok := func() [][]byte { return [][]byte{rep(cmdAckOK, nil)} }
	f.seen.mu.Lock()
	f.seen.commands = append(f.seen.commands, cmd)
	f.seen.mu.Unlock()
	if cmd != cmdConnect && pk.header.sessionID != fakeSession {
		return [][]byte{rep(cmdAckError, nil)}, false
	}
	var replies [][]byte
	switch cmd {
	case cmdConnect:
		if f.commKey != nil {
			replies = [][]byte{rep(cmdAckUnauth, nil)}
		} else {
			replies = ok()
		}
	case cmdAuth:
		f.seen.mu.Lock()
		f.seen.auth = slices.Clone(pk.data)
		f.seen.mu.Unlock()
		var key uint32
		if f.commKey != nil {
			key = *f.commKey
		}
		expected := makeCommKey(key, fakeSession, 50)
		if bytes.Equal(pk.data, expected[:]) {
			replies = ok()
		} else {
			replies = [][]byte{rep(cmdAckUnauth, nil)}
		}
	case cmdOptionsRRQ:
		name := strings.Trim(string(pk.data), "\x00~")
		value := map[string]string{"SerialNumber": "FAKE-SN-001", "Platform": "ZMM220_TFT", "DeviceName": "FakeDevice"}[name]
		replies = [][]byte{rep(cmdAckOK, []byte("~"+name+"="+value+"\x00"))}
	case cmdGetVersion:
		replies = [][]byte{rep(cmdAckOK, []byte("Ver 6.60 Sep 2 2019\x00"))}
	case cmdGetTime:
		replies = [][]byte{rep(cmdAckOK, binary.LittleEndian.AppendUint32(nil, encodeTime(t0())))}
	case cmdSetTime:
		v := binary.LittleEndian.Uint32(pk.data)
		f.seen.mu.Lock()
		f.seen.setTime = &v
		f.seen.mu.Unlock()
		replies = ok()
	case cmdGetFreeSizes:
		d := make([]byte, 80)
		binary.LittleEndian.PutUint32(d[16:], uint32(f.counts[0]))
		binary.LittleEndian.PutUint32(d[32:], uint32(f.counts[1]))
		replies = [][]byte{rep(cmdAckOK, d)}
	case cmdDisableDevice, cmdEnableDevice, cmdFreeData:
		replies = ok()
	case cmdExit:
		return ok(), true
	case cmdPrepareBuffer:
		which := binary.LittleEndian.Uint16(pk.data[1:])
		switch {
		case which == cmdUsertempRRQ:
			replies = [][]byte{rep(cmdAckData, f.usersBlob)}
		case which == cmdAttlogRRQ && f.chunkedAttendace:
			d := binary.LittleEndian.AppendUint32([]byte{0}, uint32(len(f.attBlob)))
			replies = [][]byte{rep(cmdAckOK, d)}
		case which == cmdAttlogRRQ:
			replies = [][]byte{rep(cmdAckData, f.attBlob)}
		default:
			replies = [][]byte{rep(cmdAckError, nil)}
		}
	case cmdReadBuffer:
		start := int(binary.LittleEndian.Uint32(pk.data))
		size := int(binary.LittleEndian.Uint32(pk.data[4:]))
		end := min(start+size, len(f.attBlob))
		chunk := f.attBlob[start:end]
		mid := len(chunk) / 2
		replies = [][]byte{
			rep(cmdPrepareData, binary.LittleEndian.AppendUint32(nil, uint32(len(chunk)))),
			rep(cmdData, chunk[:mid]),
			rep(cmdData, chunk[mid:]),
			rep(cmdAckOK, nil),
		}
	default:
		replies = [][]byte{rep(cmdAckError, nil)}
	}
	return replies, false
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("fake device did not finish")
	}
}

func spawnTCP(t *testing.T, f *fake) (int, <-chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	t.Cleanup(func() { l.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		for {
			var pre [8]byte
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.ReadFull(c, pre[:]); err != nil {
				return
			}
			if [4]byte(pre[:4]) != tcpMagic {
				t.Errorf("bad magic % x", pre[:4])
				return
			}
			payload := make([]byte, binary.LittleEndian.Uint32(pre[4:]))
			if _, err := io.ReadFull(c, payload); err != nil {
				return
			}
			replies, exit := f.handle(payload)
			// all replies back-to-back in one write
			var wire []byte
			for _, r := range replies {
				wire = append(wire, tcpFrame(r)...)
			}
			if _, err := c.Write(wire); err != nil || exit {
				return
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, done
}

func spawnUDP(t *testing.T, f *fake) (int, <-chan struct{}) {
	t.Helper()
	sock, err := net.ListenPacket("udp", "127.0.0.1:0")
	mustNoErr(t, err)
	t.Cleanup(func() { sock.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65536)
		for {
			_ = sock.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, from, err := sock.ReadFrom(buf)
			if err != nil {
				return
			}
			replies, exit := f.handle(buf[:n])
			for _, r := range replies {
				if _, err := sock.WriteTo(r, from); err != nil {
					return
				}
			}
			if exit {
				return
			}
		}
	}()
	return sock.LocalAddr().(*net.UDPAddr).Port, done
}

func dev(port int, tr core.Transport, commKey int) core.Device {
	return core.Device{ID: 7, Name: "ทดสอบ", IP: "127.0.0.1", Port: port, CommKey: commKey, Transport: tr, TimeoutSec: 5}
}

func checkSnapshot(t *testing.T, snap core.Snapshot) {
	t.Helper()
	in := snap.Info
	if in.Serial != "FAKE-SN-001" || in.Firmware != "Ver 6.60 Sep 2 2019" || in.Platform != "ZMM220_TFT" || in.DeviceName != "FakeDevice" {
		t.Errorf("info %+v", in)
	}
	if in.DeviceTime == nil || !in.DeviceTime.Equal(t0()) {
		t.Errorf("device time %v", in.DeviceTime)
	}
	if in.UserCount == nil || *in.UserCount != 2 || in.RecordCount == nil || *in.RecordCount != 3 {
		t.Errorf("counts %v %v", in.UserCount, in.RecordCount)
	}
	if len(snap.Users) != 2 || snap.Users[0].Name != "สมชาย" || snap.Users[0].Privilege != 14 || snap.Users[1].Card != 222222 {
		t.Fatalf("users %+v", snap.Users)
	}
	for _, u := range snap.Users {
		if u.DeviceID != 0 || u.DeviceName != "" {
			t.Errorf("user device fields must stay zero: %+v", u)
		}
	}
	r := snap.Records
	if len(r) != 3 {
		t.Fatalf("records %d", len(r))
	}
	for i, x := range r {
		if x.DeviceID != 0 || x.DeviceName != "" {
			t.Errorf("record device fields must stay zero: %+v", x)
		}
		if i > 0 && x.Timestamp.Before(r[i-1].Timestamp) {
			t.Error("records not sorted")
		}
	}
	if !r[0].Timestamp.Equal(dt(2026, 11, 3, 8, 0, 5)) || r[0].UserID != "1001" || r[0].Name != "สมชาย" {
		t.Errorf("r0 %+v", r[0])
	}
	if r[1].Name != "สุดา" || r[1].VerifyMode != 2 {
		t.Errorf("r1 %+v", r[1])
	}
	if !r[2].Timestamp.Equal(dt(2026, 11, 3, 17, 30, 0)) || r[2].PunchState != 1 {
		t.Errorf("r2 %+v", r[2])
	}
}

func deviceErr(t *testing.T, err error) *core.DeviceError {
	t.Helper()
	var de *core.DeviceError
	if !errors.As(err, &de) {
		t.Fatalf("want *core.DeviceError, got %T: %v", err, err)
	}
	return de
}

func indexOf(cmds []uint16, c uint16) int { return slices.Index(cmds, c) }

// ---- tests (port of client.rs tests) ----

func TestTCPFetchAllEndToEndChunked(t *testing.T) {
	f := newFake(nil, true)
	port, done := spawnTCP(t, f)
	snap, err := New().Fetch(context.Background(), dev(port, core.TransportTCP, 0))
	mustNoErr(t, err)
	waitDone(t, done)
	checkSnapshot(t, snap)
	cmds := f.seen.cmds()
	if cmds[0] != cmdConnect {
		t.Error("first command must be CONNECT")
	}
	if !(indexOf(cmds, cmdDisableDevice) >= 0 && indexOf(cmds, cmdDisableDevice) < indexOf(cmds, cmdPrepareBuffer)) {
		t.Errorf("DISABLE must come before bulk reads: %v", cmds)
	}
	if indexOf(cmds, cmdReadBuffer) < 0 || indexOf(cmds, cmdFreeData) < 0 {
		t.Errorf("chunked read + free data expected: %v", cmds)
	}
	n := len(cmds)
	if !slices.Equal(cmds[n-2:], []uint16{cmdEnableDevice, cmdExit}) {
		t.Errorf("must end with ENABLE, EXIT: %v", cmds)
	}
}

func TestFakeReceivesPyzkStyleConnect(t *testing.T) {
	// the very first bytes on the wire are pyzk's CONNECT packet
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	defer l.Close()
	got := make(chan [16]byte, 1)
	release := make(chan struct{})
	defer close(release)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var buf [16]byte
		if _, err := io.ReadFull(c, buf[:]); err == nil {
			got <- buf
		}
		<-release
	}()
	d := dev(l.Addr().(*net.TCPAddr).Port, core.TransportTCP, 0)
	d.TimeoutSec = 1
	_, _ = New().Fetch(context.Background(), d)
	select {
	case b := <-got:
		want := [16]byte{0x50, 0x50, 0x82, 0x7D, 8, 0, 0, 0, 0xE8, 0x03, 0x17, 0xFC, 0, 0, 0, 0}
		if b != want {
			t.Errorf("got % x want % x", b, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("device saw nothing")
	}
}

func TestTCPEmptyDeviceSkipsBulkReads(t *testing.T) {
	f := newFake(nil, true)
	f.counts = [2]int32{0, 0}
	f.usersBlob, f.attBlob = withSize(), withSize()
	port, done := spawnTCP(t, f)
	snap, err := New().Fetch(context.Background(), dev(port, core.TransportTCP, 0))
	mustNoErr(t, err)
	waitDone(t, done)
	if len(snap.Users) != 0 || len(snap.Records) != 0 {
		t.Error("expected no users/records")
	}
	if snap.Info.RecordCount == nil || *snap.Info.RecordCount != 0 {
		t.Errorf("record count %v", snap.Info.RecordCount)
	}
	cmds := f.seen.cmds()
	if indexOf(cmds, cmdPrepareBuffer) >= 0 {
		t.Errorf("no bulk read expected: %v", cmds)
	}
	if indexOf(cmds, cmdDisableDevice) < 0 || !slices.Equal(cmds[len(cmds)-2:], []uint16{cmdEnableDevice, cmdExit}) {
		t.Errorf("cmds %v", cmds)
	}
}

func TestTCPCommKeyAuth(t *testing.T) {
	f := newFake(u32p(123456), false)
	port, done := spawnTCP(t, f)
	snap, err := New().Fetch(context.Background(), dev(port, core.TransportTCP, 123456))
	mustNoErr(t, err)
	waitDone(t, done)
	checkSnapshot(t, snap)
	want := makeCommKey(123456, fakeSession, 50)
	f.seen.mu.Lock()
	defer f.seen.mu.Unlock()
	if !bytes.Equal(f.seen.auth, want[:]) {
		t.Errorf("auth % x want % x", f.seen.auth, want)
	}
}

func TestTCPWrongCommKeyIsUnauthorized(t *testing.T) {
	f := newFake(u32p(123456), false)
	port, done := spawnTCP(t, f)
	_, err := New().Fetch(context.Background(), dev(port, core.TransportTCP, 999))
	if de := deviceErr(t, err); de.Kind != core.DevErrUnauthorized || de.Hint != hintUnauthorized {
		t.Errorf("got %+v", de)
	}
	waitDone(t, done)
}

func TestTCPTestConnectionAndSetTime(t *testing.T) {
	f := newFake(nil, false)
	port, done := spawnTCP(t, f)
	info, err := New().TestConnection(context.Background(), dev(port, core.TransportTCP, 0))
	mustNoErr(t, err)
	waitDone(t, done)
	if info.Serial != "FAKE-SN-001" || info.DeviceTime == nil || !info.DeviceTime.Equal(t0()) || info.RecordCount == nil || *info.RecordCount != 3 {
		t.Errorf("info %+v", info)
	}

	f = newFake(nil, false)
	port, done = spawnTCP(t, f)
	tm := dt(2027, 1, 2, 3, 4, 5)
	mustNoErr(t, New().SetTime(context.Background(), dev(port, core.TransportTCP, 0), tm))
	waitDone(t, done)
	f.seen.mu.Lock()
	defer f.seen.mu.Unlock()
	if f.seen.setTime == nil || *f.seen.setTime != encodeTime(tm) {
		t.Errorf("set time %v", f.seen.setTime)
	}
}

func TestUDPFetchAllAckData(t *testing.T) {
	f := newFake(nil, false)
	port, done := spawnUDP(t, f)
	snap, err := New().Fetch(context.Background(), dev(port, core.TransportUDP, 0))
	mustNoErr(t, err)
	waitDone(t, done)
	checkSnapshot(t, snap)
}

func TestUDPChunkedAttendanceAndTime(t *testing.T) {
	f := newFake(nil, true)
	port, done := spawnUDP(t, f)
	s, err := openSession(context.Background(), dev(port, core.TransportUDP, 0))
	mustNoErr(t, err)
	defer s.close()
	mustNoErr(t, s.connect(0))
	if tm := s.getTime(); tm == nil || !tm.Equal(t0()) {
		t.Errorf("time %v", tm)
	}
	users, err := s.readUsers(ip(2))
	mustNoErr(t, err)
	recs, err := s.readAttendance(users, ip(3))
	mustNoErr(t, err)
	if len(recs) != 3 {
		t.Errorf("records %d", len(recs))
	}
	mustNoErr(t, s.exit())
	waitDone(t, done)
}

func TestClosedPortFailsWithinTimeout(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	d := dev(port, core.TransportTCP, 0)
	d.TimeoutSec = 2
	start := time.Now()
	_, err = New().Fetch(context.Background(), d)
	if err == nil {
		t.Error("expected an error")
	}
	deviceErr(t, err)
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("took %v", el)
	}
}

func TestSilentDeviceTimesOut(t *testing.T) {
	// accepts the connection but never answers
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	defer l.Close()
	release := make(chan struct{})
	defer close(release)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		<-release
	}()
	d := dev(l.Addr().(*net.TCPAddr).Port, core.TransportTCP, 0)
	d.TimeoutSec = 1
	start := time.Now()
	_, err = New().Fetch(context.Background(), d)
	if de := deviceErr(t, err); de.Kind != core.DevErrTimeout || de.Hint != hintTimeout {
		t.Errorf("got %+v", de)
	}
	if el := time.Since(start); el >= 3*time.Second {
		t.Errorf("took %v", el)
	}
}

func TestGarbledReplyIsProtocolError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 64)
		_, _ = c.Read(buf)
		_, _ = c.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	}()
	_, err = New().Fetch(context.Background(), dev(l.Addr().(*net.TCPAddr).Port, core.TransportTCP, 0))
	if de := deviceErr(t, err); de.Kind != core.DevErrProtocol {
		t.Errorf("got %+v", de)
	}
	waitDone(t, done)
}

// ---- context cancellation (new in the Go port) ----

// stallTCP answers CONNECT then goes silent, so cancellation lands mid-session.
func stallTCP(t *testing.T, answerConnect bool) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	release := make(chan struct{})
	t.Cleanup(func() { close(release); l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if answerConnect {
			buf := make([]byte, 16)
			if _, err := io.ReadFull(c, buf); err == nil {
				_, _ = c.Write(tcpFrame(buildPacket(cmdAckOK, fakeSession, 0, nil)))
			}
		}
		<-release
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestCancelledContextReturnsPromptly(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	mustNoErr(t, err)
	defer udp.Close()
	cases := map[string]core.Device{
		"tcp silent":               dev(stallTCP(t, false), core.TransportTCP, 0),
		"tcp silent after connect": dev(stallTCP(t, true), core.TransportTCP, 0),
		"udp silent":               dev(udp.LocalAddr().(*net.UDPAddr).Port, core.TransportUDP, 0),
	}
	for name, d := range cases {
		d.TimeoutSec = 30 // far longer than the test
		for _, op := range []string{"fetch", "test", "settime"} {
			t.Run(name+"/"+op, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(150*time.Millisecond, cancel)
				defer cancel()
				start := time.Now()
				var err error
				switch op {
				case "fetch":
					_, err = New().Fetch(ctx, d)
				case "test":
					_, err = New().TestConnection(ctx, d)
				default:
					err = New().SetTime(ctx, d, time.Now())
				}
				if el := time.Since(start); el > time.Second {
					t.Errorf("returned after %v", el)
				}
				de := deviceErr(t, err)
				if !errors.Is(de, context.Canceled) {
					t.Errorf("error should wrap context.Canceled: %+v", de)
				}
			})
		}
	}
}

func TestContextDeadlineAndAlreadyCancelled(t *testing.T) {
	d := dev(stallTCP(t, true), core.TransportTCP, 0)
	d.TimeoutSec = 30
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New().Fetch(ctx, d)
	if el := time.Since(start); el > time.Second {
		t.Errorf("deadline ignored: %v", el)
	}
	if de := deviceErr(t, err); de.Kind != core.DevErrTimeout || !errors.Is(de, context.DeadlineExceeded) {
		t.Errorf("got %+v", de)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	start = time.Now()
	_, err = New().Fetch(ctx2, d)
	deviceErr(t, err)
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Errorf("pre-cancelled ctx took %v", el)
	}
}

func TestOversizedFrameIsRejected(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	mustNoErr(t, err)
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 16)
		_, _ = io.ReadFull(c, buf)
		hdr := append(tcpMagic[:], binary.LittleEndian.AppendUint32(nil, maxFrame+1)...)
		_, _ = c.Write(hdr)
		time.Sleep(200 * time.Millisecond)
	}()
	_, err = New().Fetch(context.Background(), dev(l.Addr().(*net.TCPAddr).Port, core.TransportTCP, 0))
	if de := deviceErr(t, err); de.Kind != core.DevErrProtocol {
		t.Errorf("got %+v", de)
	}
}
