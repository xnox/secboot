// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2020 Canonical Ltd
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

package luks2_test

import (
	"bytes"
	"encoding/base64"
	"math/rand"
	"os"
	"os/exec"
	"time"

	snapd_testutil "github.com/snapcore/snapd/testutil"

	. "gopkg.in/check.v1"

	. "github.com/snapcore/secboot/internal/luks2"
	"github.com/snapcore/secboot/internal/luks2/luks2test"
	"github.com/snapcore/secboot/internal/paths/pathstest"
)

type cryptsetupSuite struct {
	snapd_testutil.BaseTest
}

func (s *cryptsetupSuite) SetUpSuite(c *C) {
	if _, exists := os.LookupEnv("NO_EXPENSIVE_CRYPTSETUP_TESTS"); exists {
		c.Skip("skipping expensive cryptsetup tests")
	}
}

func (s *cryptsetupSuite) SetUpTest(c *C) {
	s.BaseTest.SetUpTest(c)

	s.AddCleanup(pathstest.MockRunDir(c.MkDir()))

	s.AddCleanup(luks2test.WrapCryptsetup(c))
}

func (s *cryptsetupSuite) checkLUKS2Passphrase(c *C, path string, key []byte) {
	cmd := exec.Command("cryptsetup", "open", "--test-passphrase", "--key-file", "-", path)
	cmd.Stdin = bytes.NewReader(key)
	c.Check(cmd.Run(), IsNil)
}

var _ = Suite(&cryptsetupSuite{})

type testFormatData struct {
	label   string
	key     []byte
	options *FormatOptions
}

func (s *cryptsetupSuite) testFormat(c *C, data *testFormatData) {
	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	c.Check(Format(devicePath, data.label, data.key, data.options), IsNil)

	options := data.options
	if options == nil {
		options = new(FormatOptions)
	}

	info, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)

	c.Check(info.Label, Equals, data.label)

	c.Check(info.Metadata.Keyslots, HasLen, 1)
	keyslot, ok := info.Metadata.Keyslots[0]
	c.Assert(ok, Equals, true)
	c.Check(keyslot.KeySize, Equals, 64)
	c.Check(keyslot.Priority, Equals, SlotPriorityNormal)
	c.Assert(keyslot.KDF, NotNil)
	c.Check(keyslot.KDF.Type, Equals, KDFTypeArgon2i)

	c.Check(info.Metadata.Segments, HasLen, 1)
	segment, ok := info.Metadata.Segments[0]
	c.Assert(ok, Equals, true)
	c.Check(segment.Encryption, Equals, "aes-xts-plain64")

	c.Check(info.Metadata.Tokens, HasLen, 0)

	expectedMetadataSize := uint64(16 * 1024)
	if options.MetadataKiBSize > 0 {
		expectedMetadataSize = uint64(options.MetadataKiBSize * 1024)
	}
	expectedKeyslotsSize := uint64(16*1024*1024) - (2 * expectedMetadataSize)
	if options.KeyslotsAreaKiBSize > 0 {
		expectedKeyslotsSize = uint64(options.KeyslotsAreaKiBSize * 1024)
	}

	c.Check(info.Metadata.Config.JSONSize, Equals, expectedMetadataSize-uint64(4*1024))
	c.Check(info.Metadata.Config.KeyslotsSize, Equals, expectedKeyslotsSize)

	expectedMemoryKiB := 1 * 1024 * 1024
	if options.KDFOptions.MemoryKiB > 0 {
		expectedMemoryKiB = options.KDFOptions.MemoryKiB
	}

	if options.KDFOptions.ForceIterations > 0 {
		c.Check(keyslot.KDF.Time, Equals, options.KDFOptions.ForceIterations)
		c.Check(keyslot.KDF.Memory, Equals, expectedMemoryKiB)
	} else {
		expectedKDFTime := 2000 * time.Millisecond
		if options.KDFOptions.TargetDuration > 0 {
			expectedKDFTime = options.KDFOptions.TargetDuration
		}

		c.Check(keyslot.KDF.Memory, snapd_testutil.IntLessEqual, expectedMemoryKiB)

		start := time.Now()
		luks2test.CheckLUKS2Passphrase(c, devicePath, data.key)
		elapsed := time.Now().Sub(start)
		// Check KDF time here with +/-20% tolerance and additional 500ms for cryptsetup exec and other activities
		c.Check(int(elapsed/time.Millisecond), snapd_testutil.IntGreaterThan, int(float64(expectedKDFTime/time.Millisecond)*0.8))
		c.Check(int(elapsed/time.Millisecond), snapd_testutil.IntLessThan, int(float64(expectedKDFTime/time.Millisecond)*1.2)+500)
	}
}

func (s *cryptsetupSuite) TestFormatDefaults(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label:   "test",
		key:     key,
		options: &FormatOptions{}})
}

func (s *cryptsetupSuite) TestFormatNilOptions(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label: "test",
		key:   key})
}

func (s *cryptsetupSuite) TestFormatWithCustomKDFTime(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label:   "test",
		key:     key,
		options: &FormatOptions{KDFOptions: KDFOptions{TargetDuration: 100 * time.Millisecond}}})
}

func (s *cryptsetupSuite) TestFormatWithCustomKDFMemory(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label:   "data",
		key:     key,
		options: &FormatOptions{KDFOptions: KDFOptions{TargetDuration: 100 * time.Millisecond, MemoryKiB: 32 * 1024}}})
}

func (s *cryptsetupSuite) TestFormatWithForceIterations(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label:   "data",
		key:     key,
		options: &FormatOptions{KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}}})
}

func (s *cryptsetupSuite) TestFormatWithDifferentLabel(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label:   "data",
		key:     key,
		options: &FormatOptions{KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}}})
}

func (s *cryptsetupSuite) TestFormatWithCustomMetadataSize(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label: "test",
		key:   key,
		options: &FormatOptions{
			KDFOptions:      KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4},
			MetadataKiBSize: 2 * 1024}})
}

func (s *cryptsetupSuite) TestFormatWithCustomKeyslotsAreaSize(c *C) {
	key := make([]byte, 32)
	rand.Read(key)
	s.testFormat(c, &testFormatData{
		label: "test",
		key:   key,
		options: &FormatOptions{
			KDFOptions:          KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4},
			KeyslotsAreaKiBSize: 2 * 1024}})
}

type testAddKeyData struct {
	key     []byte
	options *AddKeyOptions
	time    time.Duration
}

func (s *cryptsetupSuite) testAddKey(c *C, data *testAddKeyData) {
	primaryKey := make([]byte, 32)
	rand.Read(primaryKey)

	devicePath := luks2test.CreateEmptyDiskImage(c, 20)
	fmtOpts := FormatOptions{KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}}
	c.Assert(Format(devicePath, "", primaryKey, &fmtOpts), IsNil)

	startInfo, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)

	c.Check(AddKey(devicePath, primaryKey, data.key, data.options), IsNil)

	endInfo, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)

	newSlotId := -1
	for s := range endInfo.Metadata.Keyslots {
		if _, ok := startInfo.Metadata.Keyslots[s]; !ok {
			newSlotId = int(s)
			break
		}
	}

	options := data.options
	if options == nil {
		options = &AddKeyOptions{Slot: AnySlot}
	}

	c.Assert(newSlotId, snapd_testutil.IntGreaterThan, -1)
	if options.Slot != AnySlot {
		c.Check(newSlotId, Equals, options.Slot)
	}

	c.Check(endInfo.Metadata.Keyslots, HasLen, 2)
	keyslot, ok := endInfo.Metadata.Keyslots[newSlotId]
	c.Assert(ok, Equals, true)
	c.Check(keyslot.KeySize, Equals, 64)
	c.Check(keyslot.Priority, Equals, SlotPriorityNormal)
	c.Assert(keyslot.KDF, NotNil)
	c.Check(keyslot.KDF.Type, Equals, KDFTypeArgon2i)

	expectedMemoryKiB := 1 * 1024 * 1024
	if options.KDFOptions.MemoryKiB > 0 {
		expectedMemoryKiB = options.KDFOptions.MemoryKiB
	}

	if options.KDFOptions.ForceIterations > 0 {
		c.Check(keyslot.KDF.Time, Equals, options.KDFOptions.ForceIterations)
		c.Check(keyslot.KDF.Memory, Equals, expectedMemoryKiB)
	} else {
		expectedKDFTime := 2000 * time.Millisecond
		if options.KDFOptions.TargetDuration > 0 {
			expectedKDFTime = options.KDFOptions.TargetDuration
		}

		c.Check(keyslot.KDF.Memory, snapd_testutil.IntLessEqual, expectedMemoryKiB)

		start := time.Now()
		luks2test.CheckLUKS2Passphrase(c, devicePath, data.key)
		elapsed := time.Now().Sub(start)
		// Check KDF time here with +/-20% tolerance and additional 500ms for cryptsetup exec and other activities
		c.Check(int(elapsed/time.Millisecond), snapd_testutil.IntGreaterThan, int(float64(expectedKDFTime/time.Millisecond)*0.8))
		c.Check(int(elapsed/time.Millisecond), snapd_testutil.IntLessThan, int(float64(expectedKDFTime/time.Millisecond)*1.2)+500)
	}
}

func (s *cryptsetupSuite) TestAddKeyDefaults(c *C) {
	key := make([]byte, 32)
	rand.Read(key)

	s.testAddKey(c, &testAddKeyData{
		key:     key,
		options: &AddKeyOptions{Slot: AnySlot}})
}

func (s *cryptsetupSuite) TestAddKeyNilOptions(c *C) {
	key := make([]byte, 32)
	rand.Read(key)

	s.testAddKey(c, &testAddKeyData{key: key})
}

func (s *cryptsetupSuite) TestAddKeyWithCustomKDFTime(c *C) {
	key := make([]byte, 32)
	rand.Read(key)

	s.testAddKey(c, &testAddKeyData{
		key: key,
		options: &AddKeyOptions{
			KDFOptions: KDFOptions{TargetDuration: 100 * time.Millisecond},
			Slot:       AnySlot}})
}

func (s *cryptsetupSuite) TestAddKeyWithCustomKDFMemory(c *C) {
	key := make([]byte, 32)
	rand.Read(key)

	s.testAddKey(c, &testAddKeyData{
		key: key,
		options: &AddKeyOptions{
			KDFOptions: KDFOptions{TargetDuration: 100 * time.Millisecond, MemoryKiB: 32 * 1024},
			Slot:       AnySlot}})
}

func (s *cryptsetupSuite) TestAddKeyWithForceIterations(c *C) {
	key := make([]byte, 32)
	rand.Read(key)

	s.testAddKey(c, &testAddKeyData{
		key: key,
		options: &AddKeyOptions{
			KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4},
			Slot:       AnySlot}})
}

func (s *cryptsetupSuite) TestAddKeyWithSpecificKeyslot(c *C) {
	key := make([]byte, 32)
	rand.Read(key)

	s.testAddKey(c, &testAddKeyData{
		key: key,
		options: &AddKeyOptions{
			KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4},
			Slot:       8}})
}

func (s *cryptsetupSuite) TestAddKeyWithIncorrectExistingKey(c *C) {
	primaryKey := make([]byte, 32)
	rand.Read(primaryKey)

	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	options := FormatOptions{KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}}
	c.Assert(Format(devicePath, "", primaryKey, &options), IsNil)

	c.Check(AddKey(devicePath, make([]byte, 32), []byte("foo"), nil), ErrorMatches, "cryptsetup failed with: No key available with this passphrase.")

	info, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	c.Check(info.Metadata.Keyslots, HasLen, 1)
	_, ok := info.Metadata.Keyslots[0]
	c.Check(ok, Equals, true)

	luks2test.CheckLUKS2Passphrase(c, devicePath, primaryKey)
}

type testImportTokenData struct {
	token          *Token
	expectedParams map[string]interface{}
}

func (s *cryptsetupSuite) testImportToken(c *C, data *testImportTokenData) {
	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	kdfOptions := KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}
	c.Assert(Format(devicePath, "", make([]byte, 32), &FormatOptions{KDFOptions: kdfOptions}), IsNil)
	c.Assert(AddKey(devicePath, make([]byte, 32), make([]byte, 32), &AddKeyOptions{KDFOptions: kdfOptions, Slot: AnySlot}), IsNil)

	c.Check(ImportToken(devicePath, data.token), IsNil)

	info, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)

	c.Assert(info.Metadata.Tokens, HasLen, 1)
	token, ok := info.Metadata.Tokens[0]
	c.Assert(ok, Equals, true)
	c.Check(token.Type, Equals, data.token.Type)
	c.Check(token.Keyslots, DeepEquals, data.token.Keyslots)
	c.Check(token.Params, DeepEquals, data.expectedParams)
}

func (s *cryptsetupSuite) TestImportToken1(c *C) {
	data := make([]byte, 128)
	rand.Read(data)

	s.testImportToken(c, &testImportTokenData{
		token: &Token{
			Type:     "secboot-test",
			Keyslots: []int{0},
			Params: map[string]interface{}{
				"secboot-a": 50,
				"secboot-b": data}},
		expectedParams: map[string]interface{}{
			"secboot-a": float64(50),
			"secboot-b": base64.StdEncoding.EncodeToString(data)}})
}

func (s *cryptsetupSuite) TestImportToken2(c *C) {
	data := make([]byte, 128)
	rand.Read(data)

	s.testImportToken(c, &testImportTokenData{
		token: &Token{
			Type:     "secboot-test-2",
			Keyslots: []int{1},
			Params: map[string]interface{}{
				"secboot-a": true,
				"secboot-b": data}},
		expectedParams: map[string]interface{}{
			"secboot-a": true,
			"secboot-b": base64.StdEncoding.EncodeToString(data)}})
}

func (s *cryptsetupSuite) TestImportToken3(c *C) {
	data := make([]byte, 128)
	rand.Read(data)

	s.testImportToken(c, &testImportTokenData{
		token: &Token{
			Type:     "secboot-test",
			Keyslots: []int{0, 1},
			Params: map[string]interface{}{
				"secboot-a": 50,
				"secboot-b": data}},
		expectedParams: map[string]interface{}{
			"secboot-a": float64(50),
			"secboot-b": base64.StdEncoding.EncodeToString(data)}})
}

func (s *cryptsetupSuite) testRemoveToken(c *C, tokenId int) {
	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	kdfOptions := KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}
	c.Assert(Format(devicePath, "", make([]byte, 32), &FormatOptions{KDFOptions: kdfOptions}), IsNil)
	c.Assert(AddKey(devicePath, make([]byte, 32), make([]byte, 32), &AddKeyOptions{KDFOptions: kdfOptions, Slot: AnySlot}), IsNil)
	c.Assert(ImportToken(devicePath, &Token{Type: "secboot-foo", Keyslots: []int{0}}), IsNil)
	c.Assert(ImportToken(devicePath, &Token{Type: "secboot-bar", Keyslots: []int{1}}), IsNil)

	info, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	c.Check(info.Metadata.Tokens, HasLen, 2)
	_, ok := info.Metadata.Tokens[tokenId]
	c.Check(ok, Equals, true)

	c.Check(RemoveToken(devicePath, tokenId), IsNil)

	info, err = ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	c.Check(info.Metadata.Tokens, HasLen, 1)
	_, ok = info.Metadata.Tokens[tokenId]
	c.Check(ok, Equals, false)
}

func (s *cryptsetupSuite) TestRemoveToken1(c *C) {
	s.testRemoveToken(c, 0)
}

func (s *cryptsetupSuite) TestRemoveToken2(c *C) {
	s.testRemoveToken(c, 1)
}

func (s *cryptsetupSuite) TestRemoveNonExistantToken(c *C) {
	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	options := FormatOptions{KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}}
	c.Assert(Format(devicePath, "", make([]byte, 32), &options), IsNil)
	c.Assert(ImportToken(devicePath, &Token{Type: "secboot-foo", Keyslots: []int{0}}), IsNil)

	c.Check(RemoveToken(devicePath, 10), ErrorMatches, "cryptsetup failed with: Token 10 is not in use.")

	info, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	c.Check(info.Metadata.Tokens, HasLen, 1)
	_, ok := info.Metadata.Tokens[0]
	c.Check(ok, Equals, true)
}

type testKillSlotData struct {
	key1    []byte
	key2    []byte
	key     []byte
	slotId  int
	testKey []byte
}

func (s *cryptsetupSuite) testKillSlot(c *C, data *testKillSlotData) {
	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	kdfOptions := KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}
	c.Assert(Format(devicePath, "", data.key1, &FormatOptions{KDFOptions: kdfOptions}), IsNil)
	c.Assert(AddKey(devicePath, data.key1, data.key2, &AddKeyOptions{KDFOptions: kdfOptions, Slot: AnySlot}), IsNil)

	info, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	c.Check(info.Metadata.Keyslots, HasLen, 2)
	_, ok := info.Metadata.Keyslots[data.slotId]
	c.Check(ok, Equals, true)

	c.Check(KillSlot(devicePath, data.slotId, data.key), IsNil)

	info, err = ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	c.Check(info.Metadata.Keyslots, HasLen, 1)
	_, ok = info.Metadata.Keyslots[data.slotId]
	c.Check(ok, Equals, false)

	luks2test.CheckLUKS2Passphrase(c, devicePath, data.testKey)
}

func (s *cryptsetupSuite) TestKillSlot1(c *C) {
	key1 := make([]byte, 32)
	rand.Read(key1)
	key2 := make([]byte, 32)
	rand.Read(key2)

	s.testKillSlot(c, &testKillSlotData{
		key1:    key1,
		key2:    key2,
		key:     key1,
		slotId:  1,
		testKey: key1})
}

func (s *cryptsetupSuite) TestKillSlot2(c *C) {
	key1 := make([]byte, 32)
	rand.Read(key1)
	key2 := make([]byte, 32)
	rand.Read(key2)

	s.testKillSlot(c, &testKillSlotData{
		key1:    key1,
		key2:    key2,
		key:     key2,
		slotId:  0,
		testKey: key2})
}

func (s *cryptsetupSuite) TestKillSlotWithWrongPassphrase(c *C) {
	key1 := make([]byte, 32)
	rand.Read(key1)
	key2 := make([]byte, 32)
	rand.Read(key2)

	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	kdfOptions := KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}
	c.Assert(Format(devicePath, "", key1, &FormatOptions{KDFOptions: kdfOptions}), IsNil)
	c.Assert(AddKey(devicePath, key1, key2, &AddKeyOptions{KDFOptions: kdfOptions, Slot: AnySlot}), IsNil)

	c.Check(KillSlot(devicePath, 1, key2), ErrorMatches, "cryptsetup failed with: No key available with this passphrase.")

	luks2test.CheckLUKS2Passphrase(c, devicePath, key1)
	luks2test.CheckLUKS2Passphrase(c, devicePath, key2)
}

func (s *cryptsetupSuite) TestKillNonExistantSlot(c *C) {
	key := make([]byte, 32)
	rand.Read(key)

	devicePath := luks2test.CreateEmptyDiskImage(c, 20)
	options := FormatOptions{KDFOptions: KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}}
	c.Assert(Format(devicePath, "", key, &options), IsNil)

	c.Check(KillSlot(devicePath, 8, key), ErrorMatches, "cryptsetup failed with: Keyslot 8 is not active.")

	luks2test.CheckLUKS2Passphrase(c, devicePath, key)
}

type testSetSlotPriorityData struct {
	slotId   int
	priority SlotPriority
}

func (s *cryptsetupSuite) testSetSlotPriority(c *C, data *testSetSlotPriorityData) {
	devicePath := luks2test.CreateEmptyDiskImage(c, 20)

	kdfOptions := KDFOptions{MemoryKiB: 32 * 1024, ForceIterations: 4}
	c.Assert(Format(devicePath, "", make([]byte, 32), &FormatOptions{KDFOptions: kdfOptions}), IsNil)
	c.Assert(AddKey(devicePath, make([]byte, 32), make([]byte, 32), &AddKeyOptions{KDFOptions: kdfOptions, Slot: AnySlot}), IsNil)

	info, err := ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	keyslot, ok := info.Metadata.Keyslots[data.slotId]
	c.Assert(ok, Equals, true)
	c.Check(keyslot.Priority, Equals, SlotPriorityNormal)

	c.Check(SetSlotPriority(devicePath, data.slotId, data.priority), IsNil)

	info, err = ReadHeader(devicePath, LockModeBlocking)
	c.Assert(err, IsNil)
	keyslot, ok = info.Metadata.Keyslots[data.slotId]
	c.Assert(ok, Equals, true)
	c.Check(keyslot.Priority, Equals, data.priority)
}

func (s *cryptsetupSuite) TestSetSlotPriority1(c *C) {
	s.testSetSlotPriority(c, &testSetSlotPriorityData{
		slotId:   0,
		priority: SlotPriorityHigh})
}

func (s *cryptsetupSuite) TestSetSlotPriority2(c *C) {
	s.testSetSlotPriority(c, &testSetSlotPriorityData{
		slotId:   1,
		priority: SlotPriorityHigh})
}

func (s *cryptsetupSuite) TestSetSlotPriority3(c *C) {
	s.testSetSlotPriority(c, &testSetSlotPriorityData{
		slotId:   1,
		priority: SlotPriorityIgnore})
}
