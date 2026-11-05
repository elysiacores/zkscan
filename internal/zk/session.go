package zk

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"zkscan/internal/core"
)

const (
	maxTCPChunk = 0xFFC0
	maxUDPChunk = 16 * 1024
	// maxFrame bounds one TCP frame / one whole buffer chunk (guards against garbage lengths).
	maxFrame  = 16 * 1024 * 1024
	maxBuffer = 256 * 1024 * 1024
	// cleanupTimeout is the I/O timeout used while cleaning up (ENABLEDEVICE / EXIT) after an error.
	cleanupTimeout = 2 * time.Second
)

// reply is one command reply.
type reply struct {
	command   uint16
	sessionID uint16
	data      []byte
}

// session is one open connection to a device (the Rust ZkClient). Cancelling ctx closes the
// connection, which unblocks any pending I/O at once.
type session struct {
	ctx       context.Context
	nc        net.Conn
	tcp       bool
	timeout   time.Duration
	stop      func() bool
	sessionID uint16
	replyID   uint16
	recvBuf   []byte
}

// fail converts an I/O error into a *core.DeviceError (ctx cancellation wins).
func (s *session) fail(err error) error { return toDeviceError(s.ctx, err) }

func deviceTimeout(d core.Device) time.Duration {
	return time.Duration(max(d.TimeoutSec, 1)) * time.Second
}

// openSession opens the socket (no ZK handshake yet).
func openSession(ctx context.Context, d core.Device) (*session, error) {
	timeout := deviceTimeout(d)
	network := "tcp"
	if d.Transport == core.TransportUDP {
		network = "udp"
	}
	addr := net.JoinHostPort(strings.TrimSpace(d.IP), strconv.Itoa(d.Port))
	dialer := net.Dialer{Timeout: timeout}
	nc, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, toDeviceError(ctx, err)
	}
	s := &session{
		ctx:     ctx,
		nc:      nc,
		tcp:     network == "tcp",
		timeout: timeout,
		replyID: ushrtMax - 1,
		recvBuf: make([]byte, 65536),
	}
	s.stop = context.AfterFunc(ctx, func() { _ = nc.Close() })
	return s, nil
}

// close always releases the connection.
func (s *session) close() {
	s.stop()
	_ = s.nc.Close()
}

func (s *session) maxChunk() int {
	if s.tcp {
		return maxTCPChunk
	}
	return maxUDPChunk
}

func (s *session) sendPacket(command uint16, data []byte) error {
	if err := s.ctx.Err(); err != nil {
		return s.fail(err)
	}
	// pyzk-compatible: checksum over the pre-increment reply id, incremented id on the wire
	payload := buildRequest(command, s.sessionID, s.replyID, data)
	s.replyID = nextReplyID(s.replyID)
	if s.tcp {
		payload = tcpFrame(payload)
	}
	_ = s.nc.SetWriteDeadline(time.Now().Add(s.timeout))
	if _, err := s.nc.Write(payload); err != nil {
		return s.fail(err)
	}
	return nil
}

func (s *session) readFull(buf []byte) error {
	if err := s.ctx.Err(); err != nil {
		return s.fail(err)
	}
	_ = s.nc.SetReadDeadline(time.Now().Add(s.timeout))
	if _, err := io.ReadFull(s.nc, buf); err != nil {
		return s.fail(err)
	}
	return nil
}

func (s *session) recvPayload() ([]byte, error) {
	if !s.tcp {
		if err := s.ctx.Err(); err != nil {
			return nil, s.fail(err)
		}
		_ = s.nc.SetReadDeadline(time.Now().Add(s.timeout))
		n, err := s.nc.Read(s.recvBuf)
		if err != nil {
			return nil, s.fail(err)
		}
		return append([]byte(nil), s.recvBuf[:n]...), nil
	}
	var prefix [8]byte
	if err := s.readFull(prefix[:]); err != nil {
		return nil, err
	}
	if [4]byte(prefix[:4]) != tcpMagic {
		return nil, protoErr("ไม่พบหัวแพ็กเก็ต TCP")
	}
	n := int(binary.LittleEndian.Uint32(prefix[4:]))
	if n < headerLen || n > maxFrame {
		return nil, protoErr(fmt.Sprintf("ความยาวแพ็กเก็ตผิดปกติ (%d)", n))
	}
	buf := make([]byte, n)
	if err := s.readFull(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (s *session) recvReply() (reply, error) {
	payload, err := s.recvPayload()
	if err != nil {
		return reply{}, err
	}
	p, err := parsePacket(payload)
	if err != nil {
		return reply{}, err
	}
	return reply{command: p.header.command, sessionID: p.header.sessionID, data: p.data}, nil
}

// exchange sends one command and reads one reply (no interpretation).
func (s *session) exchange(command uint16, data []byte) (reply, error) {
	if err := s.sendPacket(command, data); err != nil {
		return reply{}, err
	}
	return s.recvReply()
}

// sendCommand sends one command and reads one reply; ACK_UNAUTH becomes an Unauthorized error.
func (s *session) sendCommand(command uint16, data []byte) (reply, error) {
	r, err := s.exchange(command, data)
	if err != nil {
		return reply{}, err
	}
	if r.command == cmdAckUnauth {
		return reply{}, unauthorizedErr()
	}
	return r, nil
}

func (s *session) expectOK(command uint16, data []byte, what string) (reply, error) {
	r, err := s.sendCommand(command, data)
	if err != nil {
		return reply{}, err
	}
	if r.command != cmdAckOK {
		return reply{}, protoErr(fmt.Sprintf("%s: เครื่องตอบ %d", what, r.command))
	}
	return r, nil
}

// connect does CONNECT (+ AUTH when the device demands the comm key).
func (s *session) connect(commKey uint32) error {
	r, err := s.exchange(cmdConnect, nil)
	if err != nil {
		return err
	}
	s.sessionID = r.sessionID
	switch r.command {
	case cmdAckOK:
		return nil
	case cmdAckUnauth:
		key := makeCommKey(commKey, s.sessionID, 50)
		r2, err := s.exchange(cmdAuth, key[:])
		if err != nil {
			return err
		}
		if r2.command == cmdAckOK {
			return nil
		}
		return unauthorizedErr()
	default:
		return protoErr(fmt.Sprintf("CONNECT: เครื่องตอบ %d", r.command))
	}
}

func (s *session) exit() error {
	_, err := s.sendCommand(cmdExit, nil)
	return err
}

// disableDevice: only transport errors are reported, a refusal is ignored.
func (s *session) disableDevice() error {
	_, err := s.sendCommand(cmdDisableDevice, nil)
	return err
}

func (s *session) enableDevice() error {
	_, err := s.sendCommand(cmdEnableDevice, nil)
	return err
}

func isAckOKOrData(c uint16) bool { return c == cmdAckOK || c == cmdAckData }

func (s *session) readOption(name string) string {
	r, err := s.sendCommand(cmdOptionsRRQ, optionRequest(name))
	if err != nil || !isAckOKOrData(r.command) {
		return ""
	}
	return parseOptionsValue(r.data)
}

func (s *session) readFirmware() string {
	r, err := s.sendCommand(cmdGetVersion, nil)
	if err != nil || !isAckOKOrData(r.command) {
		return ""
	}
	return cstr(r.data)
}

func (s *session) getTime() *time.Time {
	r, err := s.sendCommand(cmdGetTime, nil)
	if err != nil || len(r.data) < 4 || !isAckOKOrData(r.command) {
		return nil
	}
	t, ok := decodeTime(binary.LittleEndian.Uint32(r.data))
	if !ok {
		return nil
	}
	return &t
}

func (s *session) setTime(t time.Time) error {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], encodeTime(t.In(time.Local)))
	_, err := s.expectOK(cmdSetTime, b[:], "SET_TIME")
	return err
}

// getSizes returns (users, records) from GET_FREE_SIZES.
func (s *session) getSizes() (*int, *int) {
	r, err := s.sendCommand(cmdGetFreeSizes, nil)
	if err != nil || !isAckOKOrData(r.command) {
		return nil, nil
	}
	return parseFreeSizes(r.data)
}

// readInfo reads serial, firmware, platform, name, clock and counts. Individual failures leave fields empty.
func (s *session) readInfo() core.DeviceInfo {
	info := core.DeviceInfo{}
	info.Serial = s.readOption("SerialNumber")
	info.Firmware = s.readFirmware()
	info.Platform = s.readOption("Platform")
	info.DeviceName = s.readOption("DeviceName")
	info.DeviceTime = s.getTime()
	info.UserCount, info.RecordCount = s.getSizes()
	return info
}

// readChunk does one READ_BUFFER request.
func (s *session) readChunk(start, size int) ([]byte, error) {
	var req [8]byte
	binary.LittleEndian.PutUint32(req[0:], uint32(int32(start)))
	binary.LittleEndian.PutUint32(req[4:], uint32(int32(size)))
	if err := s.sendPacket(cmdReadBuffer, req[:]); err != nil {
		return nil, err
	}
	r, err := s.recvReply()
	if err != nil {
		return nil, err
	}
	switch r.command {
	case cmdData:
		return r.data, nil
	case cmdPrepareData:
		if len(r.data) < 4 {
			return nil, protoErr("PREPARE_DATA สั้นเกินไป")
		}
		total := int(binary.LittleEndian.Uint32(r.data))
		if total > maxBuffer {
			return nil, protoErr("ขนาดข้อมูลใหญ่ผิดปกติ")
		}
		buf := make([]byte, 0, min(total, maxFrame))
		for len(buf) < total {
			p, err := s.recvReply()
			if err != nil {
				return nil, err
			}
			if p.command != cmdData {
				return nil, protoErr(fmt.Sprintf("คาดว่าจะได้ DATA แต่ได้ %d", p.command))
			}
			if len(p.data) == 0 {
				return nil, protoErr("DATA ว่างเปล่า")
			}
			buf = append(buf, p.data...)
		}
		buf = buf[:total]
		// trailing ACK_OK of the transfer
		ack, err := s.recvReply()
		if err != nil {
			return nil, err
		}
		if ack.command == cmdAckUnauth {
			return nil, unauthorizedErr()
		}
		return buf, nil
	case cmdAckUnauth:
		return nil, unauthorizedErr()
	default:
		return nil, protoErr(fmt.Sprintf("READ_BUFFER: เครื่องตอบ %d", r.command))
	}
}

// readWithBuffer is the bulk read via PREPARE_BUFFER / READ_BUFFER / FREE_DATA.
func (s *session) readWithBuffer(command uint16, fct, ext int32) ([]byte, error) {
	r, err := s.sendCommand(cmdPrepareBuffer, prepareBufferData(command, fct, ext))
	if err != nil {
		return nil, err
	}
	switch r.command {
	case cmdAckData, cmdData:
		return r.data, nil
	case cmdAckOK:
		if len(r.data) < 5 {
			return nil, protoErr("PREPARE_BUFFER: ไม่พบขนาดข้อมูล")
		}
		size := int(binary.LittleEndian.Uint32(r.data[1:]))
		if size > maxBuffer {
			return nil, protoErr("ขนาดข้อมูลใหญ่ผิดปกติ")
		}
		maxC := s.maxChunk()
		out := make([]byte, 0, min(size, maxFrame))
		var result error
		for len(out) < size {
			want := min(size-len(out), maxC)
			chunk, err := s.readChunk(len(out), want)
			if err != nil {
				result = err
				break
			}
			if len(chunk) == 0 {
				result = protoErr("เครื่องส่งข้อมูลว่างเปล่า")
				break
			}
			out = append(out, chunk...)
		}
		// free the device buffer, failure ignored
		_, _ = s.exchange(cmdFreeData, nil)
		if result != nil {
			return nil, result
		}
		return out[:size], nil
	default:
		return nil, protoErr(fmt.Sprintf("PREPARE_BUFFER: เครื่องตอบ %d", r.command))
	}
}

func (s *session) readUsers(count *int) ([]core.DeviceUser, error) {
	b, err := s.readWithBuffer(cmdUsertempRRQ, fctUser, 0)
	if err != nil {
		return nil, err
	}
	return parseUsers(b, count)
}

func (s *session) readAttendance(users []core.DeviceUser, count *int) ([]core.Attendance, error) {
	b, err := s.readWithBuffer(cmdAttlogRRQ, 0, 0)
	if err != nil {
		return nil, err
	}
	return parseAttendance(b, users, count)
}
