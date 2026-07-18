package nodemgr

import (
	"testing"

	"ehome/backend/pkg/frame"
)

func validEdgeHealth() []byte {
	edge := frame.SubEncoder()
	edge.EncodeVarint(1, 17)
	edge.EncodeVarint(2, 0)
	edge.EncodeVarint(3, 2)
	edge.EncodeVarint(4, 1)
	return edge.Bytes()
}

func validChannelHealth() []byte {
	channel := frame.SubEncoder()
	channel.EncodeVarint(1, 9)
	channel.EncodeSubFrame(2, validEdgeHealth())
	return channel.Bytes()
}

func TestValidateChannelHealthAcceptsRawNestedFields(t *testing.T) {
	if err := validateChannelHealth(validChannelHealth()); err != nil {
		t.Fatalf("valid ChannelHealth rejected: %v", err)
	}
}

func TestValidateChannelHealthRejectsTypePrefixAndMalformedEdge(t *testing.T) {
	withTypePrefix := append([]byte{0}, validChannelHealth()...)
	if err := validateChannelHealth(withTypePrefix); err == nil {
		t.Fatal("ChannelHealth with a top-level type byte must be rejected")
	}

	missingStatus := frame.SubEncoder()
	missingStatus.EncodeVarint(1, 17)
	missingStatus.EncodeVarint(2, 0)
	missingStatus.EncodeVarint(3, 2)
	channel := frame.SubEncoder()
	channel.EncodeVarint(1, 9)
	channel.EncodeSubFrame(2, missingStatus.Bytes())
	if err := validateChannelHealth(channel.Bytes()); err == nil {
		t.Fatal("ChannelHealth with incomplete EdgeDeviceHealth must be rejected")
	}
}
