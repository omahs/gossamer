// Copyright 2021 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package babe

import (
	"context"
	"testing"
	"time"

	"github.com/ChainSafe/gossamer/dot/types"
	"github.com/ChainSafe/gossamer/lib/crypto/sr25519"
	"github.com/ChainSafe/gossamer/pkg/scale"

	"github.com/stretchr/testify/require"
)

func TestNewEpochHandler(t *testing.T) {
	testHandleSlotFunc := func(epoch uint64, slot Slot, authorityIndex uint32,
		preRuntimeDigest *types.PreRuntimeDigest,
	) error {
		return nil
	}

	epochData := &epochData{
		threshold: scale.MaxUint128,
	}

	sd, err := time.ParseDuration("6s")
	require.NoError(t, err)

	constants := constants{ //nolint:govet
		slotDuration: sd,
		epochLength:  200,
	}

	keypair := keyring.Alice().(*sr25519.Keypair)

	epochHandler, err := newEpochHandler(1, 9999, epochData, constants, testHandleSlotFunc, keypair)
	require.NoError(t, err)
	require.Equal(t, 200, len(epochHandler.slotToPreRuntimeDigest))
	require.Equal(t, uint64(1), epochHandler.epochNumber)
	require.Equal(t, uint64(9999), epochHandler.firstSlot)
	require.Equal(t, constants, epochHandler.constants)
	require.Equal(t, epochData, epochHandler.epochData)
	require.NotNil(t, epochHandler.handleSlot)
}

func TestEpochHandler_run(t *testing.T) {
	sd, err := time.ParseDuration("10ms")
	require.NoError(t, err)
	startSlot := getCurrentSlot(sd)

	var callsToHandleSlot, firstExecutedSlot uint64
	testHandleSlotFunc := func(epoch uint64, slot Slot, authorityIndex uint32,
		preRuntimeDigest *types.PreRuntimeDigest,
	) error {
		require.Equal(t, uint64(1), epoch)
		if callsToHandleSlot == 0 {
			firstExecutedSlot = slot.number
		} else {
			require.Equal(t, firstExecutedSlot+callsToHandleSlot, slot.number)
		}
		require.Equal(t, uint32(0), authorityIndex)
		require.NotNil(t, preRuntimeDigest)
		callsToHandleSlot++
		return nil
	}

	epochData := &epochData{
		threshold: scale.MaxUint128,
	}

	const epochLength uint64 = 100
	constants := constants{ //nolint:govet
		slotDuration: sd,
		epochLength:  epochLength,
	}

	keypair := keyring.Alice().(*sr25519.Keypair)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	epochHandler, err := newEpochHandler(1, startSlot, epochData, constants, testHandleSlotFunc, keypair)
	require.NoError(t, err)
	require.Equal(t, epochLength, uint64(len(epochHandler.slotToPreRuntimeDigest)))

	errCh := make(chan error)
	go epochHandler.run(ctx, errCh)
	timer := time.NewTimer(sd * time.Duration(epochLength))
	select {
	case <-timer.C:
		require.Equal(t, epochLength-(firstExecutedSlot-startSlot), callsToHandleSlot)
	case err := <-errCh:
		timer.Stop()
		require.NoError(t, err)
	}
}
