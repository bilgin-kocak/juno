package p2p

import (
	"fmt"
	"github.com/NethermindEth/juno/blockchain"
	"github.com/NethermindEth/juno/core"
	"github.com/NethermindEth/juno/core/felt"
	"github.com/NethermindEth/juno/db/pebble"
	"github.com/NethermindEth/juno/encoder"
	"github.com/NethermindEth/juno/utils"
	"github.com/stretchr/testify/assert"
	"golang.org/x/exp/slices"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEncodeDecodeBlocks(t *testing.T) {
	db, _ := pebble.NewMem()
	bc := blockchain.New(db, utils.MAINNET, utils.NewNopZapLogger())
	c := converter{
		blockchain: bc,
	}

	globed, err := filepath.Glob("converter_tests/blocks/*dat")
	if err != nil {
		panic(err)
	}

	for i, filename := range globed {
		f, err := os.Open(filename)
		if err != nil {
			panic(err)
		}
		decoder := encoder.NewDecoder(f)
		block := core.Block{}
		err = decoder.Decode(&block)
		if err != nil {
			panic(err)
		}

		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			runEncodeDecodeBlockTest(t, c, &block)
		})
	}
}

func runEncodeDecodeBlockTest(t *testing.T, c converter, originalBlock *core.Block) {
	// Convert original struct to protobuf struct
	header, err := c.coreBlockToProtobufHeader(originalBlock)
	if err != nil {
		t.Fatalf("to protobuf failed %v", err)
	}

	body, err := c.coreBlockToProtobufBody(originalBlock)
	if err != nil {
		t.Fatalf("to protobuf failed %v", err)
	}

	// Convert protobuf struct back to original struct
	convertedBlock, _, err := protobufHeaderAndBodyToCoreBlock(header, body, utils.MAINNET)
	if err != nil {
		t.Fatalf("back to core failed %v", err)
	}

	// Check if the final struct is equal to the original struct
	assert.Equal(t, originalBlock, convertedBlock)
}

func TestEncodeDecodeStateUpdate(t *testing.T) {
	db, _ := pebble.NewMem()
	blockchain.New(db, utils.MAINNET, utils.NewNopZapLogger()) // So that the encoder get registered
	globed, err := filepath.Glob("converter_tests/state_updates/*dat")
	if err != nil {
		panic(err)
	}

	for i, filename := range globed {
		f, err := os.Open(filename)
		if err != nil {
			panic(err)
		}
		decoder := encoder.NewDecoder(f)
		stateUpdate := core.StateUpdate{}
		err = decoder.Decode(&stateUpdate)
		if err != nil {
			panic(err)
		}

		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			runEncodeDecodeStateUpdateTest(t, &stateUpdate)
		})
	}
}

func runEncodeDecodeStateUpdateTest(t *testing.T, stateUpdate *core.StateUpdate) {
	// Convert original struct to protobuf struct
	protoBuff := coreStateUpdateToProtobufStateUpdate(stateUpdate)
	reencodedStateUpdate := protobufStateUpdateToCoreStateUpdate(protoBuff)

	type storageKV struct {
		key   felt.Felt
		value []core.StorageDiff
	}

	odarray := make([]storageKV, 0)
	for k, diffs := range stateUpdate.StateDiff.StorageDiffs {
		odarray = append(odarray, storageKV{
			key:   k,
			value: diffs,
		})
	}

	rdarray := make([]storageKV, 0)
	for k, diffs := range reencodedStateUpdate.StateDiff.StorageDiffs {
		rdarray = append(rdarray, storageKV{
			key:   k,
			value: diffs,
		})
	}

	slices.SortFunc(odarray, func(a, b storageKV) bool {
		return a.key.Cmp(&b.key) > 0
	})
	slices.SortFunc(rdarray, func(a, b storageKV) bool {
		return a.key.Cmp(&b.key) > 0
	})

	// Check if the final struct is equal to the original struct
	assert.Equal(t, odarray, rdarray)
	assert.True(t, reflect.DeepEqual(stateUpdate.StateDiff.Nonces, reencodedStateUpdate.StateDiff.Nonces))
	assert.Equal(t, stateUpdate.StateDiff.DeclaredV0Classes, reencodedStateUpdate.StateDiff.DeclaredV0Classes)
	assert.Equal(t, stateUpdate.StateDiff.DeclaredV1Classes, reencodedStateUpdate.StateDiff.DeclaredV1Classes)
	assert.Equal(t, stateUpdate.StateDiff.ReplacedClasses, reencodedStateUpdate.StateDiff.ReplacedClasses)
	assert.Equal(t, stateUpdate.StateDiff.DeployedContracts, reencodedStateUpdate.StateDiff.DeployedContracts)
}
