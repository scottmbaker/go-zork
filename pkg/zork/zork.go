// Go-Zork, a Zork Z-Engine in Golang
// Scott Baker, https://github.com/scottmbaker/
//
// Based on MojoZork by Ryan C. Gordon, https://github.com/icculus/mojozork

// Package zork implements a Z-Machine interpreter for running Infocom story files.
package zork

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
)

// ZHeader holds the parsed Z-Machine story file header.
type ZHeader struct {
	Version       uint8
	Flags1        uint8
	Release       uint16
	HimemAddr     uint16
	PcStart       uint16
	DictAddr      uint16
	ObjtabAddr    uint16
	GlobalsAddr   uint16
	StaticmemAddr uint16
	Flags2        uint16
	SerialCode    [7]byte
	AbbrtabAddr   uint16
	StoryLen      uint16
	StoryChecksum uint16
}

// ZMachine is the Z-Machine virtual machine state.
type ZMachine struct {
	InstructionsRun    uint32
	Story              []byte
	Header             ZHeader
	LogicalPC          uint32
	PC                 uint32
	SP                 int
	BP                 int
	CalculatedChecksum uint16
	Quit               bool
	StepCompleted      bool
	Stack              [2048]uint16
	Operands           [8]uint16
	OperandCount       uint8
	AlphabetTable      [78]byte
	StartupScript      *string
	StoryFilename      string
	StatusBarEnabled   bool
	StatusBar          []byte
	StatusBarHighlight []byte
	StatusBarLen       int
	CurrentWindow      uint16
	UpperWindowLines   uint16

	SplitWindow func(oldval, newval uint16)
	SetWindow   func(oldval, newval uint16)

	InputChan  <-chan string // ZMachine receives user input lines
	OutputChan chan<- []byte // ZMachine sends output

	Opcodes     [256]func() error
	RandomSeed  int32
	SaveFile    string // path for save/restore; defaults to "save.dat"
	InitialSeed int32
}

func (z *ZMachine) readUI16(story []byte, offset uint32) uint16 {
	return (uint16(story[offset]) << 8) | uint16(story[offset+1])
}

func (z *ZMachine) writeUI16(story []byte, offset uint32, val uint16) {
	story[offset] = uint8((val >> 8) & 0xFF)
	story[offset+1] = uint8(val & 0xFF)
}

func (z *ZMachine) unpackAddress(addr uint32) (uint32, error) {
	if z.Header.Version <= 3 {
		return addr * 2, nil
	} else if z.Header.Version <= 5 {
		return addr * 4, nil
	} else if z.Header.Version <= 8 {
		return addr * 8, nil
	}
	return 0, fmt.Errorf("unsupported version for packed addressing")
}

func (z *ZMachine) readVar(v uint8, indirect bool) (uint16, error) {
	if v == 0 { // top of stack
		if indirect {
			if z.SP == 0 {
				return 0, fmt.Errorf("stack underflow")
			}
			return z.Stack[z.SP-1], nil
		}
		if z.SP == 0 {
			return 0, fmt.Errorf("stack underflow")
		}
		numLocals := uint16(0)
		if z.BP > 0 {
			numLocals = z.Stack[z.BP-1]
		}
		if z.BP+int(numLocals) >= z.SP {
			return 0, fmt.Errorf("stack underflow")
		}
		z.SP--
		return z.Stack[z.SP], nil
	} else if v >= 1 && v <= 15 {
		if z.Stack[z.BP-1] <= uint16(v-1) {
			return 0, fmt.Errorf("referenced unallocated local var #%d", v-1)
		}
		return z.Stack[z.BP+int(v)-1], nil
	}

	offset := uint32(z.Header.GlobalsAddr) + uint32(v-0x10)*2
	return z.readUI16(z.Story, offset), nil
}

func (z *ZMachine) writeVar(v uint8, val uint16, indirect bool) error {
	if v == 0 {
		if indirect {
			if z.SP == 0 {
				return fmt.Errorf("stack underflow")
			}
			z.Stack[z.SP-1] = val
		} else {
			if z.SP >= len(z.Stack) {
				return fmt.Errorf("stack overflow")
			}
			z.Stack[z.SP] = val
			z.SP++
		}
	} else if v >= 1 && v <= 15 {
		if z.Stack[z.BP-1] <= uint16(v-1) {
			return fmt.Errorf("referenced unallocated local var #%d", v-1)
		}
		z.Stack[z.BP+int(v)-1] = val
	} else {
		offset := uint32(z.Header.GlobalsAddr) + uint32(v-0x10)*2
		z.writeUI16(z.Story, offset, val)
	}
	return nil
}

func (z *ZMachine) doReturn(val uint16) error {
	if z.BP == 0 {
		return fmt.Errorf("stack underflow in return operation")
	}

	z.SP = z.BP

	z.SP--
	// numlocals := z.Stack[z.SP]

	z.SP--
	z.BP = int(z.Stack[z.SP])

	z.SP--
	hi := uint32(z.Stack[z.SP])

	z.SP--
	lo := uint32(z.Stack[z.SP])

	z.PC = (hi << 16) | lo

	z.SP--
	storeid := uint8(z.Stack[z.SP])

	return z.writeVar(storeid, val, false)
}

func (z *ZMachine) opcodeCall() error {
	args := z.OperandCount
	operands := z.Operands
	storeid := z.Story[z.PC]
	z.PC++

	if args == 0 || operands[0] == 0 {
		return z.writeVar(storeid, 0, false)
	}

	routine, err := z.unpackAddress(uint32(operands[0]))
	if err != nil {
		return err
	}
	z.LogicalPC = routine
	numlocals := z.Story[routine]
	routine++
	if numlocals > 15 {
		return fmt.Errorf("routine has too many local variables (%d)", numlocals)
	}

	z.Stack[z.SP] = uint16(storeid)
	z.SP++

	pcoffset := z.PC
	z.Stack[z.SP] = uint16(pcoffset & 0xFFFF)
	z.SP++
	z.Stack[z.SP] = uint16((pcoffset >> 16) & 0xFFFF)
	z.SP++

	z.Stack[z.SP] = uint16(z.BP)
	z.SP++
	z.Stack[z.SP] = uint16(numlocals)
	z.SP++

	z.BP = z.SP

	if z.Header.Version <= 4 {
		for i := uint8(0); i < numlocals; i++ {
			val := z.readUI16(z.Story, routine)
			routine += 2
			z.Stack[z.SP] = val
			z.SP++
		}
	} else {
		for i := uint8(0); i < numlocals; i++ {
			z.Stack[z.SP] = 0
			z.SP++
		}
	}

	args--
	if args > numlocals {
		args = numlocals
	}

	for i := uint8(0); i < args; i++ {
		z.Stack[z.BP+int(i)] = operands[i+1]
	}

	z.PC = routine
	return nil
}

func (z *ZMachine) opcodeRet() error {
	return z.doReturn(z.Operands[0])
}

func (z *ZMachine) opcodeRtrue() error {
	return z.doReturn(1)
}

func (z *ZMachine) opcodeRfalse() error {
	return z.doReturn(0)
}

func (z *ZMachine) opcodeRetPopped() error {
	result, err := z.readVar(0, false)
	if err != nil {
		return err
	}
	return z.doReturn(result)
}

func (z *ZMachine) opcodePop() error {
	_, err := z.readVar(0, false)
	return err
}

func (z *ZMachine) opcodePush() error {
	return z.writeVar(0, z.Operands[0], false)
}

func (z *ZMachine) opcodePull() error {
	val, err := z.readVar(0, false)
	if err != nil {
		return err
	}
	return z.writeVar(uint8(z.Operands[0]&0xFF), val, true)
}

func (z *ZMachine) updateStatusBar() {
	// To be implemented
}

func (z *ZMachine) opcodeShowStatus() error {
	z.updateStatusBar()
	return nil
}

func (z *ZMachine) opcodeAdd() error {
	store := z.Story[z.PC]
	z.PC++
	result := int16(z.Operands[0]) + int16(z.Operands[1])
	return z.writeVar(store, uint16(result), false)
}

func (z *ZMachine) opcodeSub() error {
	store := z.Story[z.PC]
	z.PC++
	result := int16(z.Operands[0]) - int16(z.Operands[1])
	return z.writeVar(store, uint16(result), false)
}

func (z *ZMachine) doBranch(truth bool) error {
	branch := z.Story[z.PC]
	z.PC++
	farjump := (branch & (1 << 6)) == 0
	onTruth := (branch & (1 << 7)) != 0

	var byte2 uint8
	if farjump {
		byte2 = z.Story[z.PC]
		z.PC++
	}

	if truth == onTruth {
		offset := int16(branch & 0x3F)
		if farjump {
			if (offset & (1 << 5)) != 0 {
				offset |= ^0x3F // sign extend
			}
			offset = (offset << 8) | int16(byte2)
		}

		if offset == 0 {
			return z.doReturn(0)
		} else if offset == 1 {
			return z.doReturn(1)
		} else {
			z.PC = uint32(int32(z.PC) + int32(offset) - 2)
		}
	}
	return nil
}

func (z *ZMachine) opcodeJe() error {
	a := z.Operands[0]
	for i := uint8(1); i < z.OperandCount; i++ {
		if a == z.Operands[i] {
			return z.doBranch(true)
		}
	}
	return z.doBranch(false)
}

func (z *ZMachine) opcodeJz() error {
	return z.doBranch(z.Operands[0] == 0)
}

func (z *ZMachine) opcodeJl() error {
	return z.doBranch(int16(z.Operands[0]) < int16(z.Operands[1]))
}

func (z *ZMachine) opcodeJg() error {
	return z.doBranch(int16(z.Operands[0]) > int16(z.Operands[1]))
}

func (z *ZMachine) opcodeTest() error {
	return z.doBranch((z.Operands[0] & z.Operands[1]) == z.Operands[1])
}

func (z *ZMachine) opcodeJump() error {
	z.PC = uint32(int32(z.PC) + int32(int16(z.Operands[0])) - 2)
	return nil
}

func (z *ZMachine) opcodeDiv() error {
	store := z.Story[z.PC]
	z.PC++
	if z.Operands[1] == 0 {
		return fmt.Errorf("division by zero")
	}
	result := int16(z.Operands[0]) / int16(z.Operands[1])
	return z.writeVar(store, uint16(result), false)
}

func (z *ZMachine) opcodeMod() error {
	store := z.Story[z.PC]
	z.PC++
	if z.Operands[1] == 0 {
		return fmt.Errorf("division by zero")
	}
	result := int16(z.Operands[0]) % int16(z.Operands[1])
	return z.writeVar(store, uint16(result), false)
}

func (z *ZMachine) opcodeMul() error {
	store := z.Story[z.PC]
	z.PC++
	result := int16(z.Operands[0]) * int16(z.Operands[1])
	return z.writeVar(store, uint16(result), false)
}

func (z *ZMachine) opcodeOr() error {
	store := z.Story[z.PC]
	z.PC++
	result := z.Operands[0] | z.Operands[1]
	return z.writeVar(store, result, false)
}

func (z *ZMachine) opcodeAnd() error {
	store := z.Story[z.PC]
	z.PC++
	result := z.Operands[0] & z.Operands[1]
	return z.writeVar(store, result, false)
}

func (z *ZMachine) opcodeNot() error {
	store := z.Story[z.PC]
	z.PC++
	result := ^z.Operands[0]
	return z.writeVar(store, result, false)
}

func (z *ZMachine) opcodeIncChk() error {
	v := uint8(z.Operands[0] & 0xFF)
	valU, err := z.readVar(v, true)
	if err != nil {
		return err
	}
	val := int16(valU) + 1
	if err := z.writeVar(v, uint16(val), true); err != nil {
		return err
	}
	return z.doBranch(val > int16(z.Operands[1]))
}

func (z *ZMachine) opcodeInc() error {
	v := uint8(z.Operands[0] & 0xFF)
	valU, err := z.readVar(v, true)
	if err != nil {
		return err
	}
	val := int16(valU) + 1
	return z.writeVar(v, uint16(val), true)
}

func (z *ZMachine) opcodeDecChk() error {
	v := uint8(z.Operands[0] & 0xFF)
	valU, err := z.readVar(v, true)
	if err != nil {
		return err
	}
	val := int16(valU) - 1
	if err := z.writeVar(v, uint16(val), true); err != nil {
		return err
	}
	return z.doBranch(val < int16(z.Operands[1]))
}

func (z *ZMachine) opcodeDec() error {
	v := uint8(z.Operands[0] & 0xFF)
	valU, err := z.readVar(v, true)
	if err != nil {
		return err
	}
	val := int16(valU) - 1
	return z.writeVar(v, uint16(val), true)
}

func (z *ZMachine) opcodeLoad() error {
	v := uint8(z.Operands[0] & 0xFF)
	val, err := z.readVar(v, true)
	if err != nil {
		return err
	}
	store := z.Story[z.PC]
	z.PC++
	return z.writeVar(store, val, false)
}

func (z *ZMachine) opcodeLoadw() error {
	store := z.Story[z.PC]
	z.PC++
	offset := uint32(z.Operands[0]) + uint32(z.Operands[1])*2
	val := z.readUI16(z.Story, offset)
	return z.writeVar(store, val, false)
}

func (z *ZMachine) opcodeLoadb() error {
	store := z.Story[z.PC]
	z.PC++
	offset := uint32(z.Operands[0]) + uint32(z.Operands[1])
	val := uint16(z.Story[offset])
	return z.writeVar(store, val, false)
}

func (z *ZMachine) opcodeStorew() error {
	offset := uint32(z.Operands[0]) + uint32(z.Operands[1])*2
	z.writeUI16(z.Story, offset, z.Operands[2])
	return nil
}

func (z *ZMachine) opcodeStoreb() error {
	offset := uint32(z.Operands[0]) + uint32(z.Operands[1])
	z.Story[offset] = uint8(z.Operands[2] & 0xFF)
	return nil
}

func (z *ZMachine) opcodeStore() error {
	v := uint8(z.Operands[0] & 0xFF)
	return z.writeVar(v, z.Operands[1], true)
}

func (z *ZMachine) getObjectPtr(objid uint16) (uint32, error) {
	if objid == 0 {
		return 0, fmt.Errorf("object ID #0 referenced")
	}
	if z.Header.Version <= 3 && objid > 255 {
		return 0, fmt.Errorf("invalid object ID referenced")
	}
	ptr := uint32(z.Header.ObjtabAddr)
	ptr += 31 * 2              // skip properties defaults table
	ptr += 9 * uint32(objid-1) // find object
	return ptr, nil
}

func (z *ZMachine) opcodeTestAttr() error {
	objid := z.Operands[0]
	attrid := z.Operands[1]
	ptr, err := z.getObjectPtr(objid)
	if err != nil {
		return err
	}

	if z.Header.Version <= 3 {
		ptr += uint32(attrid / 8)
		b := z.Story[ptr]
		return z.doBranch((b & (0x80 >> (attrid & 7))) != 0)
	}
	return fmt.Errorf("write me")
}

func (z *ZMachine) opcodeSetAttr() error {
	objid := z.Operands[0]
	attrid := z.Operands[1]
	ptr, err := z.getObjectPtr(objid)
	if err != nil {
		return err
	}

	if z.Header.Version <= 3 {
		ptr += uint32(attrid / 8)
		z.Story[ptr] |= (0x80 >> (attrid & 7))
		return nil
	}
	return fmt.Errorf("write me")
}

func (z *ZMachine) opcodeClearAttr() error {
	objid := z.Operands[0]
	attrid := z.Operands[1]
	if objid == 0 {
		return nil
	}
	ptr, err := z.getObjectPtr(objid)
	if err != nil {
		return err
	}

	if z.Header.Version <= 3 {
		ptr += uint32(attrid / 8)
		z.Story[ptr] &= ^(0x80 >> (attrid & 7))
		return nil
	}
	return fmt.Errorf("write me")
}

func (z *ZMachine) getObjectPtrParent(objptr uint32) (uint32, error) {
	if z.Header.Version <= 3 {
		parent := uint16(z.Story[objptr+4])
		if parent != 0 {
			return z.getObjectPtr(parent)
		}
		return 0, nil
	}
	return 0, fmt.Errorf("write me")
}

func (z *ZMachine) remapObjectID(objid uint16) uint16 {
	return objid
}

func (z *ZMachine) unparentObject(objid uint16) error {
	objid = z.remapObjectID(objid)
	objptr, err := z.getObjectPtr(objid)
	if err != nil {
		return err
	}
	parentptr, err := z.getObjectPtrParent(objptr)
	if err != nil {
		return err
	}
	if parentptr != 0 {
		ptr := parentptr + 6
		for z.Story[ptr] != uint8(objid) {
			objPtrTmp, err := z.getObjectPtr(uint16(z.Story[ptr]))
			if err != nil {
				return err
			}
			ptr = objPtrTmp + 5
		}
		z.Story[ptr] = z.Story[objptr+5]
	}
	return nil
}

func (z *ZMachine) opcodeInsertObj() error {
	objid := z.remapObjectID(z.Operands[0])
	dstid := z.remapObjectID(z.Operands[1])

	objptr, err := z.getObjectPtr(objid)
	if err != nil {
		return err
	}
	dstptr, err := z.getObjectPtr(dstid)
	if err != nil {
		return err
	}

	if z.Header.Version <= 3 {
		if err := z.unparentObject(objid); err != nil {
			return err
		}
		z.Story[objptr+4] = uint8(dstid)
		z.Story[objptr+5] = z.Story[dstptr+6]
		z.Story[dstptr+6] = uint8(objid)
		return nil
	}
	return fmt.Errorf("write me")
}

func (z *ZMachine) opcodeRemoveObj() error {
	objid := z.Operands[0]
	objptr, err := z.getObjectPtr(objid)
	if err != nil {
		return err
	}

	if z.Header.Version > 3 {
		return fmt.Errorf("write me")
	}
	if err := z.unparentObject(objid); err != nil {
		return err
	}
	z.Story[objptr+4] = 0
	z.Story[objptr+5] = 0
	return nil
}

func (z *ZMachine) getObjectProperty(objid uint16, propid uint32, size *uint8) (uint32, error) {
	ptr, err := z.getObjectPtr(objid)
	if err != nil {
		return 0, err
	}

	if z.Header.Version <= 3 {
		ptr += 7
		addr := z.readUI16(z.Story, ptr)
		ptr = uint32(addr)
		ptr += uint32(z.Story[ptr]*2) + 1
		for {
			info := z.Story[ptr]
			ptr++
			num := uint32(info & 0x1F)
			sz := ((info >> 5) & 0x7) + 1
			if num == propid || propid == 0xFFFFFFFF {
				if size != nil {
					*size = sz
				}
				return ptr, nil
			} else if num < propid {
				break
			}
			ptr += uint32(sz)
		}
		return 0, nil
	}
	return 0, fmt.Errorf("write me")
}

func (z *ZMachine) opcodePutProp() error {
	objid := z.Operands[0]
	propid := z.Operands[1]
	value := z.Operands[2]
	var size uint8
	ptr, err := z.getObjectProperty(objid, uint32(propid), &size)
	if err != nil {
		return err
	}

	if ptr == 0 {
		return fmt.Errorf("lookup on missing object property")
	} else if size == 1 {
		z.Story[ptr] = uint8(value & 0xFF)
	} else {
		z.writeUI16(z.Story, ptr, value)
	}
	return nil
}

func (z *ZMachine) getDefaultObjectProperty(propid uint16) uint16 {
	if (z.Header.Version <= 3 && propid > 31) ||
		(z.Header.Version >= 4 && propid > 63) {
		return 0
	}

	offset := uint32(z.Header.ObjtabAddr)
	offset += uint32(propid-1) * 2
	return z.readUI16(z.Story, offset)
}

func (z *ZMachine) opcodeGetProp() error {
	store := z.Story[z.PC]
	z.PC++
	objid := z.Operands[0]
	propid := z.Operands[1]
	var result uint16
	var size uint8
	ptr, err := z.getObjectProperty(objid, uint32(propid), &size)
	if err != nil {
		return err
	}

	if ptr == 0 {
		result = z.getDefaultObjectProperty(propid)
	} else if size == 1 {
		result = uint16(z.Story[ptr])
	} else {
		result = z.readUI16(z.Story, ptr)
	}
	return z.writeVar(store, result, false)
}

func (z *ZMachine) opcodeGetPropAddr() error {
	store := z.Story[z.PC]
	z.PC++
	objid := z.Operands[0]
	propid := z.Operands[1]
	ptr, err := z.getObjectProperty(objid, uint32(propid), nil)
	if err != nil {
		return err
	}
	return z.writeVar(store, uint16(ptr), false)
}

func (z *ZMachine) opcodeGetPropLen() error {
	store := z.Story[z.PC]
	z.PC++
	var result uint16

	if z.Operands[0] == 0 {
		result = 0
	} else if z.Header.Version <= 3 {
		offset := uint32(z.Operands[0])
		info := z.Story[offset-1]
		result = uint16(((info >> 5) & 0x7) + 1)
	} else {
		return fmt.Errorf("write me")
	}

	return z.writeVar(store, result, false)
}

func (z *ZMachine) opcodeGetNextProp() error {
	store := z.Story[z.PC]
	z.PC++
	objid := z.Operands[0]
	firstProp := z.Operands[1] == 0
	var result uint16
	var size uint8
	targetProp := uint32(z.Operands[1])
	if firstProp {
		targetProp = 0xFFFFFFFF
	}
	ptr, err := z.getObjectProperty(objid, targetProp, &size)
	if err != nil {
		return err
	}

	if ptr == 0 {
		return fmt.Errorf("get_next_prop on missing property")
	} else if z.Header.Version <= 3 {
		offset := ptr - 1
		if !firstProp {
			offset = ptr + uint32(size)
		}
		result = uint16(z.Story[offset] & 0x1F)
	} else {
		return fmt.Errorf("write me")
	}
	return z.writeVar(store, result, false)
}

func (z *ZMachine) opcodeJin() error {
	objid := z.Operands[0]
	parentid := z.Operands[1]
	if objid == 0 {
		return nil
	}
	objptr, err := z.getObjectPtr(objid)
	if err != nil {
		return err
	}

	if z.Header.Version <= 3 {
		return z.doBranch(uint16(z.Story[objptr+4]) == parentid)
	}
	return fmt.Errorf("write me")
}

func (z *ZMachine) getObjectRelationship(objid uint16, relationship uint8) (uint16, error) {
	objptr, err := z.getObjectPtr(objid)
	if err != nil {
		return 0, err
	}
	if z.Header.Version <= 3 {
		return uint16(z.Story[objptr+uint32(relationship)]), nil
	}
	return 0, fmt.Errorf("write me")
}

func (z *ZMachine) opcodeGetParent() error {
	store := z.Story[z.PC]
	z.PC++
	result, err := z.getObjectRelationship(z.Operands[0], 4)
	if err != nil {
		return err
	}
	return z.writeVar(store, result, false)
}

func (z *ZMachine) opcodeGetSibling() error {
	store := z.Story[z.PC]
	z.PC++
	result, err := z.getObjectRelationship(z.Operands[0], 5)
	if err != nil {
		return err
	}
	if err := z.writeVar(store, result, false); err != nil {
		return err
	}
	return z.doBranch(result != 0)
}

func (z *ZMachine) opcodeGetChild() error {
	store := z.Story[z.PC]
	z.PC++
	result, err := z.getObjectRelationship(z.Operands[0], 6)
	if err != nil {
		return err
	}
	if err := z.writeVar(store, result, false); err != nil {
		return err
	}
	return z.doBranch(result != 0)
}

func (z *ZMachine) opcodeNewLine() error {
	if z.OutputChan != nil {
		z.OutputChan <- []byte("\n")
	}
	return nil
}

func (z *ZMachine) decodeZsciiChar(val uint16) byte {
	var ch byte
	if val >= 32 && val <= 126 {
		ch = byte(val)
	} else if val == 13 {
		ch = '\n'
	} else if val >= 155 && val <= 251 {
		ch = '?'
	} else if val != 0 {
		ch = '?'
	}
	return ch
}

func (z *ZMachine) decodeZscii(str []byte, abbr int, buf []byte) (int, int, error) {
	buflen := len(buf)
	decodedChars := 0
	strIdx := 0
	var code uint16
	alphabet := uint8(0)
	useAbbrTable := uint8(0)
	zsciiCollector := uint8(0)
	var zsciiCode uint16

	for {
		code = z.readUI16(str, uint32(strIdx))
		strIdx += 2

		for i := int8(10); i >= 0; i -= 5 {
			newshift := 0
			var printVal byte
			ch := uint8((code >> i) & 0x1F)

			if zsciiCollector > 0 {
				if zsciiCollector == 2 {
					zsciiCode |= uint16(ch) << 5
				} else {
					zsciiCode |= uint16(ch)
				}
				zsciiCollector--
				if zsciiCollector == 0 {
					printVal = z.decodeZsciiChar(zsciiCode)
					if printVal != 0 {
						decodedChars++
						if buflen > 0 {
							buf[len(buf)-buflen] = printVal
							buflen--
						}
					}
					alphabet = 0
					useAbbrTable = 0
					zsciiCode = 0
				}
				continue
			} else if useAbbrTable > 0 {
				if abbr != 0 {
					return 0, 0, fmt.Errorf("abbreviation strings can't use abbreviations")
				}
				index := uint32(32*(useAbbrTable-1) + ch)
				ptr := uint32(z.Header.AbbrtabAddr) + index*2
				abbraddr := z.readUI16(z.Story, ptr)

				abbrSlice := make([]byte, buflen)
				if buflen > 0 {
					abbrSlice = buf[len(buf)-buflen:]
				}

				abbrDecodedChars, _, err := z.decodeZscii(z.Story[abbraddr*2:], 1, abbrSlice)
				if err != nil {
					return 0, 0, err
				}
				decodedChars += abbrDecodedChars
				if buflen < abbrDecodedChars {
					buflen = 0
				} else {
					buflen -= abbrDecodedChars
				}

				useAbbrTable = 0
				alphabet = 0
				continue
			}

			switch ch {
			case 0:
				printVal = ' '
			case 1:
				if z.Header.Version == 1 {
					printVal = '\n'
				} else {
					useAbbrTable = 1
				}
			case 2, 3:
				if z.Header.Version <= 2 {
					return 0, 0, fmt.Errorf("write me: handle ver1/2 alphabet shifting")
				}
				useAbbrTable = ch
			case 4, 5:
				if z.Header.Version <= 2 {
					return 0, 0, fmt.Errorf("write me: handle ver1/2 alphabet shift locking")
				}
				newshift = 1
				alphabet = ch - 3
			default:
				if ch == 6 && alphabet == 2 {
					zsciiCollector = 2
				} else {
					printVal = z.AlphabetTable[int(alphabet)*26+int(ch)-6]
				}
			}

			if printVal != 0 {
				decodedChars++
				if buflen > 0 {
					buf[len(buf)-buflen] = printVal
					buflen--
				}
			}

			if alphabet != 0 && newshift == 0 {
				alphabet = 0
			}
		}

		if (code & (1 << 15)) != 0 {
			break
		}
	}

	return decodedChars, strIdx, nil
}

func (z *ZMachine) printZscii(str []byte, abbr int) (int, error) {
	var buf [512]byte
	decodedChars, retval, err := z.decodeZscii(str, abbr, buf[:])
	if err != nil {
		return 0, err
	}
	if decodedChars > len(buf) {
		bigBuf := make([]byte, decodedChars)
		_, _, err = z.decodeZscii(str, abbr, bigBuf)
		if err != nil {
			return 0, err
		}
		if z.OutputChan != nil {
			z.OutputChan <- bigBuf
		}
	} else {
		if z.OutputChan != nil {
			z.OutputChan <- buf[:decodedChars]
		}
	}
	return retval, nil
}

func (z *ZMachine) opcodePrint() error {
	bytesConsumed, err := z.printZscii(z.Story[z.PC:], 0)
	if err != nil {
		return err
	}
	z.PC += uint32(bytesConsumed)
	return nil
}

func (z *ZMachine) opcodePrintNum() error {
	str := fmt.Sprintf("%d", int16(z.Operands[0]))
	if z.OutputChan != nil {
		z.OutputChan <- []byte(str)
	}
	return nil
}

func (z *ZMachine) opcodePrintChar() error {
	ch := z.decodeZsciiChar(z.Operands[0])
	if ch != 0 && z.OutputChan != nil {
		z.OutputChan <- []byte{ch}
	}
	return nil
}

func (z *ZMachine) opcodePrintRet() error {
	bytesConsumed, err := z.printZscii(z.Story[z.PC:], 0)
	if err != nil {
		return err
	}
	z.PC += uint32(bytesConsumed)
	if z.OutputChan != nil {
		z.OutputChan <- []byte("\n")
	}
	return z.doReturn(1)
}

func (z *ZMachine) opcodePrintObj() error {
	ptr, err := z.getObjectPtr(z.Operands[0])
	if err != nil {
		return err
	}
	if z.Header.Version <= 3 {
		ptr += 7
		addr := z.readUI16(z.Story, ptr)
		_, err = z.printZscii(z.Story[addr+1:], 0)
		return err
	}
	return fmt.Errorf("write me")
}

func (z *ZMachine) opcodePrintAddr() error {
	_, err := z.printZscii(z.Story[z.Operands[0]:], 0)
	return err
}

func (z *ZMachine) opcodePrintPaddr() error {
	addr, err := z.unpackAddress(uint32(z.Operands[0]))
	if err != nil {
		return err
	}
	_, err = z.printZscii(z.Story[addr:], 0)
	return err
}

func (z *ZMachine) randomNumber() int {
	z.RandomSeed = z.RandomSeed*1103515245 + 12345
	return int((uint32(z.RandomSeed) / 65536) % 32768)
}

func (z *ZMachine) doRandom(rng int16) uint16 {
	var result uint16
	if rng == 0 {
		if z.InitialSeed != 0 {
			z.RandomSeed = z.InitialSeed
		} else {
			z.RandomSeed = int32(time.Now().Unix())
		}
	} else if rng < 0 {
		z.RandomSeed = -int32(rng)
	} else {
		lo := uint16(1)
		hi := uint16(rng)
		result = (uint16(z.randomNumber()) % ((hi + 1) - lo)) + lo
		if result == 0 {
			result = 1
		}
	}
	return result
}

func (z *ZMachine) opcodeRandom() error {
	store := z.Story[z.PC]
	z.PC++
	rng := int16(z.Operands[0])
	result := z.doRandom(rng)
	return z.writeVar(store, result, false)
}

func (z *ZMachine) tokenizeUserInput() error {
	tableA2V1 := []byte("0123456789.,!?_#'\"/\\<-:()")
	tableA2V2plus := []byte("\n0123456789.,!?_#'\"/\\-:()")

	tableA2 := tableA2V2plus
	if z.Header.Version <= 1 {
		tableA2 = tableA2V1
	}

	inputAddr := z.Operands[0]
	parseAddr := z.Operands[1]

	input := z.Story[inputAddr:]
	parse := z.Story[parseAddr:]
	parselen := parse[0]

	sepsAddr := uint32(z.Header.DictAddr)
	numseps := z.Story[sepsAddr]
	sepsAddr++
	seps := z.Story[sepsAddr : sepsAddr+uint32(numseps)]

	dictAddr := sepsAddr + uint32(numseps)
	entrylen := z.Story[dictAddr]
	dictAddr++
	numentries := z.readUI16(z.Story, dictAddr)
	dictAddr += 2

	var numToks uint8

	// input[0] is max length of input buffer. input[1] onwards is the string.
	strStart := uint32(1)
	ptr := uint32(1)

	// Build map for A2 index
	tableA2Map := make(map[byte]int)
	for i, b := range tableA2 {
		tableA2Map[b] = i
	}

	for {
		isSep := false
		ch := input[ptr]
		if ch == ' ' || ch == 0 {
			isSep = true
		} else {
			for _, sep := range seps {
				if ch == sep {
					isSep = true
					break
				}
			}
		}

		if isSep {
			var encoded [3]uint16
			toklen := uint8(ptr - strStart)
			if toklen == 0 {
				break
			}

			var zchars [12]uint8
			zchidx := 0
			for i := uint8(0); i < toklen; i++ {
				c := input[strStart+uint32(i)]
				if c >= 'a' && c <= 'z' {
					zchars[zchidx] = (c - 'a') + 6
					zchidx++
				} else if c >= 'A' && c <= 'Z' {
					zchars[zchidx] = (c - 'A') + 6
					zchidx++
				} else {
					if idx, ok := tableA2Map[c]; ok {
						zchars[zchidx] = 3
						zchars[zchidx+1] = uint8(idx + 1 + 6)
						zchidx += 2
					}
				}
				if zchidx >= 12 {
					break
				}
			}

			pos := 0
			for j := 0; j < 6; j++ {
				shift := uint((2 - (j % 3)) * 5)
				val := uint16(5)
				if pos < zchidx {
					val = uint16(zchars[pos])
					pos++
				}
				encoded[j/3] |= val << shift
			}

			dictptr := dictAddr
			var i uint16
			if z.Header.Version <= 3 {
				encoded[1] |= 0x8000

				for i = 0; i < numentries; i++ {
					zscii1 := z.readUI16(z.Story, dictptr)
					zscii2 := z.readUI16(z.Story, dictptr+2)
					if encoded[0] == zscii1 && encoded[1] == zscii2 {
						break
					}
					dictptr += uint32(entrylen)
				}
			} else {
				return fmt.Errorf("write me")
			}

			var dictaddrVal uint16
			if i < numentries {
				dictaddrVal = uint16(dictptr)
			}

			parseOffset := uint32(1) + uint32(numToks)*4
			z.writeUI16(parse, parseOffset+1, dictaddrVal)
			parse[parseOffset+3] = toklen
			parse[parseOffset+4] = uint8((strStart - 1) + 1)
			numToks++

			if numToks >= parselen {
				break
			}

			strStart = ptr + 1
		}

		if ch == 0 {
			break
		}
		ptr++
	}

	parse[1] = numToks
	return nil
}

func (z *ZMachine) opcodeRead() error {
	inputAddr := z.Operands[0]
	parseAddr := z.Operands[1]
	input := z.Story[inputAddr:]
	inputlen := input[0]
	if inputlen < 3 {
		return fmt.Errorf("text buffer is too small for reading")
	}

	parse := z.Story[parseAddr:]
	parselen := parse[0]
	if parselen == 0 {
		return fmt.Errorf("parse buffer is too small for reading")
	}

	z.updateStatusBar()

	line, ok := <-z.InputChan
	if !ok {
		z.Quit = true
		z.StepCompleted = true
		return nil
	}

	// emulate fgets capping
	if len(line) > int(inputlen)-1 {
		line = line[:inputlen-1]
	}

	buf := []byte(line)
	sz := len(buf)
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		if c >= 'A' && c <= 'Z' {
			buf[i] = c - 'A' + 'a'
		} else if c == '\n' || c == '\r' {
			buf[i] = 0
			sz = i
			break
		}
	}
	if sz == len(buf) && sz < int(inputlen) {
		// add null terminator if not already there
		buf = append(buf, 0)
	}

	copy(input[1:], buf[:sz+1])

	return z.tokenizeUserInput()
}

func (z *ZMachine) opcodeVerify() error {
	return z.doBranch(z.CalculatedChecksum == z.Header.StoryChecksum)
}

func (z *ZMachine) opcodeSplitWindow() error {
	if (z.Header.Flags1 & (1 << 5)) == 0 {
		return fmt.Errorf("split_window called but implementation doesn't support it")
	}
	return nil
}

func (z *ZMachine) opcodeSetWindow() error {
	if (z.Header.Flags1 & (1 << 5)) == 0 {
		return fmt.Errorf("set_window called but implementation doesn't support it")
	}
	return nil
}

// LoadStory loads a Z-Machine story file and initializes the virtual machine.
func (z *ZMachine) LoadStory(fname string) error {
	story, err := os.ReadFile(fname)
	if err != nil {
		return fmt.Errorf("failed to open '%s': %w", fname, err)
	}

	z.Story = story
	z.StoryFilename = fname
	z.InstructionsRun = 0
	z.PC = 0
	z.LogicalPC = 0
	z.Quit = false
	z.OperandCount = 0
	z.SP = 0
	z.BP = 0

	z.Story[1] |= (1 << 4) // no status bar by default

	ptr := uint32(0)
	z.Header.Version = story[ptr]
	ptr++
	z.Header.Flags1 = story[ptr]
	ptr++
	z.Header.Release = z.readUI16(story, ptr)
	ptr += 2
	z.Header.HimemAddr = z.readUI16(story, ptr)
	ptr += 2
	z.Header.PcStart = z.readUI16(story, ptr)
	ptr += 2
	z.Header.DictAddr = z.readUI16(story, ptr)
	ptr += 2
	z.Header.ObjtabAddr = z.readUI16(story, ptr)
	ptr += 2
	z.Header.GlobalsAddr = z.readUI16(story, ptr)
	ptr += 2
	z.Header.StaticmemAddr = z.readUI16(story, ptr)
	ptr += 2
	z.Header.Flags2 = z.readUI16(story, ptr)
	ptr += 2
	for i := 0; i < 6; i++ {
		z.Header.SerialCode[i] = story[ptr]
		ptr++
	}
	z.Header.AbbrtabAddr = z.readUI16(story, ptr)
	ptr += 2
	z.Header.StoryLen = z.readUI16(story, ptr)
	ptr += 2
	z.Header.StoryChecksum = z.readUI16(story, ptr)

	if z.Header.Version != 3 {
		return fmt.Errorf("only version 3 is supported right now")
	}

	total := uint32(z.Header.StoryLen) * 2
	var checksum uint16
	for i := uint32(0x40); i < total; i++ {
		checksum += uint16(story[i])
	}
	z.CalculatedChecksum = checksum

	z.initAlphabetTable()
	z.initOpcodeTable()

	z.PC = uint32(z.Header.PcStart)
	z.LogicalPC = uint32(z.Header.PcStart)
	return nil
}

func (z *ZMachine) opcodeRestart() error {
	return z.LoadStory(z.StoryFilename)
}

func (z *ZMachine) saveFile() string {
	if z.SaveFile != "" {
		return z.SaveFile
	}
	return "save.dat"
}

func (z *ZMachine) opcodeSave() error {
	f, err := os.Create(z.saveFile())
	if err != nil {
		return z.doBranch(false)
	}
	defer f.Close()

	okay := true
	write := func(v interface{}) {
		if okay {
			if err := binary.Write(f, binary.LittleEndian, v); err != nil {
				okay = false
			}
		}
	}

	// Dynamic memory: story[0..staticmem_addr]
	if _, err := f.Write(z.Story[:z.Header.StaticmemAddr]); err != nil {
		okay = false
	}
	write(z.PC)
	write(uint32(z.SP))
	write(z.Stack)
	write(int32(z.BP))

	return z.doBranch(okay)
}

func (z *ZMachine) opcodeRestore() error {
	f, err := os.Open(z.saveFile())
	if err != nil {
		return z.doBranch(false)
	}
	defer f.Close()

	okay := true
	var pc uint32
	var sp uint32
	var bp int32

	read := func(v interface{}) {
		if okay {
			if err := binary.Read(f, binary.LittleEndian, v); err != nil {
				okay = false
			}
		}
	}

	// Dynamic memory: story[0..staticmem_addr]
	if _, err := io.ReadFull(f, z.Story[:z.Header.StaticmemAddr]); err != nil {
		okay = false
	}
	read(&pc)
	read(&sp)
	read(&z.Stack)
	read(&bp)

	if okay {
		z.PC = pc
		z.LogicalPC = pc
		z.SP = int(sp)
		z.BP = int(bp)
	}

	return z.doBranch(okay)
}

func (z *ZMachine) opcodeQuit() error {
	z.Quit = true
	z.StepCompleted = true
	return nil
}

func (z *ZMachine) opcodeNop() error {
	return nil
}

func (z *ZMachine) parseOperand(optype uint8, idx uint8) (bool, error) {
	switch optype {
	case 0:
		z.Operands[idx] = z.readUI16(z.Story, z.PC)
		z.PC += 2
		return true, nil
	case 1:
		z.Operands[idx] = uint16(z.Story[z.PC])
		z.PC++
		return true, nil
	case 2:
		v := z.Story[z.PC]
		z.PC++
		var err error
		z.Operands[idx], err = z.readVar(v, false)
		if err != nil {
			return false, err
		}
		return true, nil
	case 3:
		return false, nil
	}
	return false, nil
}

func (z *ZMachine) parseVarOperands(startIdx uint8) (uint8, error) {
	operandTypes := z.Story[z.PC]
	z.PC++
	shifter := int8(6)
	var i uint8
	for i = 0; i < 4; i++ {
		optype := (operandTypes >> uint8(shifter)) & 0x3
		shifter -= 2
		ok, err := z.parseOperand(optype, startIdx+i)
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
	}
	return i, nil
}

// RunInstruction fetches and executes the next Z-Machine instruction.
func (z *ZMachine) RunInstruction() error {
	z.LogicalPC = z.PC
	opcode := z.Story[z.PC]
	z.PC++

	if opcode <= 127 { // 2OP
		z.OperandCount = 2
		b1 := uint8(1)
		if (opcode & (1 << 6)) != 0 {
			b1 = 2
		}
		if _, err := z.parseOperand(b1, 0); err != nil {
			return err
		}
		b2 := uint8(1)
		if (opcode & (1 << 5)) != 0 {
			b2 = 2
		}
		if _, err := z.parseOperand(b2, 1); err != nil {
			return err
		}
	} else if opcode <= 175 { // 1OP
		z.OperandCount = 1
		optype := (opcode >> 4) & 0x3
		if _, err := z.parseOperand(optype, 0); err != nil {
			return err
		}
	} else if opcode <= 191 { // 0OP
		z.OperandCount = 0
	} else { // VAR
		var err error
		z.OperandCount, err = z.parseVarOperands(0)
		if err != nil {
			return err
		}
	}

	fn := z.Opcodes[opcode]

	if fn == nil {
		return fmt.Errorf("unimplemented opcode #%d", opcode)
	}

	if err := fn(); err != nil {
		return err
	}
	z.InstructionsRun++
	return nil
}

func (z *ZMachine) initAlphabetTable() {
	ptr := 0
	for i := 0; i < 26; i++ {
		z.AlphabetTable[ptr] = byte('a' + i)
		ptr++
	}
	for i := 0; i < 26; i++ {
		z.AlphabetTable[ptr] = byte('A' + i)
		ptr++
	}
	z.AlphabetTable[ptr] = 0 // A2
	ptr++
	z.AlphabetTable[ptr] = '\n'
	ptr++
	for i := 0; i < 10; i++ {
		z.AlphabetTable[ptr] = byte('0' + i)
		ptr++
	}
	for _, c := range []byte(".,!?_#'\"/\\-:") {
		z.AlphabetTable[ptr] = c
		ptr++
	}
	z.AlphabetTable[ptr] = '('
	ptr++
	z.AlphabetTable[ptr] = ')'
	ptr++
}

func (z *ZMachine) defOp(num int, fn func() error) {
	z.Opcodes[num] = fn
}

func (z *ZMachine) initOpcodeTable() {
	z.defOp(1, z.opcodeJe)
	z.defOp(2, z.opcodeJl)
	z.defOp(3, z.opcodeJg)
	z.defOp(4, z.opcodeDecChk)
	z.defOp(5, z.opcodeIncChk)
	z.defOp(6, z.opcodeJin)
	z.defOp(7, z.opcodeTest)
	z.defOp(8, z.opcodeOr)
	z.defOp(9, z.opcodeAnd)
	z.defOp(10, z.opcodeTestAttr)
	z.defOp(11, z.opcodeSetAttr)
	z.defOp(12, z.opcodeClearAttr)
	z.defOp(13, z.opcodeStore)
	z.defOp(14, z.opcodeInsertObj)
	z.defOp(15, z.opcodeLoadw)
	z.defOp(16, z.opcodeLoadb)
	z.defOp(17, z.opcodeGetProp)
	z.defOp(18, z.opcodeGetPropAddr)
	z.defOp(19, z.opcodeGetNextProp)
	z.defOp(20, z.opcodeAdd)
	z.defOp(21, z.opcodeSub)
	z.defOp(22, z.opcodeMul)
	z.defOp(23, z.opcodeDiv)
	z.defOp(24, z.opcodeMod)

	z.defOp(128, z.opcodeJz)
	z.defOp(129, z.opcodeGetSibling)
	z.defOp(130, z.opcodeGetChild)
	z.defOp(131, z.opcodeGetParent)
	z.defOp(132, z.opcodeGetPropLen)
	z.defOp(133, z.opcodeInc)
	z.defOp(134, z.opcodeDec)
	z.defOp(135, z.opcodePrintAddr)
	z.defOp(137, z.opcodeRemoveObj)
	z.defOp(138, z.opcodePrintObj)
	z.defOp(139, z.opcodeRet)
	z.defOp(140, z.opcodeJump)
	z.defOp(141, z.opcodePrintPaddr)
	z.defOp(142, z.opcodeLoad)
	z.defOp(143, z.opcodeNot)

	z.defOp(176, z.opcodeRtrue)
	z.defOp(177, z.opcodeRfalse)
	z.defOp(178, z.opcodePrint)
	z.defOp(179, z.opcodePrintRet)
	z.defOp(180, z.opcodeNop)
	z.defOp(181, z.opcodeSave)
	z.defOp(182, z.opcodeRestore)
	z.defOp(183, z.opcodeRestart)
	z.defOp(184, z.opcodeRetPopped)
	z.defOp(185, z.opcodePop)
	z.defOp(186, z.opcodeQuit)
	z.defOp(187, z.opcodeNewLine)
	z.defOp(188, z.opcodeShowStatus)
	z.defOp(189, z.opcodeVerify)

	z.defOp(224, z.opcodeCall)
	z.defOp(225, z.opcodeStorew)
	z.defOp(226, z.opcodeStoreb)
	z.defOp(227, z.opcodePutProp)
	z.defOp(228, z.opcodeRead)
	z.defOp(229, z.opcodePrintChar)
	z.defOp(230, z.opcodePrintNum)
	z.defOp(231, z.opcodeRandom)
	z.defOp(232, z.opcodePush)
	z.defOp(233, z.opcodePull)

	for i := 32; i <= 127; i++ {
		z.Opcodes[i] = z.Opcodes[i%32]
	}
	for i := 144; i <= 175; i++ {
		z.Opcodes[i] = z.Opcodes[128+(i%16)]
	}
	for i := 192; i <= 223; i++ {
		z.Opcodes[i] = z.Opcodes[i%32]
	}
}

// Run executes the Z-Machine in a goroutine. The returned channel receives
// an error if execution fails, and is closed when the machine halts.
func (z *ZMachine) Run() <-chan error {
	done := make(chan error, 1)
	go func() {
		defer close(done)
		defer close(z.OutputChan) // signals output consumer to stop; runs first (LIFO)
		for !z.Quit {
			if err := z.RunInstruction(); err != nil {
				done <- err
				return
			}
		}
	}()
	return done
}
