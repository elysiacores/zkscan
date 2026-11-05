package zk

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"zkscan/internal/core"
)

// ---- fixtures: hand-built device byte layouts shared by tests ----

func withSize(entries ...[]byte) []byte {
	body := bytes.Join(entries, nil)
	v := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
	return append(v, body...)
}

func user72(uid uint16, privilege byte, name string, card uint32, userID string) []byte {
	e := make([]byte, 72)
	binary.LittleEndian.PutUint16(e[0:], uid)
	e[2] = privilege
	if len(name) > 24 {
		panic("name field is 24 bytes")
	}
	copy(e[11:], name)
	binary.LittleEndian.PutUint32(e[35:], card)
	copy(e[48:], userID)
	return e
}

func user28(uid uint16, privilege byte, name string, card uint32, userID uint32) []byte {
	e := make([]byte, 28)
	binary.LittleEndian.PutUint16(e[0:], uid)
	e[2] = privilege
	copy(e[8:], name)
	binary.LittleEndian.PutUint32(e[16:], card)
	binary.LittleEndian.PutUint32(e[24:], userID)
	return e
}

func att40(uid uint16, userID string, verify byte, t time.Time, punch byte) []byte {
	e := make([]byte, 40)
	binary.LittleEndian.PutUint16(e[0:], uid)
	copy(e[2:], userID)
	e[26] = verify
	binary.LittleEndian.PutUint32(e[27:], encodeTime(t))
	e[31] = punch
	return e
}

func dt(y int, mo time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, mo, d, h, mi, s, 0, time.Local)
}

func ip(v int) *int { return &v }

func leBytes16(vs ...uint16) []byte {
	var b []byte
	for _, v := range vs {
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testUsers() []core.DeviceUser {
	return []core.DeviceUser{
		{UID: 5, UserID: "1005", Name: "Somchai"},
		{UID: 6, UserID: "1006", Name: "Suda"},
	}
}

// ---- tests (port of protocol.rs tests) ----

func TestChecksumKnownVector(t *testing.T) {
	// CONNECT, session 0, reply id 65534: the well-known 0xFC17.
	hdr := leBytes16(1000, 0, 0, 65534)
	if got := checksum(hdr); got != 0xFC17 {
		t.Errorf("checksum=%#x want 0xFC17", got)
	}
	// Odd length: trailing byte added on its own (1 + 2 = 3 -> 65535 - 1 - 3).
	if got := checksum([]byte{1, 0, 2}); got != 65531 {
		t.Errorf("odd checksum=%d want 65531", got)
	}
}

func TestReplyIDWraps(t *testing.T) {
	for in, want := range map[uint16]uint16{65534: 0, 0: 1, 65533: 65534} {
		if got := nextReplyID(in); got != want {
			t.Errorf("nextReplyID(%d)=%d want %d", in, got, want)
		}
	}
}

func TestPacketRoundTripAndChecksumValidates(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	p := buildPacket(cmdGetTime, 0x1234, 7, data)
	if !verifyChecksum(p) {
		t.Fatal("checksum should validate")
	}
	pk, err := parsePacket(p)
	mustNoErr(t, err)
	if pk.header.command != cmdGetTime || pk.header.sessionID != 0x1234 || pk.header.replyID != 7 {
		t.Errorf("header %+v", pk.header)
	}
	if !bytes.Equal(pk.data, data) {
		t.Errorf("data %v", pk.data)
	}
	bad := append([]byte(nil), p...)
	bad[headerLen] ^= 0xFF
	if verifyChecksum(bad) {
		t.Error("tampered packet must not validate")
	}
	if _, err := parseHeader(p[:5]); err == nil {
		t.Error("short header must fail")
	}
	empty := buildPacket(cmdConnect, 0, 0, nil)
	if len(empty) != 8 || !verifyChecksum(empty) {
		t.Errorf("empty packet len=%d", len(empty))
	}
	h, _ := parseHeader(empty)
	if h.checksum != 0xFC16 {
		t.Errorf("empty checksum %#x want 0xFC16", h.checksum)
	}
}

func TestTCPFrameLayout(t *testing.T) {
	f := tcpFrame([]byte{9, 8, 7})
	if !bytes.Equal(f[:4], tcpMagic[:]) {
		t.Error("magic")
	}
	if binary.LittleEndian.Uint32(f[4:]) != 3 {
		t.Error("length")
	}
	if !bytes.Equal(f[8:], []byte{9, 8, 7}) {
		t.Error("payload")
	}
}

func TestTimeRoundTrip(t *testing.T) {
	tm := dt(2026, 11, 4, 8, 5, 9)
	got, ok := decodeTime(encodeTime(tm))
	if !ok || !got.Equal(tm) {
		t.Errorf("round trip got %v ok=%v", got, ok)
	}
	t2 := dt(2000, 1, 1, 0, 0, 0)
	if encodeTime(t2) != 0 {
		t.Errorf("encode 2000-01-01 = %d", encodeTime(t2))
	}
	if g, ok := decodeTime(0); !ok || !g.Equal(t2) {
		t.Errorf("decode 0 = %v", g)
	}
	t3 := dt(2031, 12, 31, 23, 59, 59)
	if g, ok := decodeTime(encodeTime(t3)); !ok || !g.Equal(t3) {
		t.Errorf("t3 = %v", g)
	}
}

func TestInvalidTimeIsNone(t *testing.T) {
	// month 2, day 31 -> invalid date
	if _, ok := decodeTime((31 + 30) * 86400); ok {
		t.Error("Feb 31 must be invalid")
	}
	// large values must not panic
	decodeTime(^uint32(0))
}

func TestCommKey(t *testing.T) {
	a := makeCommKey(0, 0x1234, 50)
	b := makeCommKey(123456, 0x1234, 50)
	if a == b {
		t.Error("different keys must differ")
	}
	if b != makeCommKey(123456, 0x1234, 50) {
		t.Error("not deterministic")
	}
	if a[2] != 50 {
		t.Error("ticks byte")
	}
	// the low byte of (reversed key + session) is replaced by the ticks, so vary a higher byte
	if makeCommKey(123456, 0x1234, 50) == makeCommKey(123456, 0x2234, 50) {
		t.Error("session must influence the key")
	}
	// key 0, session 0: k = 0 -> [Z,K,S,O] -> swap halves [S,O,Z,K] -> xor 50
	if got := makeCommKey(0, 0, 50); got != [4]byte{'S' ^ 50, 'O' ^ 50, 50, 'K' ^ 50} {
		t.Errorf("got %v", got)
	}
}

func TestOptionsValues(t *testing.T) {
	cases := map[string]string{
		"~SerialNumber=ABC123\x00\x00\x00": "ABC123",
		"~Platform=ZMM220_TFT\x00":         "ZMM220_TFT",
		"Ver 6.60 Sep\x00junk":             "Ver 6.60 Sep",
		"":                                 "",
	}
	for in, want := range cases {
		if got := parseOptionsValue([]byte(in)); got != want {
			t.Errorf("parseOptionsValue(%q)=%q want %q", in, got, want)
		}
	}
	if !bytes.Equal(optionRequest("Platform"), []byte("~Platform\x00")) {
		t.Error("optionRequest")
	}
}

func TestFreeSizes(t *testing.T) {
	d := make([]byte, 80)
	binary.LittleEndian.PutUint32(d[16:], 12)
	binary.LittleEndian.PutUint32(d[32:], 345)
	u, r := parseFreeSizes(d)
	if u == nil || r == nil || *u != 12 || *r != 345 {
		t.Errorf("got %v %v", u, r)
	}
	u, r = parseFreeSizes(d[:79])
	if u != nil || r != nil {
		t.Error("short reply must give nil")
	}
}

func TestPrepareBufferLayout(t *testing.T) {
	d := prepareBufferData(cmdUsertempRRQ, fctUser, 0)
	if len(d) != 11 || d[0] != 1 {
		t.Fatalf("layout %v", d)
	}
	if int16(binary.LittleEndian.Uint16(d[1:])) != 9 {
		t.Error("command")
	}
	if int32(binary.LittleEndian.Uint32(d[3:])) != 5 {
		t.Error("fct")
	}
}

func TestUsers28(t *testing.T) {
	blob := withSize(user28(1, 14, "Somchai", 123456, 1001), user28(2, 0, "Suda", 0, 1002))
	u, err := parseUsers(blob, ip(2))
	mustNoErr(t, err)
	if len(u) != 2 {
		t.Fatalf("len=%d", len(u))
	}
	if u[0].UID != 1 || u[0].Privilege != 14 || u[0].Card != 123456 {
		t.Errorf("u0 %+v", u[0])
	}
	if u[0].UserID != "1001" || u[0].Name != "Somchai" || u[1].UserID != "1002" {
		t.Errorf("u %+v", u)
	}
	// count unknown -> inferred (56 bytes: not divisible by 72)
	u2, err := parseUsers(blob, nil)
	mustNoErr(t, err)
	if len(u2) != 2 {
		t.Errorf("inferred len=%d", len(u2))
	}
}

func TestUsers72ThaiName(t *testing.T) {
	blob := withSize(
		user72(1, 14, "สมชาย ใจ", 7654321, "1001"),
		user72(2, 0, "Suda", 5, "1002"),
	)
	u, err := parseUsers(blob, ip(2))
	mustNoErr(t, err)
	if len(u) != 2 {
		t.Fatalf("len=%d", len(u))
	}
	if u[0].Name != "สมชาย ใจ" || u[0].UserID != "1001" || u[0].Card != 7654321 || u[0].Privilege != 14 {
		t.Errorf("u0 %+v", u[0])
	}
	if u[1].Name != "Suda" {
		t.Errorf("u1 %+v", u[1])
	}
	u2, err := parseUsers(blob, nil)
	mustNoErr(t, err)
	if len(u2) != 2 {
		t.Errorf("inferred len=%d", len(u2))
	}
}

func TestUsersGarbled(t *testing.T) {
	if _, err := parseUsers([]byte{1, 2}, nil); err == nil {
		t.Error("2 bytes must fail")
	}
	if _, err := parseUsers(withSize(make([]byte, 50)), nil); err == nil {
		t.Error("50-byte entry must fail")
	}
	if u, err := parseUsers(nil, nil); err != nil || len(u) != 0 {
		t.Errorf("empty: %v %v", u, err)
	}
	// declared size larger than data: clamp, no panic
	blob := withSize(user72(1, 0, "A", 0, "1"))
	blob[0] = 0xFF
	u, err := parseUsers(blob, nil)
	mustNoErr(t, err)
	if len(u) != 1 {
		t.Errorf("len=%d", len(u))
	}
}

func TestAttendance40(t *testing.T) {
	tm := dt(2026, 11, 4, 8, 5, 9)
	blob := withSize(att40(5, "1005", 1, tm, 0), att40(6, "1006", 15, dt(2026, 11, 4, 17, 1, 2), 1))
	r, err := parseAttendance(blob, testUsers(), ip(2))
	mustNoErr(t, err)
	if len(r) != 2 {
		t.Fatalf("len=%d", len(r))
	}
	if r[0].UserID != "1005" || r[0].Name != "Somchai" || !r[0].Timestamp.Equal(tm) {
		t.Errorf("r0 %+v", r[0])
	}
	if r[0].VerifyMode != 1 || r[0].PunchState != 0 || r[1].VerifyMode != 15 || r[1].PunchState != 1 {
		t.Errorf("modes %+v", r)
	}
	// unknown count -> inferred as 40
	r2, err := parseAttendance(blob, testUsers(), nil)
	mustNoErr(t, err)
	if len(r2) != 2 {
		t.Errorf("inferred len=%d", len(r2))
	}
}

func TestAttendance16(t *testing.T) {
	tm := dt(2026, 11, 4, 8, 5, 9)
	e := make([]byte, 16)
	binary.LittleEndian.PutUint32(e[0:], 1005)
	binary.LittleEndian.PutUint32(e[4:], encodeTime(tm))
	e[8] = 2
	e[9] = 1
	binary.LittleEndian.PutUint32(e[12:], 77)
	blob := withSize(e, e)
	r, err := parseAttendance(blob, testUsers(), ip(2))
	mustNoErr(t, err)
	if len(r) != 2 {
		t.Fatalf("len=%d", len(r))
	}
	if r[0].UserID != "1005" || r[0].Name != "Somchai" || r[0].VerifyMode != 2 || r[0].PunchState != 1 || !r[0].Timestamp.Equal(tm) {
		t.Errorf("r0 %+v", r[0])
	}
	r2, err := parseAttendance(blob, testUsers(), nil)
	mustNoErr(t, err)
	if len(r2) != 2 {
		t.Errorf("inferred len=%d", len(r2))
	}
}

func TestAttendance8(t *testing.T) {
	tm := dt(2026, 11, 4, 8, 5, 9)
	mk := func(uid uint16) []byte {
		e := make([]byte, 8)
		binary.LittleEndian.PutUint16(e[0:], uid)
		e[2] = 1
		binary.LittleEndian.PutUint32(e[3:], encodeTime(tm))
		e[7] = 0
		return e
	}
	// 3 records = 24 bytes: not a multiple of 16/40, so 8 is chosen
	blob := withSize(mk(5), mk(6), mk(99))
	r, err := parseAttendance(blob, testUsers(), ip(3))
	mustNoErr(t, err)
	if len(r) != 3 {
		t.Fatalf("len=%d", len(r))
	}
	if r[0].UserID != "1005" || r[1].Name != "Suda" || r[2].UserID != "99" || r[2].Name != "" || !r[0].Timestamp.Equal(tm) {
		t.Errorf("r %+v", r)
	}
}

func TestAttendanceInvalidTimeSkippedAndGarbled(t *testing.T) {
	tm := dt(2026, 11, 4, 8, 5, 9)
	bad := att40(5, "1005", 1, tm, 0)
	binary.LittleEndian.PutUint32(bad[27:], (31+30)*86400) // Feb 31
	blob := withSize(bad, att40(6, "1006", 1, tm, 0))
	r, err := parseAttendance(blob, testUsers(), ip(2))
	mustNoErr(t, err)
	if len(r) != 1 || r[0].UserID != "1006" {
		t.Errorf("r %+v", r)
	}
	if _, err := parseAttendance([]byte{1, 2, 3}, testUsers(), nil); err == nil {
		t.Error("3 bytes must fail")
	}
	if _, err := parseAttendance(withSize(make([]byte, 13)), testUsers(), nil); err == nil {
		t.Error("13-byte entry must fail")
	}
	if r, err := parseAttendance(nil, testUsers(), nil); err != nil || len(r) != 0 {
		t.Error("empty")
	}
	if r, err := parseAttendance([]byte{0, 0, 0, 0}, testUsers(), nil); err != nil || len(r) != 0 {
		t.Error("zero size")
	}
}

func TestNameCutMidCharacter(t *testing.T) {
	// 24-byte field filled with 8 Thai letters (3 bytes each) = exactly 24; add one more cut at 23
	full := []byte("กขคงจฉชซ") // 24 bytes
	if got := nameStr(full); got != "กขคงจฉชซ" {
		t.Errorf("full %q", got)
	}
	if got := nameStr(full[:23]); got != "กขคงจฉช" {
		t.Errorf("23 %q", got)
	}
	if got := nameStr(full[:22]); got != "กขคงจฉช" {
		t.Errorf("22 %q", got)
	}
}

func TestNameTIS620(t *testing.T) {
	// "สมชาย" in TIS-620
	tis := []byte{0xCA, 0xC1, 0xAA, 0xD2, 0xC2, 0, 0, 0}
	if got := nameStr(tis); got != "สมชาย" {
		t.Errorf("tis %q", got)
	}
	// mixed with ASCII and an undefined byte (skipped)
	if got := nameStr([]byte{'A', ' ', 0xCA, 0xC1, 0x80}); got != "A สม" {
		t.Errorf("mixed %q", got)
	}
	// UTF-8 Thai still works
	if got := nameStr([]byte("สมชาย")); got != "สมชาย" {
		t.Errorf("utf8 %q", got)
	}
	// inside a 72-byte user entry
	e := user72(1, 0, "", 5, "1001")
	copy(e[11:16], []byte{0xCA, 0xC1, 0xAA, 0xD2, 0xC2})
	u, err := parseUsers(withSize(e), ip(1))
	mustNoErr(t, err)
	if u[0].Name != "สมชาย" {
		t.Errorf("entry %q", u[0].Name)
	}
}

func TestBuildRequestMatchesPyzkConnect(t *testing.T) {
	// pyzk CONNECT: command 1000, checksum over reply id 65534, sent reply id 0
	p := buildRequest(cmdConnect, 0, 65534, nil)
	if !bytes.Equal(p, []byte{0xE8, 0x03, 0x17, 0xFC, 0x00, 0x00, 0x00, 0x00}) {
		t.Errorf("connect packet % x", p)
	}
	h, _ := parseHeader(p)
	if h.replyID != nextReplyID(65534) {
		t.Error("sent reply id must be the incremented one")
	}
	q := buildRequest(cmdGetTime, 0x1234, 5, []byte{1, 2})
	qh, _ := parseHeader(q)
	if qh.replyID != 6 || !bytes.Equal(q[8:], []byte{1, 2}) {
		t.Errorf("q % x", q)
	}
	// checksum equals the one over the pre-increment header
	ph, _ := parseHeader(buildPacket(cmdGetTime, 0x1234, 5, []byte{1, 2}))
	if qh.checksum != ph.checksum {
		t.Error("checksum must be over the pre-increment header")
	}
}

func TestCstrLossy(t *testing.T) {
	if got := cstr([]byte("  abc \x00def")); got != "abc" {
		t.Errorf("got %q", got)
	}
	if got := cstr([]byte{0xFF, 'a', 0}); got != "�a" {
		t.Errorf("got %q", got)
	}
}
