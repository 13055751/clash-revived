package platform

// flatbuf.go — 极简 FlatBuffers 写入器 + 读取器。
//
// 为什么自己写而不用 github.com/google/flatbuffers/go：
// 本仓库的 go.mod 只允许 mocacubex/mihomo 一条依赖树，加一个新依赖要走 Lead 审批。
// 这里只覆盖 Platform 报文用到的子集：table（含嵌套 table、vector of string）、
// 基本标量槽位、string。写入端按官方 builder 的反向增长语义实现，读取端按
// vtable 解析，两边在 _test.go 里互相 round-trip 校验。

import (
	"encoding/binary"
	"fmt"
)

// ---------- 写入 ----------

const (
	szU8  = 1
	szU16 = 2
	szU32 = 4
	szU64 = 8
)

type fbb struct {
	buf      []byte
	head     int
	minalign int
	vt       []int // 当前 table 每个 slot 的 offset（0 = 未写）
	objEnd   int
}

func newFBB(n int) *fbb {
	if n < 128 {
		n = 128
	}
	return &fbb{buf: make([]byte, n), head: n, minalign: 1}
}

func (b *fbb) offset() int { return len(b.buf) - b.head }

func (b *fbb) grow(need int) {
	oldLen, oldHead := len(b.buf), b.head
	newLen := oldLen * 2
	for newLen-oldLen < need {
		newLen *= 2
	}
	nb := make([]byte, newLen)
	// 保持「距缓冲区末尾的距离」不变：源索引 i 要落到 newLen-(oldLen-i)。
	copy(nb[newLen-(oldLen-oldHead):], b.buf[oldHead:])
	b.buf, b.head = nb, newLen-(oldLen-oldHead)
}

func (b *fbb) pad(n int) {
	for i := 0; i < n; i++ {
		b.head--
		b.buf[b.head] = 0
	}
}

func (b *fbb) prep(size, additional int) {
	if size > b.minalign {
		b.minalign = size
	}
	if len(b.buf)-b.head < size+additional {
		b.grow(size + additional)
	}
	b.pad((-(b.offset() + additional)) & (size - 1))
}

func (b *fbb) placeU32(v uint32) {
	b.head -= 4
	binary.LittleEndian.PutUint32(b.buf[b.head:], v)
}

func (b *fbb) prependU8(v uint8) {
	b.prep(szU8, 0)
	b.head--
	b.buf[b.head] = v
}

func (b *fbb) prependU16(v uint16) {
	b.prep(szU16, 0)
	b.head -= 2
	binary.LittleEndian.PutUint16(b.buf[b.head:], v)
}

func (b *fbb) prependU32(v uint32) {
	b.prep(szU32, 0)
	b.placeU32(v)
}

func (b *fbb) prependU64(v uint64) {
	b.prep(szU64, 0)
	b.head -= 8
	binary.LittleEndian.PutUint64(b.buf[b.head:], v)
}

// prependUOffset 写入一个指向已经写好的对象的相对偏移。
func (b *fbb) prependUOffset(off int) {
	b.prep(szU32, 0)
	b.placeU32(uint32(b.offset() - off + 4))
}

func (b *fbb) createString(s string) int {
	b.prep(szU32, len(s)+1)
	b.pad(1) // 结尾 NUL
	b.head -= len(s)
	copy(b.buf[b.head:], s)
	b.placeU32(uint32(len(s)))
	return b.offset()
}

// createStringVector 写一个 vector<string>；元素必须按倒序 prepend。
func (b *fbb) createStringVector(ss []string) int {
	offs := make([]int, len(ss))
	for i, s := range ss {
		offs[i] = b.createString(s)
	}
	return b.createOffsetVector(offs, szU32)
}

func (b *fbb) createOffsetVector(offs []int, elemSize int) int {
	b.startVector(elemSize, len(offs), szU32)
	for i := len(offs) - 1; i >= 0; i-- {
		b.prependUOffset(offs[i])
	}
	return b.endVector(len(offs))
}

func (b *fbb) startVector(elemSize, n, align int) int {
	b.prep(szU32, elemSize*n)
	b.prep(align, elemSize*n)
	return b.offset()
}

func (b *fbb) endVector(n int) int {
	b.placeU32(uint32(n))
	return b.offset()
}

func (b *fbb) startObject(nslots int) {
	b.vt = make([]int, nslots)
	b.objEnd = b.offset()
}

func (b *fbb) slot(i int) { b.vt[i] = b.offset() }

func (b *fbb) endObject() int {
	b.prep(szU32, 0)
	b.placeU32(0) // vtable soffset 占位
	objOffset := b.offset()

	// 去掉尾部未写槽位
	n := len(b.vt)
	for n > 0 && b.vt[n-1] == 0 {
		n--
	}
	for i := n - 1; i >= 0; i-- {
		var vo uint16
		if b.vt[i] != 0 {
			vo = uint16(objOffset - b.vt[i])
		}
		b.prependU16(vo)
	}
	objSize := objOffset - b.objEnd
	b.prependU16(uint16(objSize))
	b.prependU16(uint16((n + 2) * szU16))

	vtOffset := b.offset()
	// 表头存「表地址 − vtable 地址」的正有符号偏差。
	// offset 空间里 vtable 的 offset 更大（更早写入 = 地址更低），所以差值取 vtOffset-objOffset。
	binary.LittleEndian.PutUint32(b.buf[len(b.buf)-objOffset:], uint32(int32(vtOffset-objOffset)))
	return objOffset
}

func (b *fbb) prependUOffsetSlot(slot, off int) {
	if off == 0 {
		return
	}
	b.prependUOffset(off)
	b.slot(slot)
}

func (b *fbb) prependBoolSlot(slot int, v, d bool) {
	if v == d {
		return
	}
	var x uint8
	if v {
		x = 1
	}
	b.prependU8(x)
	b.slot(slot)
}

func (b *fbb) prependI8Slot(slot int, v, d int8) {
	if v == d {
		return
	}
	b.prependU8(uint8(v))
	b.slot(slot)
}

func (b *fbb) prependU16Slot(slot int, v, d uint16) {
	if v == d {
		return
	}
	b.prependU16(v)
	b.slot(slot)
}

func (b *fbb) prependI32Slot(slot int, v, d int32) {
	if v == d {
		return
	}
	b.prependU32(uint32(v))
	b.slot(slot)
}

func (b *fbb) prependI64Slot(slot int, v, d int64) {
	if v == d {
		return
	}
	b.prependU64(uint64(v))
	b.slot(slot)
}

// prependStructI64x4Slot 内联一个 4×int64 的 struct（CoreState.traffic）。
// struct 在 FlatBuffers 里是内联值，字段顺序递增地址，反向写就是逆序 prepend。
func (b *fbb) prependStructI64x4Slot(slot int, v [4]int64) {
	for i := 3; i >= 0; i-- {
		b.prependU64(uint64(v[i]))
	}
	b.slot(slot)
}

func (b *fbb) finish(root int) []byte {
	b.prep(b.minalign, szU32)
	b.prependUOffset(root)
	return append([]byte(nil), b.buf[b.head:]...)
}

// ---------- 读取 ----------

type fbr struct{ buf []byte }

func newFBR(b []byte) *fbr { return &fbr{buf: b} }

func (r *fbr) u16(off int) (uint16, bool) {
	if off < 0 || off+2 > len(r.buf) {
		return 0, false
	}
	return binary.LittleEndian.Uint16(r.buf[off:]), true
}

func (r *fbr) u32(off int) (uint32, bool) {
	if off < 0 || off+4 > len(r.buf) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(r.buf[off:]), true
}

func (r *fbr) root() (int, bool) {
	v, ok := r.u32(0)
	if !ok {
		return 0, false
	}
	p := int(v)
	if p < 0 || p+4 > len(r.buf) {
		return 0, false
	}
	return p, true
}

// fieldOffset 返回 slot 对应字段的绝对偏移；0 表示字段不存在。
func (r *fbr) fieldOffset(table, slot int) int {
	so, ok := r.u32(table)
	if !ok {
		return 0
	}
	vt := table - int(int32(so))
	vtSize, ok := r.u16(vt)
	if !ok {
		return 0
	}
	idx := 4 + slot*2
	if idx+2 > int(vtSize) {
		return 0
	}
	rel, ok := r.u16(vt + idx)
	if !ok || rel == 0 {
		return 0
	}
	return table + int(rel)
}

func (r *fbr) boolField(table, slot int) (bool, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 || p >= len(r.buf) {
		return false, false
	}
	return r.buf[p] != 0, true
}

// i8Field 读 int8 槽位。1 字节字段在载荷末尾时不能用 i32Field 读。
func (r *fbr) i8Field(table, slot int) (int8, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 || p >= len(r.buf) {
		return 0, false
	}
	return int8(r.buf[p]), true
}

func (r *fbr) u16Field(table, slot int) (uint16, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 {
		return 0, false
	}
	return r.u16(p)
}

func (r *fbr) i32Field(table, slot int) (int32, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 {
		return 0, false
	}
	v, ok := r.u32(p)
	return int32(v), ok
}

func (r *fbr) i64Field(table, slot int) (int64, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 || p+8 > len(r.buf) {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(r.buf[p:])), true
}

func (r *fbr) stringField(table, slot int) (string, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 {
		return "", false
	}
	rel, ok := r.u32(p)
	if !ok {
		return "", false
	}
	sp := p + int(rel)
	n, ok := r.u32(sp)
	if !ok || int(n) > len(r.buf)-sp-4 {
		return "", false
	}
	return string(r.buf[sp+4 : sp+4+int(n)]), true
}

func (r *fbr) tableField(table, slot int) (int, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 {
		return 0, false
	}
	rel, ok := r.u32(p)
	if !ok {
		return 0, false
	}
	t := p + int(rel)
	if t <= 0 || t+4 > len(r.buf) {
		return 0, false
	}
	return t, true
}

func (r *fbr) stringVector(table, slot int) ([]string, bool) {
	p := r.fieldOffset(table, slot)
	if p == 0 {
		return nil, false
	}
	rel, ok := r.u32(p)
	if !ok {
		return nil, false
	}
	vp := p + int(rel)
	n, ok := r.u32(vp)
	if !ok || int(n) > (len(r.buf)-vp-4)/4 {
		return nil, false
	}
	out := make([]string, 0, n)
	for i := 0; i < int(n); i++ {
		ep := vp + 4 + i*4
		er, ok := r.u32(ep)
		if !ok {
			return out, false
		}
		sp := ep + int(er)
		sl, ok := r.u32(sp)
		if !ok || int(sl) > len(r.buf)-sp-4 {
			return out, false
		}
		out = append(out, string(r.buf[sp+4:sp+4+int(sl)]))
	}
	return out, true
}

// errShort 用于载荷比预期短的统一报错。
func errShort(what string, n int) error {
	return fmt.Errorf("platform: short %s payload (%d bytes)", what, n)
}
