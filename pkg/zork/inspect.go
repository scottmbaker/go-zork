package zork

// Read-only views of the game state. Out-of-range arguments give zero values.

func (z *ZMachine) objPtr(obj int) (uint32, bool) {
	if obj < 1 || obj > z.objCount {
		return 0, false
	}
	return uint32(z.Header.ObjtabAddr) + 31*2 + 9*uint32(obj-1), true
}

// ObjectCount returns how many objects the story defines.
func (z *ZMachine) ObjectCount() int { return z.objCount }

// countObjects finds the number of objects. The object table has no count;
// by convention it ends where the first property table begins.
func (z *ZMachine) countObjects() int {
	base := uint32(z.Header.ObjtabAddr) + 31*2
	low := uint32(len(z.Story))
	n := 0
	for n < 255 && base+9*uint32(n)+9 <= low {
		props := uint32(z.readUI16(z.Story, base+9*uint32(n)+7))
		if props < low {
			low = props
		}
		n++
	}
	return n
}

func (z *ZMachine) objByte(obj int, offset uint32) int {
	ptr, ok := z.objPtr(obj)
	if !ok {
		return 0
	}
	return int(z.Story[ptr+offset])
}

// ObjectParent returns the object containing obj, or 0.
func (z *ZMachine) ObjectParent(obj int) int { return z.objByte(obj, 4) }

// ObjectSibling returns the next object in the same container, or 0.
func (z *ZMachine) ObjectSibling(obj int) int { return z.objByte(obj, 5) }

// ObjectChild returns the first object contained in obj, or 0.
func (z *ZMachine) ObjectChild(obj int) int { return z.objByte(obj, 6) }

// ObjectName returns an object's short name.
func (z *ZMachine) ObjectName(obj int) string {
	ptr, ok := z.objPtr(obj)
	if !ok {
		return ""
	}
	addr := uint32(z.readUI16(z.Story, ptr+7))
	if z.Story[addr] == 0 {
		return ""
	}
	buf := make([]byte, 512)
	n, _, err := z.decodeZscii(z.Story[addr+1:], 0, buf)
	if err != nil {
		return ""
	}
	return string(buf[:min(n, len(buf))])
}

// ObjectAttr reports whether obj has an attribute (0-31) set.
func (z *ZMachine) ObjectAttr(obj, attr int) bool {
	ptr, ok := z.objPtr(obj)
	if !ok || attr < 0 || attr > 31 {
		return false
	}
	return z.Story[ptr+uint32(attr/8)]&(0x80>>(attr&7)) != 0
}

// Global returns global variable n (0-239).
func (z *ZMachine) Global(n int) uint16 {
	if n < 0 || n > 239 {
		return 0
	}
	return z.readUI16(z.Story, uint32(z.Header.GlobalsAddr)+uint32(n)*2)
}

// StatusLine returns what a version 3 status line shows for a scored game.
func (z *ZMachine) StatusLine() (room string, score, moves int) {
	return z.ObjectName(int(z.Global(0))), int(int16(z.Global(1))), int(z.Global(2))
}

// The functions below alter the game from outside. They exist for authoring
// tools that need to visit states directly and are not part of normal play.

// SetGlobal writes global variable n.
func (z *ZMachine) SetGlobal(n int, val uint16) {
	if n >= 0 && n <= 239 {
		z.writeUI16(z.Story, uint32(z.Header.GlobalsAddr)+uint32(n)*2, val)
	}
}

// SetObjectAttr sets or clears an attribute.
func (z *ZMachine) SetObjectAttr(obj, attr int, on bool) {
	ptr, ok := z.objPtr(obj)
	if !ok || attr < 0 || attr > 31 {
		return
	}
	mask := byte(0x80 >> (attr & 7))
	if on {
		z.Story[ptr+uint32(attr/8)] |= mask
	} else {
		z.Story[ptr+uint32(attr/8)] &^= mask
	}
}

// MoveObject puts obj inside dest, or nowhere if dest is 0.
func (z *ZMachine) MoveObject(obj, dest int) error {
	ptr, ok := z.objPtr(obj)
	if !ok {
		return nil
	}
	if err := z.unparentObject(uint16(obj)); err != nil {
		return err
	}
	z.Story[ptr+4], z.Story[ptr+5] = 0, 0
	if dptr, ok := z.objPtr(dest); ok {
		z.Story[ptr+4] = byte(dest)
		z.Story[ptr+5] = z.Story[dptr+6]
		z.Story[dptr+6] = byte(obj)
	}
	return nil
}

// ObjectProp returns an object's one- or two-byte property value, or the
// story's default for that property.
func (z *ZMachine) ObjectProp(obj, prop int) uint16 {
	if _, ok := z.objPtr(obj); !ok || prop < 1 || prop > 31 {
		return 0
	}
	var size uint8
	ptr, err := z.getObjectProperty(uint16(obj), uint32(prop), &size)
	if err != nil {
		return 0
	}
	switch {
	case ptr == 0:
		return z.getDefaultObjectProperty(uint16(prop))
	case size == 1:
		return uint16(z.Story[ptr])
	default:
		return z.readUI16(z.Story, ptr)
	}
}

// SetObjectProp writes a property the object already has.
func (z *ZMachine) SetObjectProp(obj, prop int, val uint16) {
	if _, ok := z.objPtr(obj); !ok || prop < 1 || prop > 31 {
		return
	}
	var size uint8
	ptr, err := z.getObjectProperty(uint16(obj), uint32(prop), &size)
	if err != nil || ptr == 0 {
		return
	}
	if size == 1 {
		z.Story[ptr] = uint8(val)
	} else {
		z.writeUI16(z.Story, ptr, val)
	}
}

// ObjectPropBytes returns a property's raw bytes, or nil if the object does not
// have the property itself.
func (z *ZMachine) ObjectPropBytes(obj, prop int) []byte {
	if _, ok := z.objPtr(obj); !ok || prop < 1 || prop > 31 {
		return nil
	}
	var size uint8
	ptr, err := z.getObjectProperty(uint16(obj), uint32(prop), &size)
	if err != nil || ptr == 0 {
		return nil
	}
	return append([]byte(nil), z.Story[ptr:ptr+uint32(size)]...)
}

// DictionaryData returns the data bytes the dictionary holds for a word, or nil
// if the word is not in it.
func (z *ZMachine) DictionaryData(word string) []byte {
	want := encodeWord([]byte(word))
	addr := uint32(z.Header.DictAddr)
	addr += 1 + uint32(z.Story[addr])
	size, n := uint32(z.Story[addr]), uint32(z.readUI16(z.Story, addr+1))
	addr += 3
	for i := uint32(0); i < n; i++ {
		p := addr + i*size
		if z.readUI16(z.Story, p) == want[0] && z.readUI16(z.Story, p+2) == want[1] {
			return append([]byte(nil), z.Story[p+4:p+size]...)
		}
	}
	return nil
}

// PackedText decodes the string at a packed address, such as the value of a
// property that holds a description.
func (z *ZMachine) PackedText(packed uint16) string {
	addr, err := z.unpackAddress(uint32(packed))
	if err != nil || addr >= uint32(len(z.Story)) {
		return ""
	}
	buf := make([]byte, 1024)
	n, _, err := z.decodeZscii(z.Story[addr:], 0, buf)
	if err != nil {
		return ""
	}
	return string(buf[:min(n, len(buf))])
}
