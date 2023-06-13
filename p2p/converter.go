package p2p

import (
	"github.com/NethermindEth/juno/blockchain"
	"github.com/NethermindEth/juno/core"
	"github.com/NethermindEth/juno/core/felt"
	"github.com/NethermindEth/juno/p2p/grpcclient"
	"github.com/NethermindEth/juno/utils"
	"github.com/bits-and-blooms/bloom/v3"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

type converter struct {
	blockchain *blockchain.Blockchain
}

func (c *converter) coreBlockToProtobufHeader(block *core.Block) (*grpcclient.BlockHeader, error) {
	txCommitment, err := block.CalculateTransactionCommitment()
	if err != nil {
		return nil, errors.Wrap(err, "unable to calculate transaction commitment")
	}

	eventCommitment, err := block.CalculateEventCommitment()
	if err != nil {
		return nil, errors.Wrap(err, "unable to calculate event commitment")
	}

	return &grpcclient.BlockHeader{
		Hash:                  feltToFieldElement(block.Hash),
		ParentBlockHash:       feltToFieldElement(block.ParentHash),
		BlockNumber:           block.Number,
		GlobalStateRoot:       feltToFieldElement(block.GlobalStateRoot),
		SequencerAddress:      feltToFieldElement(block.SequencerAddress),
		BlockTimestamp:        block.Timestamp,
		TransactionCount:      uint32(len(block.Transactions)),
		TransactionCommitment: feltToFieldElement(txCommitment),
		EventCount:            uint32(block.EventCount),
		EventCommitment:       feltToFieldElement(eventCommitment),
		ProtocolVersion:       0, //TODO: What is the correct value here?
	}, nil
}

func (c *converter) coreBlockToProtobufBody(block *core.Block) (*grpcclient.BlockBody, error) {
	grpctransactions := make([]*grpcclient.Transaction, len(block.Transactions))
	grpcreceipts := make([]*grpcclient.Receipt, len(block.Receipts))
	for i, transaction := range block.Transactions {
		tx, receipt, err := c.coreTxToProtobufTx(transaction, block.Receipts[i])
		if err != nil {
			return nil, errors.Wrap(err, "unable convert core block to protobuff")
		}

		grpctransactions[i] = tx
		grpcreceipts[i] = receipt
	}

	return &grpcclient.BlockBody{
		Transactions: grpctransactions,
		Receipts:     grpcreceipts,
	}, nil
}

func coreEventToProtobuf(events []*core.Event) []*grpcclient.Event {
	grpcevents := make([]*grpcclient.Event, len(events))
	for i, event := range events {
		grpcevents[i] = &grpcclient.Event{
			FromAddress: feltToFieldElement(event.From),
			Keys:        feltsToFieldElements(event.Keys),
			Data:        feltsToFieldElements(event.Data),
		}
	}

	return grpcevents
}

func coreL2ToL1MessageToProtobuf(messages []*core.L2ToL1Message) []*grpcclient.MessageToL1 {
	grpcmessages := make([]*grpcclient.MessageToL1, len(messages))

	for i, message := range messages {
		grpcmessages[i] = &grpcclient.MessageToL1{
			FromAddress: feltToFieldElement(message.From),
			Payload:     feltsToFieldElements(message.Payload),
			ToAddress:   addressToProto(message.To),
		}
	}

	return grpcmessages
}

func addressToProto(to common.Address) *grpcclient.EthereumAddress {
	return &grpcclient.EthereumAddress{
		Elements: to.Bytes(),
	}
}

func protoToAddress(to *grpcclient.EthereumAddress) common.Address {
	addr := common.Address{}
	if to != nil {
		copy(addr[:], to.Elements)
	}
	return addr
}

func protobufHeaderAndBodyToCoreBlock(header *grpcclient.BlockHeader, body *grpcclient.BlockBody, network utils.Network) (*core.Block, map[felt.Felt]core.Class, error) {
	parentHash := fieldElementToFelt(header.ParentBlockHash)
	globalStateRoot := fieldElementToFelt(header.GlobalStateRoot)
	sequencerAddress := fieldElementToFelt(header.SequencerAddress)
	// TODO: these are validation
	// txCommitment := fieldElementToFelt(header.TransactionCommitment)
	// eventCommitment := fieldElementToFelt(header.EventCommitment)

	block := &core.Block{
		Header: &core.Header{
			Hash:             fieldElementToFelt(header.Hash),
			ParentHash:       parentHash,
			Number:           header.BlockNumber,
			GlobalStateRoot:  globalStateRoot,
			SequencerAddress: sequencerAddress,
			TransactionCount: uint64(len(body.Transactions)),
			EventCount:       0, // many events per receipt
			Timestamp:        header.BlockTimestamp,
			ProtocolVersion:  "",
			ExtraData:        nil,
			EventsBloom:      bloom.New(8192, 6),
		},
		Transactions: make([]core.Transaction, 0), // Assuming it's initialized as an empty slice
		Receipts:     make([]*core.TransactionReceipt, 0),
	}

	eventcount := 0
	declaredClasses := map[felt.Felt]core.Class{}

	for i := uint32(0); i < header.TransactionCount; i++ {
		// Assuming you have a function to convert a transaction from protobuf to core
		transaction, receipt, classHash, class, err := protobufTransactionToCore(body.Transactions[i], body.Receipts[i], network)
		if err != nil {
			return nil, nil, err
		}
		block.Transactions = append(block.Transactions, transaction)
		block.Receipts = append(block.Receipts, receipt)

		if classHash != nil {
			declaredClasses[*classHash] = class
		}

		eventcount = eventcount + len(receipt.Events)
	}

	block.EventCount = uint64(eventcount)
	block.EventsBloom = core.EventsBloom(block.Receipts)

	return block, declaredClasses, nil
}

func protobufCommonReceiptToCoreReceipt(commonReceipt *grpcclient.CommonTransactionReceiptProperties) *core.TransactionReceipt {
	receipt := &core.TransactionReceipt{
		Fee:                fieldElementToFelt(commonReceipt.GetActualFee()),
		Events:             coreEventFromProtobuf(commonReceipt.GetEvents()),
		L2ToL1Message:      coreL2ToL1MessageFromProtobuf(commonReceipt.GetMessagesSent()),
		TransactionHash:    fieldElementToFelt(commonReceipt.GetTransactionHash()),
		ExecutionResources: MapValueViaReflect[*core.ExecutionResources](commonReceipt.GetExecutionResources()),
	}

	return receipt
}

func coreL2ToL1MessageFromProtobuf(sent []*grpcclient.MessageToL1) []*core.L2ToL1Message {
	messages := make([]*core.L2ToL1Message, len(sent))
	for i, grpcMsg := range sent {
		msg := &core.L2ToL1Message{
			From:    fieldElementToFelt(grpcMsg.FromAddress),
			Payload: fieldElementsToFelts(grpcMsg.Payload),
			To:      protoToAddress(grpcMsg.ToAddress),
		}
		messages[i] = msg
	}
	return messages
}

func coreEventFromProtobuf(events []*grpcclient.Event) []*core.Event {
	coreevents := make([]*core.Event, len(events))
	for i, event := range events {
		coreevents[i] = &core.Event{
			Data: fieldElementsToFelts(event.Data),
			From: fieldElementToFelt(event.FromAddress),
			Keys: fieldElementsToFelts(event.Keys),
		}
	}

	return coreevents
}
