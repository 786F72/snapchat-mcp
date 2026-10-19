package service

import (
	"encoding/binary"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func friendBytes(field protowire.Number, data []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, field, protowire.BytesType), data)
}
func friendNumber(field protowire.Number, n uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, field, protowire.VarintType), n)
}
func TestFriendSnapshotRequiresFullSyncAndConfirmedRelationship(t *testing.T) {
	uuid := protowire.AppendTag(nil, 1, protowire.Fixed64Type)
	uuid = protowire.AppendFixed64(uuid, 0x0011223344556677)
	uuid = protowire.AppendTag(uuid, 2, protowire.Fixed64Type)
	uuid = protowire.AppendFixed64(uuid, 0x8899aabbccddeeff)
	friend := append(friendBytes(1, uuid), friendBytes(2, []byte("testfriend"))...)
	friend = append(friend, friendNumber(4, 2)...)
	metadata := friendBytes(1, friendNumber(2, 2))
	outgoing := append(metadata, friendBytes(2, friend)...)
	friends, err := decodeFriends(friendBytes(1, outgoing))
	if err != nil || len(friends) != 1 || friends[0].ID != fixtureConversation {
		t.Fatal(friends, err)
	}
	for _, relation := range []uint64{0, 1, 3, 4, 5, 6, 7, 8, 999} {
		f := append(friendBytes(1, uuid), friendNumber(4, relation)...)
		result, err := decodeFriends(friendBytes(1, append(metadata, friendBytes(2, f)...)))
		if err != nil || len(result) != 0 {
			t.Fatalf("nonfriend relation %d entered confirmed snapshot: %v %v", relation, result, err)
		}
	}
	for _, bad := range [][]byte{nil, friendBytes(1, friendBytes(1, friendNumber(2, 1))), friendBytes(1, append(outgoing, friendBytes(2, friend)...)), friendBytes(1, outgoing[:len(outgoing)-1])} {
		if _, err := decodeFriends(bad); err == nil {
			t.Fatal("accepted incomplete or duplicate friend snapshot")
		}
	}
	// fixed64 is little endian on the wire, but UUID halves are rendered as big
	// endian integers. A regression here could select a different recipient.
	high := binary.LittleEndian.Uint64(uuid[1:9])
	if high != 0x0011223344556677 {
		t.Fatal("fixture endianness")
	}
}
