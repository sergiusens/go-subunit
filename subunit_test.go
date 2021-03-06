// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2015 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package subunit_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"time"

	"github.com/elopio/subunit"

	check "gopkg.in/check.v1"
)

var _ = check.Suite(&SubunitSuite{})

type SubunitSuite struct {
	stream *subunit.StreamResultToBytes
	output bytes.Buffer
}

func (s *SubunitSuite) readNumber() int {
	byte1 := s.output.Next(1)[0]
	// Get the first two bits, shift them to the right and add one.
	size := ((byte1 & 0xc0) >> 6) + 1
	// Unset the first two bits.
	b1Value := uint8(byte1) & 0x3f
	switch size {
	case 1:
		return int(b1Value)
	case 2:
		// Add the second byte.
		return int((int16(b1Value) << 8) | int16(s.output.Next(1)[0]))
	case 3:
		// Add the second and third bytes.
		return int(int32(b1Value)<<16 | int32(s.output.Next(1)[0])<<8 |
			int32(s.output.Next(1)[0]))
	case 4:
		// Add the second, third and fourth bytes.
		return int(int32(b1Value)<<24 | int32(s.output.Next(1)[0])<<16 |
			int32(s.output.Next(1)[0])<<8 | int32(s.output.Next(1)[0]))
	}
	// Impossible to get here.
	panic("Something wrong happened reading the number")
}

func (s *SubunitSuite) SetUpSuite(c *check.C) {
	s.stream = &subunit.StreamResultToBytes{Output: &s.output}
}

func (s *SubunitSuite) SetUpTest(c *check.C) {
	s.output.Reset()
}

func (s *SubunitSuite) TestPacketMustContainSignature(c *check.C) {
	s.stream.Status(subunit.Event{TestID: "dummytest", Status: "dummystatus"})
	signature := s.output.Next(1)[0]
	c.Assert(int(signature), check.Equals, 0xb3,
		check.Commentf("Wrong signature"))
}

func (s *SubunitSuite) TestPacketMustContainVersion2Flag(c *check.C) {
	s.stream.Status(subunit.Event{TestID: "dummytest", Status: "dummystatus"})
	s.output.Next(1) // skip the signature.
	flags := s.output.Next(2)
	version := flags[0] >> 4 // 4 first bits of the first byte.
	c.Assert(version, check.Equals, uint8(0x2), check.Commentf("Wrong version"))
}

func (s *SubunitSuite) TestWithoutIDPacketMustNotSetPresentFlag(c *check.C) {
	s.stream.Status(subunit.Event{TestID: "", Status: "dummystatus"})
	s.output.Next(1) // skip the signature.
	flags := s.output.Next(2)
	testIDPresent := flags[0] & 0x8 // bit 11.
	c.Assert(testIDPresent, check.Equals, uint8(0x0),
		check.Commentf("Test ID present flag is set"))
}

func (s *SubunitSuite) TestWithIDPacketMustSetPresentFlag(c *check.C) {
	s.stream.Status(subunit.Event{TestID: "test-id", Status: "dummystatus"})
	s.output.Next(1) // skip the signature.
	flags := s.output.Next(2)
	testIDPresent := flags[0] & 0x8 // bit 11.
	c.Assert(testIDPresent, check.Equals, uint8(0x8),
		check.Commentf("Test ID present flag is not set"))
}

var statustests = []struct {
	status string
	flag   byte
}{
	{"", 0x0},
	{"undefined", 0x0},
	{"exists", 0x1},
	{"inprogress", 0x2},
	{"success", 0x3},
	{"uxsuccess", 0x4},
	{"skip", 0x5},
	{"fail", 0x6},
	{"xfail", 0x7},
}

func (s *SubunitSuite) TestPacketStatusFlag(c *check.C) {
	for _, t := range statustests {
		s.output.Reset()
		s.stream.Status(subunit.Event{TestID: "dummytest", Status: t.status})
		s.output.Next(1) // skip the signature.
		flags := s.output.Next(2)
		testStatus := flags[1] & 0x7 // Last three bits of the second byte.
		c.Check(testStatus, check.Equals, t.flag,
			check.Commentf("Wrong status for %s", t.status))
	}
}

func (s *SubunitSuite) TestPacketLength(c *check.C) {
	s.stream.Status(subunit.Event{TestID: "", Status: "dummystatus"})
	s.output.Next(3) // skip the signature (1 byte) and the flags (2 bytes)
	length := s.output.Next(1)[0]
	// signature (1 byte) + flags (2 bytes) + length (2 bytes) + CRC32 (4 bytes)
	var expectedLength byte = 8
	c.Assert(length, check.Equals, expectedLength, check.Commentf("Wrong length"))
}

func (s *SubunitSuite) TestPacketCRC32(c *check.C) {
	s.stream.Status(subunit.Event{TestID: "", Status: ""})
	// skip the signature (1 byte), the flags (2 bytes) and the length (1 byte)
	s.output.Next(4)
	crc := s.output.Next(4)
	expectedCRC32 := make([]byte, 4)
	binary.BigEndian.PutUint32(expectedCRC32,
		// signature = 0xb3
		// flags with only version = 0x20 0x0
		// size = 0x8
		crc32.ChecksumIEEE([]byte{0xb3, 0x20, 0x0, 0x8}))
	c.Assert(crc, check.DeepEquals, expectedCRC32, check.Commentf("Wrong CRC32"))
	// Check against a CRC generated with python's subunit.
	c.Assert(crc, check.DeepEquals, []byte{0x18, 0x15, 0xf0, 0xba},
		check.Commentf("Wrong CRC32"))
}

var idtests = []struct {
	testIDPrefix  string
	testIDLen     int
	packetLenSize int // The number of bytes in the packet length.
}{
	{"test-id (1 byte)", 16, 1},
	{"test-id-with-63-chars (1 byte)", 63, 2},
	{"test-id-with-64-chars (2 bytes)", 64, 2},
	{"test-id-with-16383-chars (2 bytes)", 16383, 3},
	{"test-id-with-16384-chars (3 bytes)", 16384, 3},
	// The size limit of the packet is 4194303. This is the biggest name possible.
	{"test-id-with-4194290-chars (3 bytes)", 4194290, 3},
}

func (s *SubunitSuite) TestPacketTestID(c *check.C) {
	for _, t := range idtests {
		s.output.Reset()
		testID := t.testIDPrefix + strings.Repeat("_", t.testIDLen-len(t.testIDPrefix))
		s.stream.Status(subunit.Event{TestID: testID, Status: ""})
		// skip the signature (1 byte) and the flags (2 bytes)
		s.output.Next(3)
		// skip the packet length (variable size)
		s.readNumber()
		idLen := s.readNumber()
		c.Check(idLen, check.Equals, len(testID), check.Commentf("Wrong length"))
		id := string(s.output.Next(idLen))
		c.Check(id, check.Equals, testID, check.Commentf("Wrong ID"))
	}
}

func (s *SubunitSuite) TestWithoutTimestampPacketMustNotSetPresentFlag(c *check.C) {
	s.stream.Status(subunit.Event{})
	s.output.Next(1) // skip the signature.
	flags := s.output.Next(2)
	testIDPresent := flags[0] & 0x2 // bit 9.
	c.Assert(testIDPresent, check.Equals, uint8(0x0),
		check.Commentf("Timestamp present flag is set"))
}

func (s *SubunitSuite) TestWithTimestampPacketMustSetPresentFlag(c *check.C) {
	s.stream.Status(subunit.Event{Timestamp: time.Now()})
	s.output.Next(1) // skip the signature.
	flags := s.output.Next(2)
	testIDPresent := flags[0] & 0x2 // bit 9.
	c.Assert(testIDPresent, check.Equals, uint8(0x2),
		check.Commentf("Timestamp present flag is not set"))
}

func (s *SubunitSuite) TestPacketTimestamp(c *check.C) {
	t := time.Now()
	s.stream.Status(subunit.Event{Timestamp: t})
	// skip the signature (1 byte) and the flags (2 bytes)
	s.output.Next(3)
	// skip the packet length (variable size)
	s.readNumber()
	var sec uint32
	secondsBytes := s.output.Next(4)
	err := binary.Read(bytes.NewReader(secondsBytes), binary.BigEndian, &sec)
	c.Assert(err, check.IsNil, check.Commentf("Error reading the timestamp seconds: %s", err))
	nsec := s.readNumber()

	timestamp := time.Unix(int64(sec), int64(nsec))
	c.Assert(timestamp, check.Equals, t, check.Commentf("Wrong timestamp"))
}
