// Copyright 2023 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package core

import "encoding/json"

type telemetry interface {
	SendMessage(msg json.Marshaler)
}
