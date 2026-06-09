package id

import (
	"fmt"

	"github.com/google/uuid"
)

// ID is a type alias for UUID to ensure type safety across the app
type ID = uuid.UUID

// New generates a new UUIDv7 (Time-sortable, great for DBs)
func New() ID {
	val, err := uuid.NewV7()
	if err != nil {
		// Panic is acceptable here; if UUID generation fails, the system is broken.
		panic(fmt.Errorf("failed to generate UUIDv7: %w", err))
	}
	return val
}

// Parse converts a string to an ID
func Parse(s string) (ID, error) {
	return uuid.Parse(s)
}

// IsNil checks if the ID is empty
func IsNil(i ID) bool {
	return i == uuid.Nil
}
