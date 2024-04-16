package rpc

import (
	"github.com/NethermindEth/juno/core"
	"github.com/NethermindEth/juno/core/felt"
	"github.com/NethermindEth/juno/jsonrpc"
)

type StorageProofs struct {
	StateCommitment felt.Felt     `json:"state_commitment"`
	ClassCommitment felt.Felt     `json:"class_commitment"`
	ContractProof   PROOF         `json:"contract_proof"`
	ContractData    *ContractData `json:"contract_data,omitempty"`
}

type ContractData struct {
	ClassHash                felt.Felt `json:"class_hash"`
	Nonce                    felt.Felt `json:"nonce"`
	Root                     felt.Felt `json:"root"`
	ContractStateHashVersion felt.Felt `json:"contract_state_hash_version"`
	// Contains the requested storage proofs (in order of request)
	StorageProofs []PROOF `json:"storage_proofs"`
}

// Set of merkle tree nodes which constitute a merkle proof. Ordered from root towards the target.
type PROOF []NODE

type NODE struct {
	Binary *BINARY_NODE `json:"binary,omitempty"`
	Edge   *EDGE_NODE   `json:"edge,omitempty"`
}

type BINARY_NODE struct {
	Binary struct {
		Left  felt.Felt `json:"left"`
		Right felt.Felt `json:"right"`
	} `json:"binary"`
}

type EDGE_NODE struct {
	Edge struct {
		Child felt.Felt `json:"child"`
		Path  struct {
			Value felt.Felt `json:"value"`
			Len   int       `json:"len"`
		} `json:"path"`
	} `json:"edge"`
}

func getStorageProofs(address felt.Felt, state core.StateReader, keys []felt.Felt) (*StorageProofs, *jsonrpc.Error) {
	sRoot, err := state.StateTrieRoot()
	if err != nil {
		return nil, jsonrpc.Err(jsonrpc.InternalError, err)
	}
	cRoot, err := state.ClassTrieRoot()
	if err != nil {
		return nil, jsonrpc.Err(jsonrpc.InternalError, err)
	}

	cData, err := getContractData(address, state)
	if err != nil {
		return nil, jsonrpc.Err(jsonrpc.InternalError, err)
	}

	return &StorageProofs{
		StateCommitment: *sRoot,
		ClassCommitment: *cRoot,
		// ContractProof: , // Todo
		ContractData: cData,
	}, nil
}

func getContractData(address felt.Felt, state core.StateReader) (*ContractData, error) {
	classHash, err := state.ContractClassHash(&address)
	if err != nil {
		return nil, err
	}
	nonce, err := state.ContractNonce(&address)
	if err != nil {
		return nil, err
	}
	cRoot, err := state.ContractStorageRoot(&address)
	if err != nil {
		return nil, err
	}
	// Todo: impl ContractStateHashVersion
	// Todo: imp StorageProofs

	return &ContractData{
		ClassHash: *classHash,
		Nonce:     *nonce,
		Root:      *cRoot,
	}, nil
}
