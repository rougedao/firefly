// Copyright © 2022 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"testing"

	"github.com/hyperledger/firefly/mocks/blockchainmocks"
	"github.com/hyperledger/firefly/mocks/databasemocks"
	"github.com/hyperledger/firefly/mocks/datamocks"
	"github.com/hyperledger/firefly/mocks/identitymanagermocks"
	"github.com/hyperledger/firefly/mocks/sharedstoragemocks"
	"github.com/hyperledger/firefly/mocks/txcommonmocks"
	"github.com/hyperledger/firefly/pkg/blockchain"
	"github.com/hyperledger/firefly/pkg/database"
	"github.com/hyperledger/firefly/pkg/fftypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func sampleBatch(t *testing.T, batchType fftypes.BatchType, txType fftypes.TransactionType, data fftypes.DataArray) *fftypes.Batch {
	identity := fftypes.SignerRef{Author: "signingOrg", Key: "0x12345"}
	msgType := fftypes.MessageTypeBroadcast
	if batchType == fftypes.BatchTypePrivate {
		msgType = fftypes.MessageTypePrivate
	}
	for _, d := range data {
		err := d.Seal(context.Background(), nil)
		assert.NoError(t, err)
	}
	msg := &fftypes.Message{
		Header: fftypes.MessageHeader{
			SignerRef: identity,
			ID:        fftypes.NewUUID(),
			Type:      msgType,
			TxType:    txType,
			Topics:    fftypes.FFStringArray{"topic1"},
		},
		Data: data.Refs(),
	}
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			SignerRef: identity,
			Type:      batchType,
			ID:        fftypes.NewUUID(),
			Node:      fftypes.NewUUID(),
		},
		Payload: fftypes.BatchPayload{
			TX: fftypes.TransactionRef{
				ID:   fftypes.NewUUID(),
				Type: txType,
			},
			Messages: []*fftypes.Message{msg},
			Data:     data,
		},
	}
	err := msg.Seal(context.Background())
	assert.NoError(t, err)
	batch.Hash = fftypes.HashString(batch.Manifest().String())
	return batch
}

func TestBatchPinCompleteOkBroadcast(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()

	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})
	batchPin := &blockchain.BatchPin{
		Namespace:       "ns1",
		TransactionID:   batch.Payload.TX.ID,
		BatchID:         batch.ID,
		BatchPayloadRef: "Qmf412jQZiuVUtdgnB36FXFX7xg5V6KEbSJ4dpQuhkLyfD",
		Contexts:        []*fftypes.Bytes32{fftypes.NewRandB32()},
		Event: blockchain.Event{
			Name:           "BatchPin",
			BlockchainTXID: "0x12345",
			ProtocolID:     "10/20/30",
		},
	}

	batch.Hash = batch.Payload.Hash()
	batchPin.BatchHash = batch.Hash
	batchDataBytes, err := json.Marshal(&batch)
	assert.NoError(t, err)
	batchReadCloser := ioutil.NopCloser(bytes.NewReader(batchDataBytes))

	mpi := em.sharedstorage.(*sharedstoragemocks.Plugin)
	mpi.On("RetrieveData", mock.Anything, mock.
		MatchedBy(func(pr string) bool { return pr == batchPin.BatchPayloadRef })).
		Return(batchReadCloser, nil)

	mth := em.txHelper.(*txcommonmocks.Helper)
	mth.On("PersistTransaction", mock.Anything, "ns1", batchPin.TransactionID, fftypes.TransactionTypeBatchPin, "0x12345").
		Return(false, fmt.Errorf("pop")).Once()
	mth.On("PersistTransaction", mock.Anything, "ns1", batchPin.TransactionID, fftypes.TransactionTypeBatchPin, "0x12345").
		Return(true, nil)

	mdi := em.database.(*databasemocks.Plugin)
	rag := mdi.On("RunAsGroup", mock.Anything, mock.Anything).Return(nil)
	rag.RunFn = func(a mock.Arguments) {
		// Call through to persistBatch - the hash of our batch will be invalid,
		// which is swallowed without error as we cannot retry (it is logged of course)
		rag.ReturnArguments = mock.Arguments{
			a[1].(func(ctx context.Context) error)(a[0].(context.Context)),
		}
	}

	mdi.On("InsertBlockchainEvent", mock.Anything, mock.MatchedBy(func(e *fftypes.BlockchainEvent) bool {
		return e.Name == batchPin.Event.Name
	})).Return(fmt.Errorf("pop")).Once()
	mdi.On("InsertBlockchainEvent", mock.Anything, mock.MatchedBy(func(e *fftypes.BlockchainEvent) bool {
		return e.Name == batchPin.Event.Name
	})).Return(nil).Times(2)
	mdi.On("InsertEvent", mock.Anything, mock.MatchedBy(func(e *fftypes.Event) bool {
		return e.Type == fftypes.EventTypeBlockchainEventReceived
	})).Return(nil).Times(2)
	mdi.On("InsertPins", mock.Anything, mock.Anything).Return(nil).Once()
	mdi.On("UpsertBatch", mock.Anything, mock.Anything).Return(nil).Once()
	mdi.On("InsertDataArray", mock.Anything, mock.Anything).Return(nil).Once()
	mdi.On("InsertMessages", mock.Anything, mock.Anything).Return(nil).Once()
	mbi := &blockchainmocks.Plugin{}

	mdm := em.data.(*datamocks.Manager)
	mdm.On("UpdateMessageCache", mock.Anything, mock.Anything).Return()

	err = em.BatchPinComplete(mbi, batchPin, &fftypes.VerifierRef{
		Type:  fftypes.VerifierTypeEthAddress,
		Value: "0x12345",
	})
	assert.NoError(t, err)

	mdi.AssertExpectations(t)
	mpi.AssertExpectations(t)
	mth.AssertExpectations(t)
	mdm.AssertExpectations(t)
}

func TestBatchPinCompleteOkPrivate(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()

	batchPin := &blockchain.BatchPin{
		Namespace:     "ns1",
		TransactionID: fftypes.NewUUID(),
		BatchID:       fftypes.NewUUID(),
		Contexts:      []*fftypes.Bytes32{fftypes.NewRandB32()},
		Event: blockchain.Event{
			BlockchainTXID: "0x12345",
		},
	}

	mth := em.txHelper.(*txcommonmocks.Helper)
	mth.On("PersistTransaction", mock.Anything, "ns1", batchPin.TransactionID, fftypes.TransactionTypeBatchPin, "0x12345").Return(true, nil)

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("RunAsGroup", mock.Anything, mock.Anything).Return(nil)
	mdi.On("InsertPins", mock.Anything, mock.Anything).Return(fmt.Errorf("These pins have been seen before")) // simulate replay fallback
	mdi.On("UpsertPin", mock.Anything, mock.Anything).Return(nil)
	mbi := &blockchainmocks.Plugin{}

	err := em.BatchPinComplete(mbi, batchPin, &fftypes.VerifierRef{
		Type:  fftypes.VerifierTypeEthAddress,
		Value: "0xffffeeee",
	})
	assert.NoError(t, err)

	// Call through to persistBatch - the hash of our batch will be invalid,
	// which is swallowed without error as we cannot retry (it is logged of course)
	fn := mdi.Calls[0].Arguments[1].(func(ctx context.Context) error)
	err = fn(context.Background())
	assert.NoError(t, err)

	mdi.AssertExpectations(t)
	mth.AssertExpectations(t)
}

func TestSequencedBroadcastRetrieveIPFSFail(t *testing.T) {
	em, cancel := newTestEventManager(t)

	batch := &blockchain.BatchPin{
		Namespace:       "ns",
		TransactionID:   fftypes.NewUUID(),
		BatchID:         fftypes.NewUUID(),
		BatchPayloadRef: "Qmf412jQZiuVUtdgnB36FXFX7xg5V6KEbSJ4dpQuhkLyfD",
		Contexts:        []*fftypes.Bytes32{fftypes.NewRandB32()},
		Event: blockchain.Event{
			BlockchainTXID: "0x12345",
		},
	}

	cancel() // to avoid retry
	mpi := em.sharedstorage.(*sharedstoragemocks.Plugin)
	mpi.On("RetrieveData", mock.Anything, mock.Anything).Return(nil, fmt.Errorf("pop"))
	mbi := &blockchainmocks.Plugin{}

	err := em.BatchPinComplete(mbi, batch, &fftypes.VerifierRef{
		Type:  fftypes.VerifierTypeEthAddress,
		Value: "0xffffeeee",
	})
	mpi.AssertExpectations(t)
	assert.Regexp(t, "FF10158", err)
}

func TestBatchPinCompleteBadData(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()

	batch := &blockchain.BatchPin{
		Namespace:       "ns",
		TransactionID:   fftypes.NewUUID(),
		BatchID:         fftypes.NewUUID(),
		BatchPayloadRef: "Qmf412jQZiuVUtdgnB36FXFX7xg5V6KEbSJ4dpQuhkLyfD",
		Contexts:        []*fftypes.Bytes32{fftypes.NewRandB32()},
	}
	batchReadCloser := ioutil.NopCloser(bytes.NewReader([]byte(`!json`)))

	mpi := em.sharedstorage.(*sharedstoragemocks.Plugin)
	mpi.On("RetrieveData", mock.Anything, mock.Anything).Return(batchReadCloser, nil)
	mbi := &blockchainmocks.Plugin{}

	err := em.BatchPinComplete(mbi, batch, &fftypes.VerifierRef{
		Type:  fftypes.VerifierTypeEthAddress,
		Value: "0xffffeeee",
	})
	assert.NoError(t, err) // We do not return a blocking error in the case of bad data stored in IPFS
}

func TestBatchPinCompleteNoTX(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()

	batch := &blockchain.BatchPin{}
	mbi := &blockchainmocks.Plugin{}

	err := em.BatchPinComplete(mbi, batch, &fftypes.VerifierRef{
		Type:  fftypes.VerifierTypeEthAddress,
		Value: "0x12345",
	})
	assert.NoError(t, err)
}

func TestBatchPinCompleteBadNamespace(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()

	batch := &blockchain.BatchPin{
		Namespace:     "!bad",
		TransactionID: fftypes.NewUUID(),
		Event: blockchain.Event{
			BlockchainTXID: "0x12345",
		},
	}
	mbi := &blockchainmocks.Plugin{}

	err := em.BatchPinComplete(mbi, batch, &fftypes.VerifierRef{
		Type:  fftypes.VerifierTypeEthAddress,
		Value: "0x12345",
	})
	assert.NoError(t, err)
}

func TestPersistBatchMissingID(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batch, valid, err := em.persistBatch(context.Background(), &fftypes.Batch{})
	assert.False(t, valid)
	assert.Nil(t, batch)
	assert.NoError(t, err)
}

func TestPersistBatchAuthorResolveFail(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batchHash := fftypes.NewRandB32()
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			ID: fftypes.NewUUID(),
			SignerRef: fftypes.SignerRef{
				Author: "author1",
				Key:    "0x12345",
			},
			Hash: batchHash,
		},
		Payload: fftypes.BatchPayload{
			TX: fftypes.TransactionRef{
				Type: fftypes.TransactionTypeBatchPin,
				ID:   fftypes.NewUUID(),
			},
		},
	}
	mim := em.identity.(*identitymanagermocks.Manager)
	mim.On("NormalizeSigningKeyIdentity", mock.Anything, mock.Anything).Return("", fmt.Errorf("pop"))
	batch.Hash = batch.Payload.Hash()
	valid, err := em.persistBatchFromBroadcast(context.Background(), batch, batchHash)
	assert.NoError(t, err) // retryable
	assert.False(t, valid)
}

func TestPersistBatchBadAuthor(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batchHash := fftypes.NewRandB32()
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			ID: fftypes.NewUUID(),
			SignerRef: fftypes.SignerRef{
				Author: "author1",
				Key:    "0x12345",
			},
			Hash: batchHash,
		},
		Payload: fftypes.BatchPayload{
			TX: fftypes.TransactionRef{
				Type: fftypes.TransactionTypeBatchPin,
				ID:   fftypes.NewUUID(),
			},
		},
	}
	mim := em.identity.(*identitymanagermocks.Manager)
	mim.On("NormalizeSigningKeyIdentity", mock.Anything, mock.Anything).Return("author2", nil)
	batch.Hash = batch.Payload.Hash()
	valid, err := em.persistBatchFromBroadcast(context.Background(), batch, batchHash)
	assert.NoError(t, err)
	assert.False(t, valid)
}

func TestPersistBatchMismatchChainHash(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			ID: fftypes.NewUUID(),
			SignerRef: fftypes.SignerRef{
				Author: "author1",
				Key:    "0x12345",
			},
			Hash: fftypes.NewRandB32(),
		},
		Payload: fftypes.BatchPayload{
			TX: fftypes.TransactionRef{
				Type: fftypes.TransactionTypeBatchPin,
				ID:   fftypes.NewUUID(),
			},
		},
	}
	mim := em.identity.(*identitymanagermocks.Manager)
	mim.On("NormalizeSigningKeyIdentity", mock.Anything, mock.Anything).Return("author1", nil)
	batch.Hash = batch.Payload.Hash()
	valid, err := em.persistBatchFromBroadcast(context.Background(), batch, fftypes.NewRandB32())
	assert.NoError(t, err)
	assert.False(t, valid)
}

func TestPersistBatchUpsertBatchMismatchHash(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("UpsertBatch", mock.Anything, mock.Anything).Return(database.HashMismatch)

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.False(t, valid)
	assert.Nil(t, bp)
	assert.NoError(t, err)
	mdi.AssertExpectations(t)
}

func TestPersistBatchBadHash(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})
	batch.Hash = fftypes.NewRandB32()

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.False(t, valid)
	assert.Nil(t, bp)
	assert.NoError(t, err)
}

func TestPersistBatchNoData(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			ID: fftypes.NewUUID(),
			SignerRef: fftypes.SignerRef{
				Author: "author1",
				Key:    "0x12345",
			},
		},
		Payload: fftypes.BatchPayload{
			TX: fftypes.TransactionRef{
				Type: fftypes.TransactionTypeBatchPin,
				ID:   fftypes.NewUUID(),
			},
		},
	}
	batch.Hash = fftypes.NewRandB32()

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.False(t, valid)
	assert.Nil(t, bp)
	assert.NoError(t, err)
}

func TestPersistBatchUpsertBatchFail(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("UpsertBatch", mock.Anything, mock.Anything).Return(fmt.Errorf("pop"))

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.Nil(t, bp)
	assert.False(t, valid)
	assert.EqualError(t, err, "pop")
}

func TestPersistBatchSwallowBadData(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			ID: fftypes.NewUUID(),
			SignerRef: fftypes.SignerRef{
				Author: "author1",
				Key:    "0x12345",
			},
			Namespace: "ns1",
		},
		Payload: fftypes.BatchPayload{
			TX: fftypes.TransactionRef{
				Type: fftypes.TransactionTypeBatchPin,
				ID:   fftypes.NewUUID(),
			},
			Messages: []*fftypes.Message{nil},
			Data:     fftypes.DataArray{nil},
		},
	}
	batch.Hash = batch.Payload.Hash()

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("UpsertBatch", mock.Anything, mock.Anything).Return(nil)

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.False(t, valid)
	assert.NoError(t, err)
	assert.Nil(t, bp)
	mdi.AssertExpectations(t)
}

func TestPersistBatchGoodDataUpsertOptimizFail(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("UpsertBatch", mock.Anything, mock.Anything).Return(nil)
	mdi.On("InsertDataArray", mock.Anything, mock.Anything).Return(fmt.Errorf("optimzation miss"))
	mdi.On("UpsertData", mock.Anything, mock.Anything, database.UpsertOptimizationExisting).Return(fmt.Errorf("pop"))

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.Nil(t, bp)
	assert.False(t, valid)
	assert.EqualError(t, err, "pop")
}

func TestPersistBatchGoodDataMessageFail(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("UpsertBatch", mock.Anything, mock.Anything).Return(nil)
	mdi.On("InsertDataArray", mock.Anything, mock.Anything).Return(nil)
	mdi.On("InsertMessages", mock.Anything, mock.Anything).Return(fmt.Errorf("optimzation miss"))
	mdi.On("UpsertMessage", mock.Anything, mock.Anything, database.UpsertOptimizationExisting).Return(fmt.Errorf("pop"))

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.False(t, valid)
	assert.Nil(t, bp)
	assert.EqualError(t, err, "pop")
}

func TestPersistBatchGoodMessageAuthorMismatch(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})
	batch.Payload.Messages[0].Header.Key = "0x9999999"
	batch.Payload.Messages[0].Header.DataHash = batch.Payload.Messages[0].Data.Hash()
	batch.Payload.Messages[0].Hash = batch.Payload.Messages[0].Header.Hash()
	batch.Hash = batch.Payload.Hash()

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("UpsertBatch", mock.Anything, mock.Anything).Return(nil)

	bp, valid, err := em.persistBatch(context.Background(), batch)
	assert.Nil(t, bp)
	assert.False(t, valid)
	assert.NoError(t, err)
}

func TestPersistBatchDataNilData(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			ID: fftypes.NewUUID(),
		},
	}
	data := &fftypes.Data{
		ID: fftypes.NewUUID(),
	}
	valid := em.validateBatchData(context.Background(), batch, 0, data)
	assert.False(t, valid)
}

func TestPersistBatchDataBadHash(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	data := &fftypes.Data{
		ID:    fftypes.NewUUID(),
		Value: fftypes.JSONAnyPtr(`"test"`),
	}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})
	batch.Payload.Data[0].Hash = fftypes.NewRandB32()
	valid := em.validateBatchData(context.Background(), batch, 0, data)
	assert.False(t, valid)
}

func TestPersistBatchDataOk(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()

	data := &fftypes.Data{ID: fftypes.NewUUID(), Value: fftypes.JSONAnyPtr(`"test"`)}
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{data})

	valid := em.validateBatchData(context.Background(), batch, 0, data)
	assert.True(t, valid)
}

func TestPersistBatchMessageNilData(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batch := &fftypes.Batch{
		BatchHeader: fftypes.BatchHeader{
			ID: fftypes.NewUUID(),
		},
	}
	msg := &fftypes.Message{
		Header: fftypes.MessageHeader{
			ID: fftypes.NewUUID(),
		},
	}
	valid := em.validateBatchMessage(context.Background(), batch, 0, msg)
	assert.False(t, valid)
}

func TestPersistBatchMessageOK(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()
	batch := sampleBatch(t, fftypes.BatchTypeBroadcast, fftypes.TransactionTypeBatchPin, fftypes.DataArray{})

	valid := em.validateBatchMessage(context.Background(), batch, 0, batch.Payload.Messages[0])
	assert.True(t, valid)
}

func TestPersistContextsFail(t *testing.T) {
	em, cancel := newTestEventManager(t)
	defer cancel()

	mdi := em.database.(*databasemocks.Plugin)
	mdi.On("InsertPins", mock.Anything, mock.Anything).Return(fmt.Errorf("duplicate pins"))
	mdi.On("UpsertPin", mock.Anything, mock.Anything).Return(fmt.Errorf("pop"))

	err := em.persistContexts(em.ctx, &blockchain.BatchPin{
		Contexts: []*fftypes.Bytes32{
			fftypes.NewRandB32(),
		},
	}, &fftypes.VerifierRef{
		Type:  fftypes.VerifierTypeEthAddress,
		Value: "0x12345",
	}, false)
	assert.EqualError(t, err, "pop")
	mdi.AssertExpectations(t)
}
