package frame

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func sampleChannelCmdV2() ChannelCmdV2 {
	var commandID, digest [16]byte
	for i := 0; i < 16; i++ {
		commandID[i] = byte(i)
		digest[i] = byte(0x10 + i)
	}
	return ChannelCmdV2{CommandID: commandID, PayloadDigest: digest, Attempt: 1, BootID: "boot-1", EdgeDeviceID: 7, ChannelID: 9, DeadlineUnixMS: 1700000000000, TXData: []byte{1, 3, 0, 0, 0, 2, 0xc4, 0x0b}, ReadSize: 9, RXTimeoutMS: 1000, PostTXDelayMS: 100, RiskClass: 0, Flags: 0}
}

func TestChannelCmdV2GoldenRoundTrip(t *testing.T) {
	cmd := sampleChannelCmdV2()
	wire, err := EncodeChannelCmdV2(cmd)
	if err != nil {
		t.Fatal(err)
	}
	const golden = "150a10000102030405060708090a0b0c0d0e0f1210101112131415161718191a1b1c1d1e1f18012206626f6f742d31280730093880d095ffbc314208010300000002c40b480950e8075864600068007001"
	if got := hex.EncodeToString(wire); got != golden {
		t.Fatalf("golden mismatch\n got %s\nwant %s", got, golden)
	}
	decoded, err := DecodeChannelCmdV2(wire)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.BootID != cmd.BootID || decoded.Attempt != cmd.Attempt || !bytes.Equal(decoded.TXData, cmd.TXData) || decoded.DeadlineUnixMS != cmd.DeadlineUnixMS {
		t.Fatalf("round trip=%+v", decoded)
	}
}

func TestChannelCmdV2RejectsMalformedEnvelope(t *testing.T) {
	cmd := sampleChannelCmdV2()
	wire, err := EncodeChannelCmdV2(cmd)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := append(append([]byte(nil), wire...), 0x18, 0x02) // field 3 attempt repeated
	if _, err := DecodeChannelCmdV2(duplicate); err == nil {
		t.Fatal("duplicate attempt accepted")
	}
	unknown := append(append([]byte(nil), wire...), 0x78, 0x01) // field 15
	if _, err := DecodeChannelCmdV2(unknown); err == nil {
		t.Fatal("unknown field accepted")
	}
	cmd.TXData = make([]byte, ChannelCmdV2MaxTX+1)
	if _, err := EncodeChannelCmdV2(cmd); err == nil {
		t.Fatal("oversized tx accepted")
	}
}

func TestChannelCmdV2ResponseStrictDecode(t *testing.T) {
	cmd := sampleChannelCmdV2()
	enc := NewEncoder(MsgChannelCmdV2Final)
	enc.EncodeBytes(1, cmd.CommandID[:])
	enc.EncodeBytes(2, cmd.PayloadDigest[:])
	enc.EncodeVarint(3, 1)
	enc.EncodeString(4, cmd.BootID)
	enc.EncodeVarint(5, 3)
	enc.EncodeVarint(6, 1)
	enc.EncodeVarint(7, 0)
	enc.EncodeBytes(8, []byte{1, 3, 2, 0, 0})
	enc.EncodeVarint(9, 0)
	response, err := DecodeChannelCmdV2Response(enc.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !response.Final || !response.Success || response.EventSequence != 3 || len(response.RawResponse) != 5 {
		t.Fatalf("response=%+v", response)
	}
	bad := append(append([]byte(nil), enc.Bytes()...), 0x30, 0x04) // duplicate event sequence
	if _, err := DecodeChannelCmdV2Response(bad); err == nil {
		t.Fatal("duplicate field accepted")
	}
}
