package p2

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestCRC8KnownValues(t *testing.T) {
	on := make([]byte, 16)
	copy(on, PayloadON)
	if got := CRC8(on); got != 0x33 {
		t.Fatalf("CRC8(ON) = %#x, want 0x33", got)
	}

	off := make([]byte, 16)
	copy(off, PayloadOFF)
	if got := CRC8(off); got != 0xD6 {
		t.Fatalf("CRC8(OFF) = %#x, want 0xd6", got)
	}
}

func TestBuildParseAndRecoverRoundTrip(t *testing.T) {
	localKey := []byte("0123456789abcdef")
	beaconKey := []byte("fedcba9876543210")
	src := [2]byte{0x80, 0x01}
	dst := [2]byte{0x00, 0x03}

	frame, err := BuildFrame(src, dst, 1234, PayloadON, localKey, beaconKey)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	if len(frame) != FrameLen {
		t.Fatalf("len(frame) = %d, want %d", len(frame), FrameLen)
	}

	adv := Advertisement(frame)
	if len(adv) != 31 || !bytes.Equal(adv[:3], Flags) {
		t.Fatalf("invalid advertisement: %x", adv)
	}
	if extracted := FindFrame(adv); !bytes.Equal(extracted, frame) {
		t.Fatalf("FindFrame mismatch")
	}

	dec, err := ParseFrame(frame, localKey, beaconKey)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if dec.Src != src || dec.Dst != dst || dec.Seq != 1234 || dec.SubCmd != SetDPs {
		t.Fatalf("decoded metadata mismatch: %+v", dec)
	}
	dps := DPParse(dec.Payload)
	if len(dps) != 1 || dps[0].ID != 1 || dps[0].Type != DPTypeBool || !bytes.Equal(dps[0].Value, []byte{1}) {
		t.Fatalf("DPParse mismatch: %+v", dps)
	}

	recovered, err := RecoverBeaconKey(frame, PayloadON, localKey)
	if err != nil {
		t.Fatalf("RecoverBeaconKey: %v", err)
	}
	if !bytes.Equal(recovered, beaconKey) {
		t.Fatalf("recovered beaconKey = %x, want %x", recovered, beaconKey)
	}
}

func TestFindFrameWithLimitedDiscoverableFlags(t *testing.T) {
	// Real capture from Zinnia Crux CR90 (starts with 02 01 01 1b 03 ...)
	raw, _ := hex.DecodeString("0201011b030b7d690003020c0533c03ed88d7ab22501ab8eec0dd0e268eb94")
	frame := FindFrame(raw)
	if len(frame) != FrameLen {
		t.Fatalf("expected 26-byte frame, got %d", len(frame))
	}
	if frame[8] != 0x33 {
		t.Fatalf("expected ON CRC 0x33, got %#x", frame[8])
	}
}
