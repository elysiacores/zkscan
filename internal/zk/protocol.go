// Package zk talks to ZKTeco attendance devices (TCP/UDP) and provides a deterministic demo device.
//
// protocol.go holds the pure packet encoding/decoding functions (no I/O).
package zk

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"zkscan/internal/core"
)

// ---- commands -------------------------------------------------------------
const (
	cmdConnect       uint16 = 1000
	cmdExit          uint16 = 1001
	cmdEnableDevice  uint16 = 1002
	cmdDisableDevice uint16 = 1003
	cmdAuth          uint16 = 1102
	cmdAckOK         uint16 = 2000
	cmdAckError      uint16 = 2001
	cmdAckData       uint16 = 2002
	cmdAckUnauth     uint16 = 2005
	cmdPrepareData   uint16 = 1500
	cmdData          uint16 = 1501
	cmdFreeData      uint16 = 1502
	cmdPrepareBuffer uint16 = 1503
	cmdReadBuffer    uint16 = 1504
	cmdGetFreeSizes  uint16 = 50
	cmdOptionsRRQ    uint16 = 11
	cmdGetTime       uint16 = 201
	cmdSetTime       uint16 = 202
	cmdAttlogRRQ     uint16 = 13
	cmdUsertempRRQ   uint16 = 9
	cmdGetVersion    uint16 = 1100

	fctUser int32 = 5
)

// tcpMagic is the prefix of every TCP frame.
var tcpMagic = [4]byte{0x50, 0x50, 0x82, 0x7D}

const (
	headerLen = 8
	ushrtMax  = 65535
)

// ---- checksum / packets ---------------------------------------------------

// checksum is the ZK 16-bit one's-complement style checksum over buf (checksum field must be zero).
func checksum(buf []byte) uint16 {
	var c int64
	pairEnd := len(buf) / 2 * 2
	for i := 0; i < pairEnd; i += 2 {
		c += int64(le16(buf, i))
		if c > 65535 {
			c -= 65535
		}
	}
	if len(buf)%2 == 1 {
		c += int64(buf[len(buf)-1])
	}
	for c > 65535 {
		c -= 65535
	}
	c = ^c
	for c < 0 {
		c += 65535
	}
	return uint16(c)
}

// nextReplyID increments the reply id and wraps at 65535.
func nextReplyID(id uint16) uint16 {
	n := uint32(id) + 1
	if n >= ushrtMax {
		n -= ushrtMax
	}
	return uint16(n)
}

// buildPacket builds header + data with a valid checksum (no TCP prefix).
func buildPacket(command, sessionID, replyID uint16, data []byte) []byte {
	buf := make([]byte, headerLen, headerLen+len(data))
	binary.LittleEndian.PutUint16(buf[0:], command)
	binary.LittleEndian.PutUint16(buf[4:], sessionID)
	binary.LittleEndian.PutUint16(buf[6:], replyID)
	buf = append(buf, data...)
	binary.LittleEndian.PutUint16(buf[2:], checksum(buf))
	return buf
}

// buildRequest builds a request exactly like pyzk / node-zklib: the checksum is computed over a header
// holding the reply id BEFORE incrementing, while the header that is sent carries the incremented id
// (the checksum is not recomputed). Real devices accept this; they do not verify the checksum.
func buildRequest(command, sessionID, prevReplyID uint16, data []byte) []byte {
	buf := buildPacket(command, sessionID, prevReplyID, data)
	binary.LittleEndian.PutUint16(buf[6:], nextReplyID(prevReplyID))
	return buf
}

// tcpFrame wraps a payload in the TCP frame (magic + u32 length).
func tcpFrame(payload []byte) []byte {
	out := make([]byte, 0, 8+len(payload))
	out = append(out, tcpMagic[:]...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
	return append(out, payload...)
}

type header struct {
	command   uint16
	checksum  uint16
	sessionID uint16
	replyID   uint16
}

func parseHeader(payload []byte) (header, error) {
	if len(payload) < headerLen {
		return header{}, protoErr(fmt.Sprintf("แพ็กเก็ตสั้นเกินไป (%d ไบต์)", len(payload)))
	}
	return header{
		command:   le16(payload, 0),
		checksum:  le16(payload, 2),
		sessionID: le16(payload, 4),
		replyID:   le16(payload, 6),
	}, nil
}

type packet struct {
	header header
	data   []byte
}

func parsePacket(payload []byte) (packet, error) {
	h, err := parseHeader(payload)
	if err != nil {
		return packet{}, err
	}
	return packet{header: h, data: append([]byte(nil), payload[headerLen:]...)}, nil
}

// verifyChecksum is true when the checksum stored in payload matches the recomputed one.
func verifyChecksum(payload []byte) bool {
	h, err := parseHeader(payload)
	if err != nil {
		return false
	}
	cp := append([]byte(nil), payload...)
	cp[2], cp[3] = 0, 0
	return checksum(cp) == h.checksum
}

// makeCommKey is the comm-key scrambling used by the AUTH command.
func makeCommKey(key uint32, sessionID uint16, ticks byte) [4]byte {
	var k uint32
	for i := 0; i < 32; i++ {
		if key&(1<<uint(i)) != 0 {
			k = (k << 1) | 1
		} else {
			k <<= 1
		}
	}
	k += uint32(sessionID)
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], k)
	b = [4]byte{b[0] ^ 'Z', b[1] ^ 'K', b[2] ^ 'S', b[3] ^ 'O'}
	b = [4]byte{b[2], b[3], b[0], b[1]}
	return [4]byte{b[0] ^ ticks, b[1] ^ ticks, ticks, b[3] ^ ticks}
}

// ---- time -----------------------------------------------------------------

// decodeTime converts device time (seconds in the ZK packed calendar) to a time.Local date-time;
// ok is false when the packed value is not a valid date.
func decodeTime(t uint32) (time.Time, bool) {
	v := uint64(t)
	s := v % 60
	v /= 60
	mi := v % 60
	v /= 60
	h := v % 24
	v /= 24
	d := v%31 + 1
	v /= 31
	mo := v%12 + 1
	v /= 12
	y := v + 2000
	// validate the calendar date independent of the local zone (DST gaps etc.)
	chk := time.Date(int(y), time.Month(mo), int(d), 0, 0, 0, 0, time.UTC)
	if chk.Year() != int(y) || chk.Month() != time.Month(mo) || chk.Day() != int(d) {
		return time.Time{}, false
	}
	return time.Date(int(y), time.Month(mo), int(d), int(h), int(mi), int(s), 0, time.Local), true
}

// encodeTime converts the wall-clock fields of t to the ZK packed calendar.
func encodeTime(t time.Time) uint32 {
	y := uint64(((t.Year() % 100) + 100) % 100)
	mo := uint64(t.Month())
	d := uint64(t.Day())
	days := y*12*31 + (mo-1)*31 + (d - 1)
	secs := (uint64(t.Hour())*60+uint64(t.Minute()))*60 + uint64(t.Second())
	return uint32(days*86400 + secs)
}

// ---- text helpers -----------------------------------------------------------

func untilNUL(b []byte) []byte {
	for i, c := range b {
		if c == 0 {
			return b[:i]
		}
	}
	return b
}

// cstr is the bytes up to the first NUL, decoded as UTF-8 (lossy when invalid), trimmed.
func cstr(b []byte) string {
	return strings.TrimSpace(strings.ToValidUTF8(string(untilNUL(b)), "�"))
}

// nameStr decodes a fixed-width name field: bytes up to the first NUL. UTF-8 when valid (an incomplete
// multi-byte sequence cut off at the very end of the field is dropped); otherwise TIS-620
// (Windows-874), which Thai devices commonly use: ASCII below 0x80, 0xA1..=0xFB are
// U+0E01.., any other byte is skipped.
func nameStr(bytes []byte) string {
	b := untilNUL(bytes)
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			if !utf8.FullRune(b[i:]) {
				// only an incomplete sequence at the very end
				return strings.TrimSpace(string(b[:i]))
			}
			return tis620(b)
		}
		i += size
	}
	return strings.TrimSpace(string(b))
}

func tis620(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c <= 0x7F:
			sb.WriteRune(rune(c))
		case c >= 0xA1 && c <= 0xFB:
			sb.WriteRune(rune(0x0E01 + int(c-0xA1)))
		}
	}
	return strings.TrimSpace(sb.String())
}

// parseOptionsValue turns an OPTIONS_RRQ reply like "~SerialNumber=ABC123\0..." into "ABC123".
// Without '=' the whole text up to NUL is returned.
func parseOptionsValue(data []byte) string {
	rest := data
	for i, c := range data {
		if c == '=' {
			rest = data[i+1:]
			break
		}
	}
	return cstr(rest)
}

// optionRequest is the data for OPTIONS_RRQ, e.g. optionRequest("SerialNumber") = "~SerialNumber\0".
func optionRequest(name string) []byte {
	v := make([]byte, 0, len(name)+2)
	v = append(v, '~')
	v = append(v, name...)
	return append(v, 0)
}

// parseFreeSizes turns a GET_FREE_SIZES reply into (users, records); nil when unknown.
func parseFreeSizes(data []byte) (users, records *int) {
	if len(data) < 80 {
		return nil, nil
	}
	get := func(i int) *int {
		v := int32(binary.LittleEndian.Uint32(data[i*4:]))
		if v < 0 {
			return nil
		}
		n := int(v)
		return &n
	}
	return get(4), get(8)
}

// prepareBufferData is the data for PREPARE_BUFFER.
func prepareBufferData(command uint16, fct, ext int32) []byte {
	v := make([]byte, 0, 11)
	v = append(v, 1)
	v = binary.LittleEndian.AppendUint16(v, command)
	v = binary.LittleEndian.AppendUint32(v, uint32(fct))
	v = binary.LittleEndian.AppendUint32(v, uint32(ext))
	return v
}

// ---- bulk record parsing ------------------------------------------------------

// sizedBody splits "u32 size + body": returns the body clamped to the bytes actually present.
func sizedBody(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return b, nil
	}
	if len(b) < 4 {
		return nil, protoErr("ข้อมูลสั้นเกินไป")
	}
	size := int(binary.LittleEndian.Uint32(b))
	body := b[4:]
	if size > len(body) {
		size = len(body)
	}
	return body[:size], nil
}

func pickEntrySize(total int, count *int, candidates []int) (int, error) {
	if count != nil && *count > 0 {
		n := *count
		if total%n == 0 {
			for _, c := range candidates {
				if c == total/n {
					return c, nil
				}
			}
		}
	}
	for _, c := range candidates {
		if total%c == 0 {
			return c, nil
		}
	}
	return 0, protoErr(fmt.Sprintf("ขนาดข้อมูลไม่ตรงรูปแบบที่รู้จัก (%d ไบต์)", total))
}

func le16(b []byte, i int) uint16 { return binary.LittleEndian.Uint16(b[i:]) }
func le32(b []byte, i int) uint32 { return binary.LittleEndian.Uint32(b[i:]) }

// parseUsers parses the USERTEMP_RRQ buffer (u32 size + entries of 28 or 72 bytes).
// DeviceID / DeviceName are left zero.
func parseUsers(b []byte, count *int) ([]core.DeviceUser, error) {
	body, err := sizedBody(b)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return []core.DeviceUser{}, nil
	}
	size, err := pickEntrySize(len(body), count, []int{72, 28})
	if err != nil {
		return nil, err
	}
	out := make([]core.DeviceUser, 0, len(body)/size)
	for off := 0; off+size <= len(body); off += size {
		e := body[off : off+size]
		uid := int(le16(e, 0))
		privilege := int(e[2])
		var name, userID string
		var card uint32
		if size == 28 {
			// uid(2) priv(1) password(5) name(8) card(4) pad(1) group(1) tz(2) user_id(4)
			name, card, userID = nameStr(e[8:16]), le32(e, 16), fmt.Sprint(le32(e, 24))
		} else {
			// uid(2) priv(1) password(8) name(24) card(4) pad(1) group(7) pad(1) user_id(24)
			name, card, userID = nameStr(e[11:35]), le32(e, 35), cstr(e[48:72])
		}
		if userID == "" {
			userID = fmt.Sprint(uid)
		}
		out = append(out, core.DeviceUser{UID: uid, UserID: userID, Name: name, Privilege: privilege, Card: card})
	}
	return out, nil
}

// parseAttendance parses the ATTLOG_RRQ buffer (u32 size + records of 8, 16 or 40 bytes).
// DeviceID / DeviceName / ID / PulledAt are left zero; Name is filled from users.
// Records with an invalid time stamp are skipped.
func parseAttendance(b []byte, users []core.DeviceUser, count *int) ([]core.Attendance, error) {
	body, err := sizedBody(b)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return []core.Attendance{}, nil
	}
	size, err := pickEntrySize(len(body), count, []int{40, 16, 8})
	if err != nil {
		return nil, err
	}
	byUID := make(map[int]*core.DeviceUser, len(users))
	byUserID := make(map[string]*core.DeviceUser, len(users))
	for i := range users {
		byUID[users[i].UID] = &users[i]
		byUserID[users[i].UserID] = &users[i]
	}
	out := make([]core.Attendance, 0, len(body)/size)
	for off := 0; off+size <= len(body); off += size {
		e := body[off : off+size]
		var userID string
		var tm uint32
		var verify, punch byte
		switch size {
		case 8:
			uid := int(le16(e, 0))
			if u, ok := byUID[uid]; ok {
				userID = u.UserID
			} else {
				userID = fmt.Sprint(uid)
			}
			tm, verify, punch = le32(e, 3), e[2], e[7]
		case 16:
			userID, tm, verify, punch = fmt.Sprint(le32(e, 0)), le32(e, 4), e[8], e[9]
		default:
			uid := int(le16(e, 0))
			userID = cstr(e[2:26])
			if userID == "" {
				userID = fmt.Sprint(uid)
			}
			tm, verify, punch = le32(e, 27), e[26], e[31]
		}
		ts, ok := decodeTime(tm)
		if !ok {
			continue
		}
		name := ""
		if u, ok := byUserID[userID]; ok {
			name = u.Name
		}
		out = append(out, core.Attendance{
			UserID:     userID,
			Name:       name,
			Timestamp:  ts,
			VerifyMode: int(verify),
			PunchState: int(punch),
		})
	}
	return out, nil
}
