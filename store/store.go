package store

import "context"

type BackendType uint8

const (
	EigenDA BackendType = iota
	Memory
	S3
	Redis

	Unknown
)

func (b BackendType) String() string {
	switch b {
	case EigenDA:
		return "EigenDA"
	case Memory:
		return "Memory"
	case S3:
		return "S3"
	case Redis:
		return "Redis"
	case Unknown:
		fallthrough
	default:
		return "Unknown"
	}
}

func StringToBackendType(s string) BackendType {
	switch s {
	case "EigenDA":
		return EigenDA
	case "Memory":
		return Memory
	case "S3":
		return S3
	case "Redis":
		return Redis
	case "Unknown":
		fallthrough
	default:
		return Unknown
	}
}

// Used for E2E tests
type Stats struct {
	Entries int
	Reads   int
}

type Store interface {
	// Stats returns the current usage metrics of the key-value data store.
	Stats() *Stats
	// Backend returns the backend type provider of the store.
	BackendType() BackendType
	// Verify verifies the given key-value pair.
	Verify(key []byte, value []byte) error
}

type KeyGeneratedStore interface {
	Store
	// Get retrieves the given key if it's present in the key-value data store.
	Get(ctx context.Context, key []byte) ([]byte, error)
	// Put inserts the given value into the key-value data store.
	Put(ctx context.Context, value []byte) (key []byte, err error)
}

type WVMedKeyGeneratedStore interface {
	KeyGeneratedStore
	GetWvmTxHashByCommitment(ctx context.Context, key []byte) (string, error)
	GetBlobFromWvm(ctx context.Context, key []byte) ([]byte, error)
}

type PrecomputedKeyStore interface {
	Store
	// Get retrieves the given key if it's present in the key-value data store.
	Get(ctx context.Context, key []byte) ([]byte, error)
	// Put inserts the given value into the key-value data store.
	Put(ctx context.Context, key []byte, value []byte) error
}
