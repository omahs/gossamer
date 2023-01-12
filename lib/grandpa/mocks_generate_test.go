// Copyright 2021 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package grandpa

//go:generate mockgen -destination=mocks_test.go -package $GOPACKAGE . BlockState,GrandpaState,Network
//go:generate mockgen -source=interfaces_mock_source.go -destination=mocks_local_test.go -package $GOPACKAGE
//go:generate mockgen -destination=mock_telemetry_test.go -package $GOPACKAGE . Telemetry
