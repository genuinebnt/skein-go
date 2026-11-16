package fakes_test

import (
	"testing"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/fakes"
	"github.com/genuinebnt/skein-go/services/identity/internal/storetest"
)

func TestFakeStoreMeetsTheStoreContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) app.Store { return fakes.NewStore() })
}
