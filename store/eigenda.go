package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Layr-Labs/eigenda-proxy/verify"
	"github.com/Layr-Labs/eigenda/api/clients"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

type EigenDAStoreConfig struct {
	MaxBlobSizeBytes uint64
	// the # of Ethereum blocks to wait after the EigenDA L1BlockReference # before attempting to verify
	// & accredit a blob
	EthConfirmationDepth uint64

	// total duration time that client waits for blob to confirm
	StatusQueryTimeout time.Duration
}

// EigenDAStore does storage interactions and verifications for blobs with DA.
type EigenDAStore struct {
	client    *clients.EigenDAClient
	verifier  *verify.Verifier
	cfg       *EigenDAStoreConfig
	log       log.Logger
	wvmClient *WVMClient
}

var _ KeyGeneratedStore = (*EigenDAStore)(nil)

func NewEigenDAStore(client *clients.EigenDAClient,
	v *verify.Verifier, log log.Logger, cfg *EigenDAStoreConfig, wvmClient *WVMClient) (*EigenDAStore, error) {
	return &EigenDAStore{
		client:    client,
		verifier:  v,
		log:       log,
		cfg:       cfg,
		wvmClient: wvmClient,
	}, nil
}

// Get fetches a blob from DA using certificate fields and verifies blob
// against commitment to ensure data is valid and non-tampered.
func (e EigenDAStore) Get(ctx context.Context, key []byte) ([]byte, error) {
	var cert verify.Certificate
	err := rlp.DecodeBytes(key, &cert)
	if err != nil {
		return nil, fmt.Errorf("failed to decode DA cert to RLP format: %w", err)
	}

	decodedBlob, err := e.client.GetBlob(ctx, cert.BlobVerificationProof.BatchMetadata.BatchHeaderHash, cert.BlobVerificationProof.BlobIndex)
	if err != nil {
		return nil, fmt.Errorf("EigenDA client failed to retrieve decoded blob: %w", err)
	}

	return decodedBlob, nil
}

// Put disperses a blob for some pre-image and returns the associated RLP encoded certificate commit.
func (e EigenDAStore) Put(ctx context.Context, value []byte) ([]byte, error) {
	encodedBlob, err := e.client.GetCodec().EncodeBlob(value)
	if err != nil {
		return nil, fmt.Errorf("EigenDA client failed to re-encode blob: %w", err)
	}
	// WVM: check that the data is lower than 100kb - Set it in configs via proxy config
	if uint64(len(encodedBlob)) > e.cfg.MaxBlobSizeBytes {
		return nil, fmt.Errorf("encoded blob is larger than max blob size: blob length %d, encoded blob length: %d, max blob size %d", len(value), len(encodedBlob), e.cfg.MaxBlobSizeBytes)
	}

	dispersalStart := time.Now()
	blobInfo, err := e.client.PutBlob(ctx, value)
	if err != nil {
		return nil, err
	}
	cert := (*verify.Certificate)(blobInfo)

	err = e.verifier.VerifyCommitment(cert.BlobHeader.Commitment, encodedBlob)
	if err != nil {
		return nil, err
	}

	dispersalDuration := time.Since(dispersalStart)
	remainingTimeout := e.cfg.StatusQueryTimeout - dispersalDuration

	ticker := time.NewTicker(12 * time.Second) // avg. eth block time
	defer ticker.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), remainingTimeout)
	defer cancel()

	done := false
	for !done {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out when trying to verify the DA certificate for a blob batch after dispersal")
		case <-ticker.C:
			err = e.verifier.VerifyCert(cert)
			switch {
			case err == nil:
				done = true
			case errors.Is(err, verify.ErrBatchMetadataHashNotFound):
				e.log.Info("Blob confirmed, waiting for sufficient confirmation depth...", "targetDepth", e.cfg.EthConfirmationDepth)
			default:
				return nil, err
			}
		}
	}

	// WVM: we store the encoded blob in wvm
	err = e.wvmClient.Store(ctx, cert, encodedBlob)
	if err != nil {
		return nil, err
	}

	bytes, err := rlp.EncodeToBytes(cert)
	if err != nil {
		return nil, fmt.Errorf("failed to encode DA cert to RLP format: %w", err)
	}

	return bytes, nil
}

// Entries are a no-op for EigenDA Store
func (e EigenDAStore) Stats() *Stats {
	return nil
}

// Backend returns the backend type for EigenDA Store
func (e EigenDAStore) BackendType() BackendType {
	return EigenDA
}

// Key is used to recover certificate fields and that verifies blob
// against commitment to ensure data is valid and non-tampered.
func (e EigenDAStore) Verify(key []byte, value []byte) error {
	var cert verify.Certificate
	err := rlp.DecodeBytes(key, &cert)
	if err != nil {
		return fmt.Errorf("failed to decode DA cert to RLP format: %w", err)
	}

	// re-encode blob for verification
	encodedBlob, err := e.client.GetCodec().EncodeBlob(value)
	if err != nil {
		return fmt.Errorf("EigenDA client failed to re-encode blob: %w", err)
	}

	// verify kzg data commitment
	err = e.verifier.VerifyCommitment(cert.BlobHeader.Commitment, encodedBlob)
	if err != nil {
		return err
	}

	// verify DA certificate against on-chain
	return e.verifier.VerifyCert(&cert)
}

// GetWvmTxHashByCommitment uses commitment to get wvm tx hash from the internal map(temprorary hack)
// and returns it to the caller
func (e EigenDAStore) GetWvmTxHashByCommitment(ctx context.Context, key []byte) (string, error) {
	e.log.Info("try get wvm tx hash using provided commitment")
	var cert verify.Certificate
	err := rlp.DecodeBytes(key, &cert)
	if err != nil {
		return "", fmt.Errorf("failed to decode DA cert to RLP format: %w", err)
	}

	wvmTxHash, err := e.wvmClient.GetWvmTxHashByCommitment(ctx, &cert)
	if err != nil {
		return "", err
	}

	return wvmTxHash, nil
}

func (e EigenDAStore) GetBlobFromWvm(ctx context.Context, key []byte) ([]byte, error) {
	var cert verify.Certificate
	err := rlp.DecodeBytes(key, &cert)
	if err != nil {
		return nil, fmt.Errorf("failed to decode DA cert to RLP format: %w", err)
	}

	wvmTxHash, err := e.wvmClient.GetWvmTxHashByCommitment(ctx, &cert)
	if err != nil {
		return nil, err
	}

	e.log.Info("found wvm tx hash using provided commitment", "provided key", commitmentKey(cert.BlobVerificationProof.BatchId, cert.BlobVerificationProof.BlobIndex))

	wvmDecodedBlob, err := e.wvmClient.GetBlobFromWvm(ctx, wvmTxHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get eigenda blob from wvm: %w", err)
	}

	decodedData, err := e.client.Codec.DecodeBlob(wvmDecodedBlob)
	if err != nil {
		return nil, fmt.Errorf("error decoding eigen blob: %w", err)
	}

	return decodedData, nil
}
