package protocol

// NBT（Named Binary Tag）编解码，采用现代网络格式：
//   - 1.20.2+ 根标签不写名字（网络 NBT）
//   - 1.8+ TAG_String / TAG_List / 数组的长度前缀为 VarInt
//   - 标量统一为大端
//
// Go 值与 NBT 类型的映射：
//
//	int8 / int16 / int32 / int64 / float32 / float64 / string
//	[]byte      -> TAG_Byte_Array
//	[]int32     -> TAG_Int_Array
//	[]int64     -> TAG_Long_Array
//	Compound    -> TAG_Compound（map[string]any）
//	List        -> TAG_List（[]any，元素类型须一致）
// Compound 对应 TAG_Compound。
type Compound = map[string]any

// List 对应 TAG_List。
type List = []any

// NBT 标签类型编号。
const (
	TagEnd         byte = 0
	TagByte        byte = 1
	TagShort       byte = 2
	TagInt         byte = 3
	TagLong        byte = 4
	TagFloat       byte = 5
	TagDouble      byte = 6
	TagByteArray   byte = 7
	TagString      byte = 8
	TagList        byte = 9
	TagCompound    byte = 10
	TagIntArray    byte = 11
	TagLongArray   byte = 12
)

// maxNBTDepth 防止恶意嵌套导致栈溢出。
const maxNBTDepth = 512

// tagIDOf 返回 Go 值对应的 NBT 标签编号。
func tagIDOf(v any) byte {
	switch v.(type) {
	case int8:
		return TagByte
	case int16:
		return TagShort
	case int32:
		return TagInt
	case int64:
		return TagLong
	case float32:
		return TagFloat
	case float64:
		return TagDouble
	case []byte:
		return TagByteArray
	case string:
		return TagString
	case List:
		return TagList
	case Compound:
		return TagCompound
	case []int32:
		return TagIntArray
	case []int64:
		return TagLongArray
	default:
		return TagEnd
	}
}

// WriteNBT 以网络格式（根标签无名）写入一个 Compound。
func WriteNBT(w *Writer, root Compound) {
	w.Uint8(TagCompound)
	writeNBTPayload(w, root, 0)
}

// writeNBTPayload 写入任意值的 NBT 载荷（不含标签编号与名字）。
func writeNBTPayload(w *Writer, v any, depth int) {
	if depth > maxNBTDepth || w.Err != nil {
		if w.Err == nil {
			w.Err = ErrOverrun
		}
		return
	}
	switch t := v.(type) {
	case int8:
		w.Int8(t)
	case int16:
		w.Int16(t)
	case int32:
		w.Int32(t)
	case int64:
		w.Int64(t)
	case float32:
		w.Float(t)
	case float64:
		w.Double(t)
	case []byte:
		w.VarInt(int32(len(t)))
		w.raw(t)
	case string:
		w.VarInt(int32(len(t)))
		w.raw([]byte(t))
	case []int32:
		w.VarInt(int32(len(t)))
		for _, e := range t {
			w.Int32(e)
		}
	case []int64:
		w.VarInt(int32(len(t)))
		for _, e := range t {
			w.Int64(e)
		}
	case List:
		w.Uint8(tagIDOfList(t))
		w.VarInt(int32(len(t)))
		for _, e := range t {
			writeNBTPayload(w, e, depth+1)
		}
	case Compound:
		for _, key := range sortedKeys(t) {
			w.Uint8(tagIDOf(t[key]))
			w.VarInt(int32(len(key)))
			w.raw([]byte(key))
			writeNBTPayload(w, t[key], depth+1)
		}
		w.Uint8(TagEnd)
	default:
		w.Err = ErrOverrun
	}
}

// tagIDOfList 取列表元素类型的标签编号（空列表按 TAG_End 写出）。
func tagIDOfList(l List) byte {
	if len(l) == 0 {
		return TagEnd
	}
	return tagIDOf(l[0])
}

// sortedKeys 让 Compound 编码结果确定（便于测试与缓存）。
func sortedKeys(c Compound) []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	// 简单插入排序：注册表键数量有限，避免引入 sort 之外的复杂度
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// ReadNBT 以网络格式读取一个 NBT 值（根标签无名）。
func ReadNBT(r *Reader) any {
	id := r.Uint8()
	if id != TagCompound {
		r.Err = ErrOverrun
		return nil
	}
	return readNBTPayload(r, id, 0)
}

// readNBTPayload 读取指定标签编号的载荷。
func readNBTPayload(r *Reader, id byte, depth int) any {
	if depth > maxNBTDepth || r.Err != nil {
		if r.Err == nil {
			r.Err = ErrOverrun
		}
		return nil
	}
	switch id {
	case TagByte:
		return r.Int8()
	case TagShort:
		return r.Int16()
	case TagInt:
		return r.Int32()
	case TagLong:
		return r.Int64()
	case TagFloat:
		return r.Float()
	case TagDouble:
		return r.Double()
	case TagByteArray:
		n := int(r.VarInt())
		b := r.take(n)
		if b == nil {
			return nil
		}
		out := make([]byte, n)
		copy(out, b)
		return out
	case TagString:
		return r.String()
	case TagList:
		elemID := r.Uint8()
		n := int(r.VarInt())
		if n < 0 || n > len(r.data) {
			r.Err = ErrOverrun
			return nil
		}
		l := make(List, n)
		for i := 0; i < n; i++ {
			l[i] = readNBTPayload(r, elemID, depth+1)
		}
		return l
	case TagCompound:
		c := Compound{}
		for {
			eid := r.Uint8()
			if eid == TagEnd {
				return c
			}
			name := r.String()
			c[name] = readNBTPayload(r, eid, depth+1)
		}
	case TagIntArray:
		n := int(r.VarInt())
		if n < 0 || n > len(r.data) {
			r.Err = ErrOverrun
			return nil
		}
		arr := make([]int32, n)
		for i := range arr {
			arr[i] = r.Int32()
		}
		return arr
	case TagLongArray:
		n := int(r.VarInt())
		if n < 0 || n > len(r.data) {
			r.Err = ErrOverrun
			return nil
		}
		arr := make([]int64, n)
		for i := range arr {
			arr[i] = r.Int64()
		}
		return arr
	default:
		r.Err = ErrOverrun
		return nil
	}
}
