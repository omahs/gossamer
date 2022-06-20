// Copyright 2021 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package wasmer

import (
	"fmt"
	"github.com/ChainSafe/gossamer/lib/transaction"
	"github.com/ChainSafe/gossamer/pkg/scale"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyExtrinsicErrors(t *testing.T) {
	testValidity := &transaction.Validity{}
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
			expValidity: &transaction.Validity{},
		},
		{
			name:   "api error",
			test:   []byte{1, 0, 5}, // taken from core integration tests
			expErr: &ApiError{},
		},
	}

	for _, c := range testCases {
		t.Run(c.name, func(t *testing.T) {
			validity, err := decodeValidity(c.test)
			if err != nil {
				fmt.Println(err.Error())
			}
			require.Equal(t, c.expErr, err)
			require.Equal(t, c.expValidity, validity)
		})
	}
}
