package p2

import (
	"encoding/binary"
	"errors"
)

const xxteaDelta uint32 = 0x9E3779B9

// XXTEA encrypts or decrypts a 16-byte block using Tuya's big-endian 19-round XXTEA variant.
func XXTEA(block, key []byte, decrypt bool) ([]byte, error) {
	if len(block) != 16 || len(key) != 16 {
		return nil, errors.New("p2: block and key must each be 16 bytes")
	}

	var v [4]uint32
	var k [4]uint32
	for i := 0; i < 4; i++ {
		v[i] = binary.BigEndian.Uint32(block[i*4 : (i+1)*4])
		k[i] = binary.BigEndian.Uint32(key[i*4 : (i+1)*4])
	}

	mix := func(z, y, total uint32, p, e uint32) uint32 {
		return ((z>>5 ^ y<<2) + (y>>3 ^ z<<4)) ^ ((total ^ y) + (k[(p&3)^e] ^ z))
	}

	if decrypt {
		rounds := uint32(19)
		total := rounds * xxteaDelta
		y := v[0]
		for round := 0; round < 19; round++ {
			e := (total >> 2) & 3
			for p := uint32(3); p > 0; p-- {
				z := v[p-1]
				v[p] -= mix(z, y, total, p, e)
				y = v[p]
			}
			z := v[3]
			v[0] -= mix(z, y, total, 0, e)
			y = v[0]
			total -= xxteaDelta
		}
	} else {
		var total uint32
		z := v[3]
		for round := 0; round < 19; round++ {
			total += xxteaDelta
			e := (total >> 2) & 3
			for p := uint32(0); p < 3; p++ {
				y := v[p+1]
				v[p] += mix(z, y, total, p, e)
				z = v[p]
			}
			y := v[0]
			v[3] += mix(z, y, total, 3, e)
			z = v[3]
		}
	}

	out := make([]byte, 16)
	for i := 0; i < 4; i++ {
		binary.BigEndian.PutUint32(out[i*4:(i+1)*4], v[i])
	}
	return out, nil
}

// CRC8 computes Tuya's CRC-8 (polynomial 0x07, initial value 0x00).
func CRC8(data []byte) byte {
	var val uint32
	for _, b := range data {
		val ^= uint32(b) << 8
		for i := 0; i < 8; i++ {
			if val&0x8000 != 0 {
				val = (val ^ 0x8380) << 1
			} else {
				val <<= 1
			}
		}
	}
	return byte((val >> 8) & 0xFF)
}
