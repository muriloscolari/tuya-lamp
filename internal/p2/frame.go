package p2

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	FrameLen = 26
	SetDPs   = 0x05

	DPTypeRaw    byte = 0
	DPTypeBool   byte = 1
	DPTypeValue  byte = 2
	DPTypeString byte = 3
	DPTypeEnum   byte = 4
)

var (
	Flags = []byte{0x02, 0x01, 0x06}

	PayloadON  = MustDPPayload([]DP{{ID: 1, Type: DPTypeBool, Value: []byte{0x01}}})
	PayloadOFF = MustDPPayload([]DP{{ID: 1, Type: DPTypeBool, Value: []byte{0x00}}})
)

// DP represents a single Tuya Data Point.
type DP struct {
	ID    byte
	Type  byte
	Value []byte
}

// DecodedFrame represents a validated P2 frame.
type DecodedFrame struct {
	Src     [2]byte
	Dst     [2]byte
	Seq     uint16
	SubCmd  byte
	Payload []byte
}

// KeyFromString parses a 16-char ASCII or 32-char hex Tuya key.
func KeyFromString(s string) ([]byte, error) {
	switch len(s) {
	case 16:
		return []byte(s), nil
	case 32:
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("p2: invalid hex key: %w", err)
		}
		return b, nil
	default:
		return nil, errors.New("p2: key must be 16 ASCII characters or 32 hex digits")
	}
}

// DPPayload encodes one or more DPs into a compact TLV stream (max 16 bytes).
func DPPayload(dps []DP) ([]byte, error) {
	out := make([]byte, 0, 16)
	for _, dp := range dps {
		if len(dp.Value) > 15 {
			return nil, fmt.Errorf("p2: dp %d value too long (%d bytes)", dp.ID, len(dp.Value))
		}
		out = append(out, dp.ID, (dp.Type<<4)|byte(len(dp.Value)))
		out = append(out, dp.Value...)
	}
	if len(out) > 16 {
		return nil, fmt.Errorf("p2: DP payload exceeds 16 bytes (%d)", len(out))
	}
	return out, nil
}

// MustDPPayload panics if encoding fails (for static payloads).
func MustDPPayload(dps []DP) []byte {
	b, err := DPPayload(dps)
	if err != nil {
		panic(err)
	}
	return b
}

// DPParse decodes a 16-byte padded plaintext payload into DPs.
func DPParse(payload []byte) []DP {
	var dps []DP
	i := 0
	for i+2 <= len(payload) && payload[i] != 0 {
		id := payload[i]
		kind := payload[i+1]
		size := int(kind & 0x0F)
		if i+2+size > len(payload) {
			break
		}
		val := make([]byte, size)
		copy(val, payload[i+2:i+2+size])
		dps = append(dps, DP{ID: id, Type: kind >> 4, Value: val})
		i += 2 + size
	}
	return dps
}

// BuildFrame builds a 26-byte Tuya Beacon P2 frame.
func BuildFrame(src, dst [2]byte, seq uint16, payload, localKey, beaconKey []byte) ([]byte, error) {
	if len(localKey) != 16 || len(beaconKey) != 16 {
		return nil, errors.New("p2: localKey and beaconKey must each be 16 bytes")
	}
	if len(payload) > 16 {
		return nil, errors.New("p2: payload exceeds 16 bytes")
	}

	plain := make([]byte, 16)
	copy(plain, payload)

	cipher, err := XXTEA(plain, localKey, false)
	if err != nil {
		return nil, err
	}

	header := byte((1 << 3) | 3) // 0x0b
	middle := make([]byte, 0, 8)
	middle = append(middle, src[0], src[1], dst[0], dst[1])
	var seqBuf [2]byte
	binary.BigEndian.PutUint16(seqBuf[:], seq)
	middle = append(middle, seqBuf[0], seqBuf[1], SetDPs, CRC8(plain))

	crcInput := make([]byte, 0, 25)
	crcInput = append(crcInput, header&0xFC)
	crcInput = append(crcInput, middle...)
	crcInput = append(crcInput, cipher...)
	outerCRC := CRC8(crcInput)

	frame := make([]byte, FrameLen)
	frame[0] = header
	copy(frame[1:9], middle)
	for i := 0; i < 16; i++ {
		frame[9+i] = cipher[i] ^ beaconKey[i]
	}
	frame[25] = outerCRC
	return frame, nil
}

// ParseFrame validates and decrypts a 26-byte P2 frame.
func ParseFrame(frame, localKey, beaconKey []byte) (*DecodedFrame, error) {
	if len(frame) != FrameLen {
		return nil, errors.New("p2: invalid frame length")
	}
	if len(localKey) != 16 || len(beaconKey) != 16 {
		return nil, errors.New("p2: invalid key length")
	}

	cipher := make([]byte, 16)
	for i := 0; i < 16; i++ {
		cipher[i] = frame[9+i] ^ beaconKey[i]
	}

	crcInput := make([]byte, 0, 25)
	crcInput = append(crcInput, frame[0]&0xFC)
	crcInput = append(crcInput, frame[1:9]...)
	crcInput = append(crcInput, cipher...)
	if CRC8(crcInput) != frame[25] {
		return nil, errors.New("p2: outer CRC mismatch")
	}

	plain, err := XXTEA(cipher, localKey, true)
	if err != nil {
		return nil, err
	}
	if CRC8(plain) != frame[8] {
		return nil, errors.New("p2: inner CRC mismatch")
	}

	return &DecodedFrame{
		Src:     [2]byte{frame[1], frame[2]},
		Dst:     [2]byte{frame[3], frame[4]},
		Seq:     binary.BigEndian.Uint16(frame[5:7]),
		SubCmd:  frame[7],
		Payload: plain,
	}, nil
}

// Advertisement wraps a 26-byte P2 frame into a 31-byte BLE advertising payload.
func Advertisement(frame []byte) []byte {
	out := make([]byte, 0, 31)
	out = append(out, Flags...)
	out = append(out, byte(len(frame)+1), 0x03)
	out = append(out, frame...)
	return out
}

// RecoverBeaconKey derives the 16-byte mesh beaconKey from a captured frame with known plaintext.
func RecoverBeaconKey(frame, payload, localKey []byte) ([]byte, error) {
	if len(frame) != FrameLen {
		return nil, errors.New("p2: invalid frame length")
	}
	plain := make([]byte, 16)
	copy(plain, payload)
	cipher, err := XXTEA(plain, localKey, false)
	if err != nil {
		return nil, err
	}
	beaconKey := make([]byte, 16)
	for i := 0; i < 16; i++ {
		beaconKey[i] = cipher[i] ^ frame[9+i]
	}
	return beaconKey, nil
}

// FindFrame extracts a 26-byte P2 frame from raw BLE advertising data (AD type 0x03).
func FindFrame(advData []byte) []byte {
	i := 0
	for i < len(advData) {
		size := int(advData[i])
		if size == 0 || i+1+size > len(advData) {
			return nil
		}
		if advData[i+1] == 0x03 && size-1 == FrameLen {
			frame := make([]byte, FrameLen)
			copy(frame, advData[i+2:i+1+size])
			return frame
		}
		i += size + 1
	}
	return nil
}
