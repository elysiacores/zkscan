package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"zkscan/internal/auth"
	"zkscan/internal/core"
	"zkscan/internal/puller"
	"zkscan/internal/store"
	"zkscan/internal/zk"
)

const (
	defaultListen = ":6565"
	defaultData   = "./data"
	dbFile        = "zkscan.db"
	passwordFile  = "initial-admin-password.txt"
	tempPassLabel = "รหัสผ่านชั่วคราวของ "
)

const usageText = `zkscan — ดึงข้อมูลเวลาเข้า-ออกงานจากเครื่องสแกน ZKTeco

วิธีใช้:  zkscan <คำสั่ง> [ตัวเลือก]

คำสั่ง:
  serve                      เปิดบริการ (หน้าเว็บ + API + ดึงข้อมูลตามรอบ)
  init                       เตรียมฐานข้อมูลและสร้างบัญชี admin ครั้งแรก
  reset-password <ชื่อผู้ใช้>   ออกรหัสผ่านชั่วคราวใหม่ให้ผู้ใช้
  show-initial-password      แสดงรหัสผ่านชั่วคราวของ admin (ถ้ายังไม่ได้เปลี่ยน)
  version                    แสดงเวอร์ชัน
  help                       แสดงข้อความนี้

ตัวเลือก (ใส่หลังชื่อคำสั่ง):
  --listen <ที่อยู่>   ที่อยู่ที่รับการเชื่อมต่อ (env ZKSCAN_LISTEN, ค่าเริ่มต้น :6565)
  --data <โฟลเดอร์>    โฟลเดอร์เก็บข้อมูล (env ZKSCAN_DATA, ค่าเริ่มต้น ./data)
`

// run คือตัวเข้าโปรแกรมที่ทดสอบได้: คืนรหัสออก 0 สำเร็จ, 1 ผิดพลาด, 2 ใช้คำสั่งผิด
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help", "-help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "version", "--version":
		fmt.Fprintf(stdout, "zkscan %s\n", version)
		return 0
	case "serve", "init", "reset-password", "show-initial-password":
	default:
		fmt.Fprintf(stderr, "ไม่รู้จักคำสั่ง “%s”\n\n%s", cmd, usageText)
		return 2
	}

	opts, pos, err := parseOpts(cmd, rest, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usageText)
			return 0
		}
		fmt.Fprintf(stderr, "ตัวเลือกไม่ถูกต้อง: %v\n", err)
		return 2
	}
	switch cmd {
	case "reset-password":
		if len(pos) != 1 {
			fmt.Fprint(stderr, "วิธีใช้:  zkscan reset-password <ชื่อผู้ใช้>\n")
			return 2
		}
	default:
		if len(pos) != 0 {
			fmt.Fprintf(stderr, "คำสั่ง %s ไม่รับค่าเพิ่มเติม “%s”\n", cmd, pos[0])
			return 2
		}
	}

	switch cmd {
	case "init":
		return cmdInit(opts, stdout, stderr)
	case "show-initial-password":
		return cmdShowPassword(opts, stdout, stderr)
	case "reset-password":
		return cmdReset(opts, pos[0], stdout, stderr)
	default:
		return cmdServe(opts, stdout, stderr)
	}
}

type options struct {
	Listen string
	Data   string
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// parseOpts อ่าน --listen/--data (flag ชนะ env) และคืนอาร์กิวเมนต์ที่ไม่ใช่ flag
// ยอมให้ flag อยู่หลังค่า positional ได้ เช่น reset-password admin --data x
func parseOpts(cmd string, args []string, stderr io.Writer) (options, []string, error) {
	o := options{Listen: envOr("ZKSCAN_LISTEN", defaultListen), Data: envOr("ZKSCAN_DATA", defaultData)}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Listen, "listen", o.Listen, "")
	fs.StringVar(&o.Data, "data", o.Data, "")
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return o, nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if strings.TrimSpace(o.Data) == "" {
		return o, nil, errors.New("--data ต้องไม่ว่าง")
	}
	return o, pos, nil
}

// openStore สร้างโฟลเดอร์ข้อมูล (0750) แล้วเปิดฐานข้อมูล
func openStore(dataDir string) (*store.Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("สร้างโฟลเดอร์ข้อมูล %s ไม่ได้: %w", dataDir, err)
	}
	st, err := store.Open(filepath.Join(dataDir, dbFile))
	if err != nil {
		return nil, fmt.Errorf("เปิดฐานข้อมูลไม่ได้: %w", err)
	}
	return st, nil
}

func passwordLine(user, pw string) string { return tempPassLabel + user + ": " + pw }

// writePasswordFile เขียนรหัสชั่วคราวลงไฟล์ด้วยสิทธิ์ 0600 (บังคับสิทธิ์แม้ไฟล์เดิมมีอยู่)
func writePasswordFile(dataDir, pw string) error {
	p := filepath.Join(dataDir, passwordFile)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(pw + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// bootstrap สร้างบัญชี admin ถ้ายังไม่มีผู้ใช้ และเขียนไฟล์รหัสชั่วคราว
// คืน (รหัส, สร้างใหม่หรือไม่, error ของการเขียนไฟล์) — error ของไฟล์ไม่ทำให้รหัสหาย
func bootstrap(ctx context.Context, svc core.AuthService, dataDir string) (pw string, created bool, fileErr error, err error) {
	pw, created, err = svc.Bootstrap(ctx)
	if err != nil || !created {
		return "", false, nil, err
	}
	return pw, true, writePasswordFile(dataDir, pw), nil
}

func announceAdmin(out, errOut io.Writer, pw string, fileErr error) {
	fmt.Fprintln(out, passwordLine("admin", pw))
	fmt.Fprintln(out, "ต้องเปลี่ยนรหัสผ่านนี้ทันทีเมื่อเข้าสู่ระบบครั้งแรก")
	if fileErr != nil {
		fmt.Fprintf(errOut, "คำเตือน: เขียนไฟล์ %s ไม่สำเร็จ (%v) — จดรหัสด้านบนไว้ก่อน\n", passwordFile, fileErr)
	}
}

func cmdInit(o options, stdout, stderr io.Writer) int {
	st, err := openStore(o.Data)
	if err != nil {
		fmt.Fprintln(stderr, "ผิดพลาด:", err)
		return 1
	}
	defer st.Close()
	pw, created, fileErr, err := bootstrap(context.Background(), auth.New(st, auth.Options{}), o.Data)
	if err != nil {
		fmt.Fprintln(stderr, "ผิดพลาด: สร้างบัญชีผู้ดูแลไม่สำเร็จ:", err)
		return 1
	}
	if !created {
		fmt.Fprintln(stdout, "มีบัญชีผู้ดูแลอยู่แล้ว")
		return 0
	}
	announceAdmin(stdout, stderr, pw, fileErr)
	if fileErr != nil {
		return 1
	}
	fmt.Fprintf(stdout, "บันทึกรหัสไว้ที่ %s (ดูอีกครั้งได้ด้วย zkscan show-initial-password)\n", filepath.Join(o.Data, passwordFile))
	return 0
}

func cmdShowPassword(o options, stdout, stderr io.Writer) int {
	b, err := os.ReadFile(filepath.Join(o.Data, passwordFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "ไม่พบรหัสผ่านชั่วคราว — admin เปลี่ยนรหัสผ่านไปแล้ว หรือไฟล์ถูกลบแล้ว\n"+
				"ถ้าลืมรหัสผ่าน ให้ออกรหัสใหม่ด้วยคำสั่ง  zkscan reset-password admin")
		} else {
			fmt.Fprintln(stderr, "ผิดพลาด: อ่านไฟล์รหัสผ่านไม่ได้:", err)
		}
		return 1
	}
	pw := strings.TrimSpace(string(b))
	if pw == "" {
		fmt.Fprintln(stderr, "ไฟล์รหัสผ่านว่างเปล่า — ใช้  zkscan reset-password admin  เพื่อออกรหัสใหม่")
		return 1
	}
	fmt.Fprintln(stdout, passwordLine("admin", pw))
	return 0
}

func cmdReset(o options, username string, stdout, stderr io.Writer) int {
	st, err := openStore(o.Data)
	if err != nil {
		fmt.Fprintln(stderr, "ผิดพลาด:", err)
		return 1
	}
	defer st.Close()
	uname := strings.ToLower(strings.TrimSpace(username))
	pw, err := auth.New(st, auth.Options{}).ResetPassword(context.Background(), uname)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			fmt.Fprintf(stderr, "ผิดพลาด: ไม่พบผู้ใช้ “%s”\n", username)
		} else {
			fmt.Fprintln(stderr, "ผิดพลาด: รีเซ็ตรหัสผ่านไม่สำเร็จ:", err)
		}
		return 1
	}
	fmt.Fprintln(stdout, passwordLine(uname, pw))
	fmt.Fprintln(stdout, "ผู้ใช้ต้องเปลี่ยนรหัสผ่านนี้เมื่อเข้าสู่ระบบครั้งถัดไป (session เดิมถูกยกเลิกแล้ว)")
	return 0
}

func logSystem(st core.Store, level, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = st.AddLog(ctx, core.LogEntry{At: time.Now(), Level: level, Source: "system", Message: msg})
}

func cmdServe(o options, stdout, stderr io.Writer) int {
	st, err := openStore(o.Data)
	if err != nil {
		fmt.Fprintln(stderr, "ผิดพลาด:", err)
		return 1
	}
	var wg sync.WaitGroup
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer func() { // ปิดฐานข้อมูลหลังงานเบื้องหลังจบหมดแล้วเท่านั้น
		wg.Wait()
		st.Close()
	}()

	authSvc := auth.New(st, auth.Options{})
	pw, created, fileErr, err := bootstrap(ctx, authSvc, o.Data)
	if err != nil {
		fmt.Fprintln(stderr, "ผิดพลาด: สร้างบัญชีผู้ดูแลไม่สำเร็จ:", err)
		return 1
	}
	if created {
		announceAdmin(stdout, stderr, pw, fileErr)
	}

	client := zk.New()
	pl := puller.New(st, client, nil)
	app, err := buildApp(appDeps{Store: st, Auth: authSvc, Puller: pl, Client: client, Version: version})
	if err != nil {
		fmt.Fprintln(stderr, "ผิดพลาด: เริ่มหน้าเว็บไม่สำเร็จ:", err)
		return 1
	}

	bg, cancel := context.WithCancel(ctx)
	defer cancel()
	wg.Add(3)
	go func() { defer wg.Done(); pl.Run(bg) }()
	go func() { defer wg.Done(); sessionJanitor(bg, st) }()
	go func() { defer wg.Done(); passwordFileJanitor(bg, st, o.Data, time.Minute) }()

	fmt.Fprintf(stdout, "zkscan %s เริ่มทำงาน — รับการเชื่อมต่อที่ %s, โฟลเดอร์ข้อมูล %s\n", version, o.Listen, o.Data)
	logSystem(st, "info", fmt.Sprintf("zkscan %s เริ่มทำงาน (รับการเชื่อมต่อที่ %s)", version, o.Listen))

	errc := make(chan error, 1)
	go func() { errc <- app.Listen(o.Listen) }()

	code := 0
	select {
	case err := <-errc:
		fmt.Fprintf(stderr, "ผิดพลาด: เปิดพอร์ต %s ไม่ได้: %v\nถ้าพอร์ตถูกใช้อยู่ ให้เปลี่ยน --listen หรือ ZKSCAN_LISTEN\n", o.Listen, err)
		code = 1
	case <-ctx.Done():
		fmt.Fprintln(stdout, "ได้รับสัญญาณปิด กำลังหยุดบริการ...")
		if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
			fmt.Fprintln(stderr, "คำเตือน: ปิดเซิร์ฟเวอร์ไม่เรียบร้อย:", err)
		}
	}
	cancel()
	wg.Wait()
	logSystem(st, "info", "zkscan หยุดทำงาน")
	fmt.Fprintln(stdout, "zkscan หยุดทำงานแล้ว")
	return code
}

// sessionJanitor ลบ session หมดอายุทุกชั่วโมง
func sessionJanitor(ctx context.Context, st core.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = st.DeleteExpiredSessions(ctx, time.Now())
		}
	}
}

// removePasswordFileIfChanged ลบไฟล์รหัสชั่วคราวเมื่อ admin เปลี่ยนรหัสแล้ว (หรือไม่มีบัญชี admin แล้ว)
func removePasswordFileIfChanged(ctx context.Context, st core.Store, dataDir string) {
	p := filepath.Join(dataDir, passwordFile)
	if _, err := os.Stat(p); err != nil {
		return
	}
	u, err := st.GetUserByUsername(ctx, "admin")
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return
	}
	if err == nil && u.MustChangePassword {
		return
	}
	_ = os.Remove(p)
}

func passwordFileJanitor(ctx context.Context, st core.Store, dataDir string, every time.Duration) {
	removePasswordFileIfChanged(ctx, st, dataDir)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			removePasswordFileIfChanged(ctx, st, dataDir)
		}
	}
}
