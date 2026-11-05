package puller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zkscan/internal/core"
)

type fakeStore struct {
	core.Store // เมธอดที่ไม่ได้ override จะ panic
	mu         sync.Mutex
	devices    []core.Device
	settings   core.Settings
	snaps      map[int64]core.Snapshot
	failures   map[int64][2]string
	logs       []core.LogEntry
	newRecords int
	saveErr    error
}

func (s *fakeStore) ListDevices(context.Context) ([]core.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.Device(nil), s.devices...), nil
}
func (s *fakeStore) GetDevice(_ context.Context, id int64) (core.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.ID == id {
			return d, nil
		}
	}
	return core.Device{}, core.ErrNotFound
}
func (s *fakeStore) GetSettings(context.Context) (core.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, nil
}
func (s *fakeStore) setInterval(m int) {
	s.mu.Lock()
	s.settings.PullIntervalMin = m
	s.mu.Unlock()
}
func (s *fakeStore) SaveSnapshot(_ context.Context, id int64, snap core.Snapshot, _ time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return 0, s.saveErr
	}
	if s.snaps == nil {
		s.snaps = map[int64]core.Snapshot{}
	}
	s.snaps[id] = snap
	return s.newRecords, nil
}
func (s *fakeStore) SavePullFailure(_ context.Context, id int64, msg, hint string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = map[int64][2]string{}
	}
	s.failures[id] = [2]string{msg, hint}
	return nil
}
func (s *fakeStore) AddLog(_ context.Context, e core.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, e)
	return nil
}
func (s *fakeStore) logsCopy() []core.LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]core.LogEntry(nil), s.logs...)
}

type fakeClient struct {
	core.DeviceClient
	fetch func(ctx context.Context, d core.Device) (core.Snapshot, error)
	calls atomic.Int32
}

func (c *fakeClient) Fetch(ctx context.Context, d core.Device) (core.Snapshot, error) {
	c.calls.Add(1)
	return c.fetch(ctx, d)
}

func okFetch(_ context.Context, _ core.Device) (core.Snapshot, error) {
	return core.Snapshot{Users: []core.DeviceUser{{UserID: "1"}, {UserID: "2"}}}, nil
}

func newTest(devs []core.Device, fetch func(context.Context, core.Device) (core.Snapshot, error)) (*Puller, *fakeStore, *fakeClient) {
	st := &fakeStore{devices: devs, settings: core.Settings{PullIntervalMin: 5}, newRecords: 3}
	cl := &fakeClient{fetch: fetch}
	return New(st, cl, time.Now), st, cl
}

func TestFailureDoesNotStopNext(t *testing.T) {
	devs := []core.Device{{ID: 1, Name: "A", Enabled: true}, {ID: 2, Name: "B", Enabled: true}}
	p, st, _ := newTest(devs, func(ctx context.Context, d core.Device) (core.Snapshot, error) {
		if d.ID == 1 {
			return core.Snapshot{}, errors.New("boom")
		}
		return okFetch(ctx, d)
	})
	res, err := p.PullAll(context.Background())
	if err != nil || len(res) != 2 {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if res[0].OK || !strings.HasPrefix(res[0].Error, "ดึงข้อมูลไม่สำเร็จ") || !strings.Contains(res[0].Error, "boom") || res[0].Hint != "" {
		t.Errorf("res[0]=%+v", res[0])
	}
	if !res[1].OK || res[1].NewRecords != 3 || res[1].TotalUsers != 2 || res[1].At.IsZero() {
		t.Errorf("res[1]=%+v", res[1])
	}
	if _, ok := st.failures[1]; !ok {
		t.Error("no SavePullFailure for device 1")
	}
	if _, ok := st.snaps[2]; !ok {
		t.Error("no SaveSnapshot for device 2")
	}
	logs := st.logsCopy()
	if len(logs) != 2 || logs[1].Level != "info" || logs[1].Message != "ดึงข้อมูล B สำเร็จ ได้รายการใหม่ 3 รายการ" ||
		logs[1].Source != "pull" || logs[1].DeviceID == nil || *logs[1].DeviceID != 2 || logs[0].Level != "error" {
		t.Errorf("logs=%+v", logs)
	}
	if got := p.LastResults(); len(got) != 2 {
		t.Errorf("LastResults=%v", got)
	}
}

func TestDisabledSkippedByPullAllButPulledByPullDevice(t *testing.T) {
	devs := []core.Device{{ID: 1, Name: "A", Enabled: false}, {ID: 2, Name: "B", Enabled: true}}
	p, _, cl := newTest(devs, okFetch)
	res, _ := p.PullAll(context.Background())
	if len(res) != 1 || res[0].DeviceID != 2 || cl.calls.Load() != 1 {
		t.Fatalf("res=%v calls=%d", res, cl.calls.Load())
	}
	r, err := p.PullDevice(context.Background(), 1)
	if err != nil || !r.OK || r.DeviceID != 1 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if last := p.LastResults(); len(last) != 2 {
		t.Fatalf("merge failed: %v", last)
	}
	if _, err := p.PullDevice(context.Background(), 99); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err=%v", err)
	}
}

func TestBusy(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p, _, _ := newTest([]core.Device{{ID: 1, Name: "A", Enabled: true}}, func(ctx context.Context, d core.Device) (core.Snapshot, error) {
		close(entered)
		<-release
		return okFetch(ctx, d)
	})
	done := make(chan struct{})
	go func() { _, _ = p.PullAll(context.Background()); close(done) }()
	<-entered
	if !p.Running() {
		t.Error("Running false during pull")
	}
	if _, err := p.PullAll(context.Background()); !errors.Is(err, core.ErrBusy) {
		t.Errorf("PullAll err=%v", err)
	}
	if _, err := p.PullDevice(context.Background(), 1); !errors.Is(err, core.ErrBusy) {
		t.Errorf("PullDevice err=%v", err)
	}
	close(release)
	<-done
	if p.Running() {
		t.Error("Running true after pull")
	}
}

func TestPanicRecovered(t *testing.T) {
	devs := []core.Device{{ID: 1, Name: "A", Enabled: true}, {ID: 2, Name: "B", Enabled: true}}
	p, st, _ := newTest(devs, func(ctx context.Context, d core.Device) (core.Snapshot, error) {
		if d.ID == 1 {
			panic("kaboom")
		}
		return okFetch(ctx, d)
	})
	res, err := p.PullAll(context.Background())
	if err != nil || len(res) != 2 || res[0].OK || !res[1].OK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(res[0].Error, "kaboom") {
		t.Errorf("error=%q", res[0].Error)
	}
	if _, ok := st.failures[1]; !ok {
		t.Error("panic not recorded as failure")
	}
}

func TestDeviceErrorPropagated(t *testing.T) {
	p, st, _ := newTest([]core.Device{{ID: 1, Name: "A", Enabled: true}}, func(context.Context, core.Device) (core.Snapshot, error) {
		return core.Snapshot{}, fmt.Errorf("wrap: %w", &core.DeviceError{Kind: core.DevErrTimeout, Message: "เครื่องไม่ตอบ", Hint: "ตรวจสายแลน"})
	})
	r, _ := p.PullDevice(context.Background(), 1)
	if r.OK || r.Error != "เครื่องไม่ตอบ" || r.Hint != "ตรวจสายแลน" {
		t.Errorf("r=%+v", r)
	}
	if f := st.failures[1]; f[0] != "เครื่องไม่ตอบ" || f[1] != "ตรวจสายแลน" {
		t.Errorf("failure=%v", f)
	}
	l := st.logsCopy()
	if len(l) != 1 || !strings.Contains(l[0].Message, "เครื่องไม่ตอบ") || !strings.Contains(l[0].Message, "ตรวจสายแลน") {
		t.Errorf("logs=%+v", l)
	}
}

func TestStoreErrorReported(t *testing.T) {
	p, st, _ := newTest([]core.Device{{ID: 1, Name: "A", Enabled: true}}, okFetch)
	st.saveErr = errors.New("disk full")
	r, _ := p.PullDevice(context.Background(), 1)
	if r.OK || !strings.Contains(r.Error, "disk full") {
		t.Errorf("r=%+v", r)
	}
	if _, ok := st.failures[1]; !ok {
		t.Error("store failure not recorded via SavePullFailure")
	}
}

func TestFetchTimeoutBounded(t *testing.T) {
	var dl time.Time
	p, _, _ := newTest([]core.Device{{ID: 1, Name: "A", Enabled: true, TimeoutSec: 2}}, func(ctx context.Context, d core.Device) (core.Snapshot, error) {
		dl, _ = ctx.Deadline()
		return okFetch(ctx, d)
	})
	before := time.Now()
	_, _ = p.PullDevice(context.Background(), 1)
	if got := dl.Sub(before); got < fetchTimeout-time.Minute || got > fetchTimeout+time.Minute {
		t.Errorf("deadline in %v, want ~15m", got)
	}
}

func fastRun(p *Puller) {
	p.initialDelay = 5 * time.Millisecond
	p.disabledRecheck = 5 * time.Millisecond
	p.intervalUnit = 10 * time.Millisecond
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestIntervalChangeAndDisable(t *testing.T) {
	p, st, cl := newTest([]core.Device{{ID: 1, Name: "A", Enabled: true}}, okFetch)
	fastRun(p)
	st.setInterval(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	waitFor(t, "3 pulls", func() bool { return cl.calls.Load() >= 3 })

	st.setInterval(0) // ปิด
	time.Sleep(60 * time.Millisecond)
	n := cl.calls.Load()
	time.Sleep(150 * time.Millisecond)
	if cl.calls.Load() != n {
		t.Errorf("pulled while disabled: %d -> %d", n, cl.calls.Load())
	}

	st.setInterval(100000) // นานมาก: ดึงทันทีเมื่อเปิดใหม่ครั้งเดียว แล้วไม่ดึงอีก
	waitFor(t, "resume after disable", func() bool { return cl.calls.Load() > n })
	n = cl.calls.Load()
	time.Sleep(150 * time.Millisecond)
	if cl.calls.Load() != n {
		t.Errorf("interval change not applied: %d -> %d", n, cl.calls.Load())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestRunExitsOnCancel(t *testing.T) {
	// ก่อนดึงครั้งแรก (หน่วงเริ่มต้น 5 วินาทีจริง)
	p, _, _ := newTest(nil, okFetch)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit (initial wait)")
	}

	// กลางการดึง: Fetch เคารพ ctx
	p2, _, cl := newTest([]core.Device{{ID: 1, Name: "A", Enabled: true}}, func(ctx context.Context, _ core.Device) (core.Snapshot, error) {
		<-ctx.Done()
		return core.Snapshot{}, ctx.Err()
	})
	fastRun(p2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { p2.Run(ctx2); close(done2) }()
	waitFor(t, "fetch started", func() bool { return cl.calls.Load() >= 1 })
	cancel2()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit (mid-pull)")
	}
}

func TestLastResultsConcurrency(t *testing.T) {
	devs := []core.Device{{ID: 1, Name: "A", Enabled: true}, {ID: 2, Name: "B", Enabled: true}}
	p, _, _ := newTest(devs, okFetch)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r := p.LastResults()
					if len(r) > 0 {
						r[0].DeviceName = "mutated" // สำเนา ต้องไม่กระทบของจริง
					}
					_ = p.Running()
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		_, _ = p.PullAll(context.Background())
		_, _ = p.PullDevice(context.Background(), 1)
	}
	close(stop)
	wg.Wait()
	for _, r := range p.LastResults() {
		if r.DeviceName == "mutated" {
			t.Fatal("LastResults returned shared storage")
		}
	}
}
