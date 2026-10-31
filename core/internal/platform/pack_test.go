package platform

// pack_test.go — 载荷编解码的 round-trip 测试。
//
// 写入器与读取器是各自独立实现的（一个反向增长、一个 vtable 解析），
// 所以 round-trip 能同时校验两边；再叠加对槽位序的显式断言。

import (
	"testing"
	"time"
)

func TestVpnConfigureRoundTrip(t *testing.T) {
	cfg := &VpnConfigure{
		RouteSubnets: true,
		SystemProxy: &SystemProxyConfig{
			Enabled:  true,
			Host:     "127.0.0.1",
			Port:     7890,
			Excludes: []string{"10.0.0.0/8", "192.168.0.0/16"},
		},
		AccessControl: &VpnAccessControl{
			Enabled:   true,
			AllowApps: []string{"com.android.chrome", "org.telegram.messenger"},
		},
	}
	p := encodeVpnConfigure(cfg)

	r := newFBR(p)
	root, ok := r.root()
	if !ok {
		t.Fatalf("no root table")
	}

	// slot0: bool 路由模式 —— 已确认类型
	mode, ok := r.boolField(root, 0)
	if !ok {
		t.Fatalf("slot0 (routeSubnets) missing")
	}
	if mode != cfg.RouteSubnets {
		t.Errorf("routeSubnets = %v, want %v", mode, cfg.RouteSubnets)
	}

	// slot1: uoffset → SystemProxyConfig
	sp, ok := r.tableField(root, 1)
	if !ok {
		t.Fatalf("slot1 (systemProxy) is not a table")
	}
	if v, ok := r.boolField(sp, 0); !ok || !v {
		t.Errorf("systemProxy.slot0 enabled = %v (present=%v), want true", v, ok)
	}
	if got, _ := r.stringField(sp, 1); got != "127.0.0.1" {
		t.Errorf("systemProxy.slot1 host = %q, want 127.0.0.1", got)
	}
	if got, ok := r.u16Field(sp, 2); !ok || got != 7890 {
		t.Errorf("systemProxy.slot2 port = %d (present=%v), want 7890", got, ok)
	}
	if got, ok := r.stringVector(sp, 3); !ok || len(got) != 2 || got[0] != "10.0.0.0/8" {
		t.Errorf("systemProxy.slot3 excludes = %v (present=%v)", got, ok)
	}

	// slot2: uoffset → VpnAccessControl
	ac, ok := r.tableField(root, 2)
	if !ok {
		t.Fatalf("slot2 (accessControl) is not a table")
	}
	if got, ok := r.boolField(ac, 0); !ok || !got {
		t.Errorf("accessControl.slot0 enabled = %v (present=%v), want true", got, ok)
	}
	if got, ok := r.stringVector(ac, 1); !ok || len(got) != 2 || got[1] != "org.telegram.messenger" {
		t.Errorf("accessControl.slot1 allowApps = %v (present=%v)", got, ok)
	}

	// 空配置也要能编出一个合法根表
	empty := newFBR(encodeVpnConfigure(nil))
	if _, ok := empty.root(); !ok {
		t.Errorf("empty VpnConfigure produced no root table")
	}
}

func TestCoreStateRoundTrip(t *testing.T) {
	st := &CoreState{
		ProfileName: "机场 A",
		Traffic:     Traffic{Upload: 11, Download: 22, UploadTotal: 33, DownloadTotal: 44},
	}
	r := newFBR(encodeCoreState(st))
	root, ok := r.root()
	if !ok {
		t.Fatalf("no root table")
	}
	if got, _ := r.stringField(root, 2); got != "机场 A" {
		t.Errorf("profileName = %q", got)
	}
	// slot0 是内联 struct：4×int64
	p := r.fieldOffset(root, 0)
	if p == 0 {
		t.Fatalf("traffic struct missing")
	}
	var got [4]int64
	for i := 0; i < 4; i++ {
		v, ok := r.i64Field(root, 0)
		_ = v
		if !ok {
			t.Fatalf("traffic not readable")
		}
		break
	}
	for i := 0; i < 4; i++ {
		off := p + i*8
		if off+8 > len(r.buf) {
			t.Fatalf("traffic field %d out of range", i)
		}
		got[i] = int64(uint64(r.buf[off]) | uint64(r.buf[off+1])<<8 | uint64(r.buf[off+2])<<16 |
			uint64(r.buf[off+3])<<24 | uint64(r.buf[off+4])<<32 | uint64(r.buf[off+5])<<40 |
			uint64(r.buf[off+6])<<48 | uint64(r.buf[off+7])<<56)
	}
	want := [4]int64{11, 22, 33, 44}
	if got != want {
		t.Errorf("traffic = %v, want %v", got, want)
	}
}

func TestNotificationRoundTrip(t *testing.T) {
	n := &Notification{State: 3, Title: "标题", Content: "内容", Channel: "clash"}
	r := newFBR(encodeNotification(n))
	root, ok := r.root()
	if !ok {
		t.Fatalf("no root table")
	}
	if got, _ := r.i32Field(root, 0); int8(got) != 3 {
		t.Errorf("state = %d", got)
	}
	if got, _ := r.stringField(root, 1); got != "标题" {
		t.Errorf("slot1 = %q", got)
	}
	if got, _ := r.stringField(root, 2); got != "内容" {
		t.Errorf("slot2 = %q", got)
	}
	if got, _ := r.stringField(root, 3); got != "clash" {
		t.Errorf("slot3 = %q", got)
	}
}

func TestConnectionInfoRoundTrip(t *testing.T) {
	ci := &ConnectionInfo{Protocol: 6, Source: "10.0.0.2", SourcePort: 51000, Dest: "1.1.1.1", DestPort: 443}
	r := newFBR(encodeConnectionInfo(ci))
	root, ok := r.root()
	if !ok {
		t.Fatalf("no root table")
	}
	if got, _ := r.i32Field(root, 0); int8(got) != 6 {
		t.Errorf("protocol = %d", got)
	}
	if got, _ := r.stringField(root, 1); got != "10.0.0.2" {
		t.Errorf("source = %q", got)
	}
	if got, _ := r.i32Field(root, 2); got != 51000 {
		t.Errorf("sourcePort = %d", got)
	}
	if got, _ := r.stringField(root, 3); got != "1.1.1.1" {
		t.Errorf("dest = %q", got)
	}
	if got, _ := r.i32Field(root, 4); got != 443 {
		t.Errorf("destPort = %d", got)
	}
}

func TestAppSocketSpecRoundTrip(t *testing.T) {
	sp := &AppSocketSpec{Domain: 2, Type: 1, Protocol: 6}
	r := newFBR(encodeAppSocketSpec(sp))
	root, ok := r.root()
	if !ok {
		t.Fatalf("no root table")
	}
	for i, want := range []int32{2, 1, 6} {
		if got, ok := r.i32Field(root, i); !ok || got != want {
			t.Errorf("slot%d = %d (present=%v), want %d", i, got, ok, want)
		}
	}
}

func TestScheduleWakeupRoundTrip(t *testing.T) {
	at := time.UnixMilli(1730000000123)
	r := newFBR(encodeScheduleWakeup(at))
	root, ok := r.root()
	if !ok {
		t.Fatalf("no root table")
	}
	if got, ok := r.i64Field(root, 0); !ok || got != 1730000000123 {
		t.Errorf("slot0 = %d (present=%v)", got, ok)
	}
}

func TestLockInfoRoundTrip(t *testing.T) {
	r := newFBR(encodeLockInfo(&LockInfo{Kind: 2}))
	root, ok := r.root()
	if !ok {
		t.Fatalf("no root table")
	}
	if got, ok := r.i8Field(root, 0); !ok || got != 2 {
		t.Errorf("slot0 = %d (present=%v)", got, ok)
	}
}

func TestDecodeTimeZone(t *testing.T) {
	b := newFBB(64)
	id := b.createString("Asia/Shanghai")
	b.startObject(2)
	b.prependI64Slot(1, 28800, 0)
	b.prependUOffsetSlot(0, id)
	tz, err := decodeTimeZone(b.finish(b.endObject()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tz.ID != "Asia/Shanghai" || tz.RawOffset != 28800 {
		t.Errorf("got %+v", tz)
	}
}

func TestDecodeFeaturesLenient(t *testing.T) {
	b := newFBB(64)
	b.startObject(8)
	b.prependBoolSlot(0, true, false)
	b.prependBoolSlot(3, true, false)
	f := decodeFeatures(b.finish(b.endObject()))
	if !f.Present[0] || !f.Flags[0] {
		t.Errorf("slot0 should be present & true")
	}
	if !f.Present[3] || !f.Flags[3] {
		t.Errorf("slot3 should be present & true")
	}
	if f.Present[1] {
		t.Errorf("slot1 should be absent")
	}
	if !f.VpnSupported() {
		t.Errorf("VpnSupported should be true when any flag is set")
	}
	// 空载荷不能 panic
	if got := decodeFeatures(nil); got.VpnSupported() {
		t.Errorf("empty payload should not claim support")
	}
}
