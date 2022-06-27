// Copyright 2021 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package wasmer

import (
	"github.com/ChainSafe/gossamer/lib/transaction"
	"github.com/ChainSafe/gossamer/pkg/scale"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyExtrinsicErrors(t *testing.T) {
	testValidity := &transaction.Validity{
		Priority: 0x3e8,
		Requires: [][]byte{{0xb5, 0x47, 0xb1, 0x90, 0x37, 0x10, 0x7e, 0x1f, 0x79,
			0x4c, 0xa8, 0x69, 0x0, 0xa1, 0xb5, 0x98}},
		Provides: [][]byte{{0xe4, 0x80, 0x7d, 0x1b, 0x67, 0x49, 0x37, 0xbf, 0xc7,
			0x89, 0xbb, 0xdd, 0x88, 0x6a, 0xdd, 0xd6}},
		Longevity: 0x40,
		Propagate: true,
	}
	encValidity, err := scale.Marshal(testValidity)
	require.NoError(t, err)
	validByte := []byte{0, 0}
	validByte = append(validByte, encValidity...)

	apiErrBytes := []byte{1, 0}
	apiErrBytes = append(apiErrBytes, []byte("test err")...)
	testCases := []struct {
		name          string
		test          []byte
		expErr        error
		expValidity   *transaction.Validity
		isValidityErr bool
	}{
		{
			name:   "lookup failed",
			test:   []byte{0, 1, 1, 0},
			expErr: &TransactionValidityError{errLookupFailed},
		},
		{
			name:   "unexpected transaction call",
			test:   []byte{0, 1, 0, 0},
			expErr: &TransactionValidityError{errUnexpectedTxCall},
		},
		{
			name:        "valid path",
			test:        validByte,
			expValidity: testValidity,
		},
		{
			name:   "application error",
			test:   []byte{1, 3},
			expErr: &ApiError{errTransparentApi},
		},
		{
			name:   "api error",
			test:   []byte{1, 0, 5}, // taken from core integration tests
			expErr: &ApiError{errFailedToDecodeReturnValue},
		},
	}

	for _, c := range testCases {
		t.Run(c.name, func(t *testing.T) {
			validity, err := decodeValidity(c.test)
			require.Equal(t, c.expErr, err)
			require.Equal(t, c.expValidity, validity)
		})
	}
}
