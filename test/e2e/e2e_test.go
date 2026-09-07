//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestPicokubeE2E is the suite entrypoint. All Test<NN>_* methods on
// PicokubeE2ESuite are dispatched by testify in lexicographic order.
func TestPicokubeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e suite skipped in -short")
	}
	suite.Run(t, new(PicokubeE2ESuite))
}
