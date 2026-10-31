package main

// flatbuf.go — 最小 FlatBuffers 读取器。
//
// 只需要读：root table、字符串字段、子表字段、bool/int 字段。
// 依据 CFA 3.0.3 的 CoreAPI.initialize 字节码还原出的写入序列：
//   FlatBufferBuilder(128)
//   createString x4          // 4 个 String 参数
//   createString x3          // LegacyDatabases.filesPath / v21xPath / v23xPath
//   startTable(3) + addOffset(2..0) + endTable()   -> legacy 子表
//   startTable(5) + addOffset(...) x5 + endTable() -> 根表
//   finish(root)
// 因此根表 5 个字段，其中一个是三字符串子表。字段的具体 slot 归属
// 尚需用真实抓包确认；下面的读取器按 slot 索引取，不依赖语义。

import (
	"encoding/binary"
	"errors"
	"fmt"
)

type FB struct {
	buf []byte
}

func NewFB(b []byte) *FB { return &FB{buf: b} }

func (f *FB) u32(off int) (uint32, error) {
	if off < 0 || off+4 > len(f.buf) {
		return 0, fmt.Errorf("flatbuf: read u32 out of range at %d (len %d)", off, len(f.buf))
	}
	return binary.LittleEndian.Uint32(f.buf[off:]), nil
}

func (f *FB) i32(off int) (int32, error) {
	v, err := f.u32(off)
	return int32(v), err
}

func (f *FB) u16(off int) (uint16, error) {
	if off < 0 || off+2 > len(f.buf) {
		return 0, fmt.Errorf("flatbuf: read u16 out of range at %d", off)
	}
	return binary.LittleEndian.Uint16(f.buf[off:]), nil
}

// Root returns the offset of the root table.
func (f *FB) Root() (int, error) {
	v, err := f.u32(0)
	if err != nil {
		return 0, err
	}
	return int(v), nil
}

// fieldOffset resolves vtable slot -> absolute offset within buf, or -1 if absent.
func (f *FB) fieldOffset(table, slot int) (int, error) {
	// table starts with soffset32 to vtable
	vtRel, err := f.i32(table)
	if err != nil {
		return -1, err
	}
	vt := table - int(vtRel)
	vtSize, err := f.u16(vt)
	if err != nil {
		return -1, err
	}
	slotOff := 4 + slot*2
	if slotOff+2 > int(vtSize) {
		return -1, nil
	}
	rel, err := f.u16(vt + slotOff)
	if err != nil {
		return -1, err
	}
	if rel == 0 {
		return -1, nil
	}
	return table + int(rel), nil
}

// String reads a string field at an absolute offset (which holds a uoffset to the string).
func (f *FB) StringAt(abs int) (string, error) {
	rel, err := f.u32(abs)
	if err != nil {
		return "", err
	}
	p := abs + int(rel)
	n, err := f.u32(p)
	if err != nil {
		return "", err
	}
	if int(n) > len(f.buf)-p-4 {
		return "", errors.New("flatbuf: string length out of range")
	}
	return string(f.buf[p+4 : p+4+int(n)]), nil
}

// TableField returns absolute offset of a sub-table field.
func (f *FB) TableField(table, slot int) (int, error) {
	abs, err := f.fieldOffset(table, slot)
	if err != nil || abs < 0 {
		return -1, err
	}
	rel, err := f.u32(abs)
	if err != nil {
		return -1, err
	}
	return abs + int(rel), nil
}

// StringField reads slot as string ("" if absent).
func (f *FB) StringField(table, slot int) string {
	abs, err := f.fieldOffset(table, slot)
	if err != nil || abs < 0 {
		return ""
	}
	s, err := f.StringAt(abs)
	if err != nil {
		return ""
	}
	return s
}

// BoolField reads slot as bool padded to u8.
func (f *FB) BoolField(table, slot int) (bool, bool) {
	abs, err := f.fieldOffset(table, slot)
	if err != nil || abs < 0 || abs >= len(f.buf) {
		return false, false
	}
	return f.buf[abs] != 0, true
}

// IntField reads slot as int32.
func (f *FB) IntField(table, slot int) (int32, bool) {
	abs, err := f.fieldOffset(table, slot)
	if err != nil || abs < 0 {
		return 0, false
	}
	v, err := f.i32(abs)
	if err != nil {
		return 0, false
	}
	return v, true
}

// LooksLikeTable probes an absolute offset: a real table starts with a signed
// soffset pointing back to a plausible vtable (even size, 4..4+2*32 bytes).
func (f *FB) LooksLikeTable(abs int) bool {
	rel, err := f.i32(abs)
	if err != nil || rel == 0 {
		return false
	}
	vt := abs - int(rel)
	if vt < 0 || vt+4 > len(f.buf) {
		return false
	}
	vtSize, err := f.u16(vt)
	if err != nil {
		return false
	}
	if vtSize < 4 || vtSize > 4+2*32 || vtSize%2 != 0 {
		return false
	}
	tableSize, err := f.u16(vt + 2)
	if err != nil || tableSize < 4 || int(tableSize) > 4096 {
		return false
	}
	return true
}

// NumSlots reports how many vtable slots the table declares.
func (f *FB) NumSlots(table int) (int, error) {
	vtRel, err := f.i32(table)
	if err != nil {
		return 0, err
	}
	vt := table - int(vtRel)
	vtSize, err := f.u16(vt)
	if err != nil {
		return 0, err
	}
	if vtSize < 4 {
		return 0, errors.New("flatbuf: bogus vtable size")
	}
	return (int(vtSize) - 4) / 2, nil
}
